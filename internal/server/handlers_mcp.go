package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/crmkit/crmkit/internal/auth"
	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/store"
	"github.com/crmkit/crmkit/internal/version"
)

// This file implements the MCP (Model Context Protocol) endpoint at POST /mcp:
// JSON-RPC 2.0 over the Streamable HTTP transport. It exposes crmkit's CRM
// operations as MCP tools so standard chat clients (ChatGPT, Claude) can drive
// the CRM as a connector. Tools are thin wrappers: each replays a synthesized
// HTTP request against the existing handlers (see mcp_tools.go) and returns
// their plain-text body verbatim - the same grepable text the HTTP API serves.
//
// Authentication is a crmkit bearer token (minted via the OAuth flow in
// handlers_oauth.go, or any ck_ token). An unauthenticated request gets a 401
// with a WWW-Authenticate header pointing at the protected-resource metadata,
// which bootstraps a client's OAuth discovery.

// mcpProtocolVersion is the default MCP protocol version crmkit reports when a
// client does not request a specific one. When the client does, crmkit echoes
// the requested version (maximizing compatibility).
const mcpProtocolVersion = "2025-06-18"

// JSON-RPC 2.0 error codes used here.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// handleMCPGet answers GET /mcp. crmkit's transport is request/response only
// (no server-initiated SSE stream), so it declines the optional GET stream.
func (s *Server) handleMCPGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	http.Error(w, "the crmkit MCP endpoint is POST-only (no server-initiated stream)", http.StatusMethodNotAllowed)
}

// handleMCP is the Streamable HTTP MCP endpoint. It authenticates the bearer
// token once, then dispatches one or a batch of JSON-RPC messages. The resolved
// session is threaded into tool dispatch so the internal handlers do not
// re-resolve the token.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		// No credentials at all: a bare challenge (no error code) is the correct
		// bootstrap signal per RFC 6750, distinct from a supplied-but-bad token.
		s.mcpUnauthorized(w, "", "Authenticate with a bearer token (connect via OAuth).")
		return
	}
	sess, err := s.store.ResolveToken(auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrTokenExpired) {
		s.mcpUnauthorized(w, "invalid_token", "Token is unknown, revoked, or expired. Re-authenticate.")
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, rpcResponse{JSONRPC: "2.0",
			Error: &rpcError{Code: rpcInternalError, Message: "server error"}})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0",
			Error: &rpcError{Code: rpcParseError, Message: "could not read request body"}})
		return
	}
	trimmed := bytes.TrimSpace(body)

	// A JSON-RPC batch (array) is processed element-by-element; a single object
	// is the common case for MCP clients.
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var reqs []rpcRequest
		if err := json.Unmarshal(trimmed, &reqs); err != nil {
			writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0",
				Error: &rpcError{Code: rpcParseError, Message: "invalid JSON-RPC batch"}})
			return
		}
		var out []rpcResponse
		for i := range reqs {
			if resp := s.dispatchRPC(r, sess, &reqs[i]); resp != nil {
				out = append(out, *resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0",
			Error: &rpcError{Code: rpcParseError, Message: "invalid JSON-RPC request"}})
		return
	}
	resp := s.dispatchRPC(r, sess, &req)
	if resp == nil {
		// A notification (no id) expects no response body.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, *resp)
}

// mcpUnauthorized writes the 401 that bootstraps OAuth discovery: the
// WWW-Authenticate header points the client at the protected-resource metadata.
// errCode is the OAuth error (e.g. "invalid_token") or "" for a credential-less
// request, which gets a bare challenge per RFC 6750.
func (s *Server) mcpUnauthorized(w http.ResponseWriter, errCode, desc string) {
	challenge := `Bearer resource_metadata="` + s.protectedResourceMetadataURL() + `"`
	if errCode != "" {
		challenge = `Bearer error="` + errCode + `", resource_metadata="` + s.protectedResourceMetadataURL() + `"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	msg := errCode
	if msg == "" {
		msg = "auth_required"
	}
	writeJSON(w, http.StatusUnauthorized, rpcResponse{JSONRPC: "2.0",
		Error: &rpcError{Code: rpcInvalidRequest, Message: msg, Data: desc}})
}

// dispatchRPC handles one JSON-RPC message. It returns nil for notifications
// (requests without an id), which produce no response.
func (s *Server) dispatchRPC(r *http.Request, sess protocol.Session, req *rpcRequest) *rpcResponse {
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	reply := func(result any, rpcErr *rpcError) *rpcResponse {
		if isNotification {
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
	}

	switch req.Method {
	case "initialize":
		return reply(s.mcpInitialize(req.Params), nil)
	case "notifications/initialized", "notifications/cancelled", "notifications/progress":
		return nil
	case "ping":
		return reply(map[string]any{}, nil)
	case "tools/list":
		return reply(map[string]any{"tools": mcpTools()}, nil)
	case "tools/call":
		result, rpcErr := s.mcpToolsCall(r, sess, req.Params)
		return reply(result, rpcErr)
	default:
		return reply(nil, &rpcError{Code: rpcMethodNotFound, Message: "method not found: " + req.Method})
	}
}

// mcpInitialize returns the MCP initialize result: protocol version,
// capabilities, and server info. It echoes the client's requested protocol
// version when present.
func (s *Server) mcpInitialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	pv := p.ProtocolVersion
	if pv == "" {
		pv = mcpProtocolVersion
	}
	return map[string]any{
		"protocolVersion": pv,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "crmkit", "version": version.Version},
		"instructions": "crmkit is an agent-first CRM. It exposes a single tool, `request`, that calls the " +
			"CRM's plain-text HTTP API (method + path + optional body) - read that tool's description, and " +
			"call `request` with GET /help for the full manual. Results are plain text; records are addressed " +
			"by a handle like contact/c_ab12.",
	}
}

// mcpToolsCall executes a tools/call request: it looks up the named tool, builds
// the synthesized HTTP request, replays it against the internal handlers with
// the already-resolved session, and returns the plain-text body as MCP content.
func (s *Server) mcpToolsCall(r *http.Request, sess protocol.Session, params json.RawMessage) (any, *rpcError) {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{Code: rpcInvalidParams, Message: "invalid tools/call params"}
	}
	tool, ok := lookupTool(call.Name)
	if !ok {
		return nil, &rpcError{Code: rpcInvalidParams, Message: "unknown tool: " + call.Name}
	}
	method, path, reqBody, err := tool.build(call.Arguments)
	if err != nil {
		// A bad-argument error is reported as a tool error (isError), not a
		// protocol error, so the model can read the message and retry.
		return mcpTextResult(err.Error(), true), nil
	}

	text, status := s.dispatchInternal(r.Context(), method, path, sess, reqBody)
	return mcpTextResult(text, status >= 400), nil
}

// dispatchInternal replays a synthesized request against the internal route
// table and returns the (plain-text) response body and status. The caller's
// already-resolved session is injected into the request context so the internal
// handlers run with the same identity without re-resolving the token (authed
// short-circuits when a session is present). Accept is text/plain so the body is
// the grepable text form.
func (s *Server) dispatchInternal(ctx context.Context, method, path string, sess protocol.Session, body []byte) (string, int) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req = req.WithContext(context.WithValue(ctx, sessionKey, sess))
	req.Header.Set("Accept", "text/plain")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.internalHandler().ServeHTTP(rec, req)
	return rec.Body.String(), rec.Code
}

// mcpTextResult builds an MCP tools/call result with a single text content
// block. isError marks tool-level failures (e.g. validation or a 4xx body).
func mcpTextResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}
