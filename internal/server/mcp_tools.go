package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// The MCP tool surface is a single generic tool, "request", that calls crmkit's
// plain-text HTTP API directly (method + path + optional body). This mirrors
// crmkit's design - an agent reads the manual and drives the HTTP API - instead
// of re-encoding every endpoint as a typed MCP tool that would duplicate the API
// and drift from it. The tool's description is a compressed operating brief
// (always loaded by the client), and GET /help returns the full manual. handlers
// _mcp.go replays the (method, path, body) against the internal route table and
// returns the plain-text body verbatim.

// mcpTool is one entry in the MCP tool catalogue.
type mcpTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	// build maps tool arguments to an internal HTTP request. A returned error is
	// surfaced to the model as a tool error (isError), not a protocol fault.
	build func(args map[string]any) (method, path string, body []byte, err error)
}

// allowedPrefixes is the CRM surface the generic request tool may reach. Using
// an allowlist (rather than a blocklist) means the auth plane (/auth/*),
// the OAuth AS (/oauth/*), the MCP endpoint itself, and the well-known/health
// routes are all unreachable by construction - the model cannot make the server
// email OTPs or re-enter the authorization plane. Everything stays scoped to the
// calling token's workspace.
var allowedPrefixes = []string{
	"/whoami",
	"/search",
	"/contacts",
	"/companies",
	"/deals",
	"/tasks",
	"/campaigns",
	"/activities",
	"/reminders",
	"/audit",
	"/workspaces",
	"/tokens",
	"/help",
}

// requestDescription is the compressed operating manual carried in the tool's
// description. It is always loaded by the client, so it orients the model before
// the first call; GET /help returns the full manual on demand.
const requestDescription = `Operate the crmkit CRM through its plain-text HTTP API. You are already authenticated for one workspace.
Responses are grepable text (one labeled line per record) unless you add ?format=json; records are addressed by a handle like contact/c_ab12 - use the bare id (c_ab12) in paths.

Core:
  GET  /whoami                     identity, plan, usage
  GET  /search?q=acme              find anything across contacts, companies & deals (grouped)
  GET  /contacts|/companies|/deals list/query (see filters below)
  POST /contacts|/companies|/deals create; POST upserts contacts by email, companies by domain
  GET|PATCH|DELETE /contacts/{id}  fetch / update / delete one (same for companies, deals)
  POST /contacts/{id}/activities   log a call|email|meeting|note|task  {"kind":...,"body":...}
  GET|POST /tasks                  list / create follow-up work  {"title":..,"due_at":..,"contact_id":..}; PATCH /tasks/{id} {"done":true} to complete
  GET  /reminders                  open tasks due now/overdue (?days=N looks ahead)

Campaigns (a prospecting effort - a brief plus the contacts/companies gathered under it):
  GET|POST /campaigns              list / create  {"name":...,"description":"what you're collecting & why"}
  GET|PATCH|DELETE /campaigns/{id} fetch (shows member counts) / update status / delete
  GET  /campaigns/{id}/members     contacts & companies in the campaign
  POST /campaigns/{id}/members     attach one {"kind":"contact","id":"contact_..","reason":".."}; idempotent, so re-finding the same contact is free
  DELETE /campaigns/{id}/members/{kind}/{id}   detach one
Shortcut: POST /contacts?campaign=campaign_..&reason=.. (and /companies) creates/upserts AND attaches in one call.
Workflow: open a campaign, then upsert contacts/companies straight into it via ?campaign=. Re-check GET /campaigns/{id} for progress; the same entity can be in several campaigns.

List filters: ?field=value or ?field=op:value (ops: eq ne gt gte lt lte like in is not), plus &search= &sort=-field &limit= &cursor= .
Lists end with "# total: N" (rows matching your filters, across all pages) and, when more remain, "# next: <cursor>" to pass as &cursor=. Use total to decide whether to keep paging or narrow your filters.
DELETE is two-step: the first call returns a confirm token to resend as ?confirm=. Errors are instructive - they tell you what to do next.

Fetch the full manual any time: request GET /help`

// toolCatalogue is the (single-entry) MCP tool catalogue, built once.
var toolCatalogue = []mcpTool{
	{
		Name:        "request",
		Description: requestDescription,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"method", "path"},
			"properties": map[string]any{
				"method": map[string]any{
					"type":        "string",
					"enum":        []string{"GET", "POST", "PATCH", "DELETE"},
					"description": "HTTP method.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "API path beginning with '/', including any query string (e.g. /contacts?stage=lead&limit=10, or /contacts/c_ab12).",
				},
				"body": map[string]any{
					"type":        "object",
					"description": "JSON request body for POST/PATCH; omit for GET/DELETE.",
				},
			},
		},
		build: buildRequest,
	},
}

// toolIndex maps tool name -> tool for O(1) lookup.
var toolIndex = func() map[string]mcpTool {
	m := make(map[string]mcpTool, len(toolCatalogue))
	for _, t := range toolCatalogue {
		m[t.Name] = t
	}
	return m
}()

// mcpTools returns the tools/list payload: name, description, inputSchema.
func mcpTools() []map[string]any {
	out := make([]map[string]any, 0, len(toolCatalogue))
	for _, t := range toolCatalogue {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	return out
}

// lookupTool finds a tool by name.
func lookupTool(name string) (mcpTool, bool) {
	t, ok := toolIndex[name]
	return t, ok
}

// buildRequest validates the request tool's arguments and turns them into an
// internal (method, path, body) call. Validation failures are returned as
// errors, which the caller surfaces as a tool error the model can read and fix.
func buildRequest(args map[string]any) (string, string, []byte, error) {
	method := strings.ToUpper(strings.TrimSpace(asString(args["method"])))
	switch method {
	case "GET", "POST", "PATCH", "DELETE":
	default:
		return "", "", nil, fmt.Errorf("method must be one of GET, POST, PATCH, DELETE")
	}

	path := strings.TrimSpace(asString(args["path"]))
	if path == "" || !strings.HasPrefix(path, "/") {
		return "", "", nil, fmt.Errorf("path must begin with '/'")
	}
	if _, err := url.ParseRequestURI(path); err != nil {
		return "", "", nil, fmt.Errorf("invalid path %q", path)
	}
	if !pathAllowed(path) {
		return "", "", nil, fmt.Errorf("path %q is not permitted via MCP; allowed prefixes: %s",
			pathOnly(path), strings.Join(allowedPrefixes, " "))
	}

	var body []byte
	if b, ok := args["body"].(map[string]any); ok && len(b) > 0 {
		raw, err := json.Marshal(b)
		if err != nil {
			return "", "", nil, fmt.Errorf("invalid body: %v", err)
		}
		body = raw
	}
	return method, path, body, nil
}

// pathAllowed reports whether path (its route portion, ignoring any query) falls
// under an allowed CRM prefix.
func pathAllowed(path string) bool {
	p := pathOnly(path)
	for _, prefix := range allowedPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// pathOnly returns the path without its query string.
func pathOnly(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}

// asString coerces an argument to a string ("" if it is not one).
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
