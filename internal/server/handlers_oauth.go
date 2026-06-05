package server

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crmkit/crmkit/internal/auth"
	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/store"
)

// This file implements crmkit's OAuth 2.1 authorization server, exposed so that
// standard MCP clients (ChatGPT, Claude) can connect to /mcp as a one-click
// connector. It is deliberately minimal: public clients only (PKCE S256, no
// client secrets), the authorization-code grant only, and the access token it
// issues is a normal crmkit bearer token (so /mcp reuses the same auth). The
// authorize page drives crmkit's existing email-OTP login.
//
// Endpoints:
//
//	GET  /.well-known/oauth-protected-resource   (RFC 9728)
//	GET  /.well-known/oauth-authorization-server (RFC 8414)
//	POST /oauth/register                         (RFC 7591 dynamic client registration)
//	GET  /oauth/authorize                        (the login page)
//	POST /oauth/authorize                        (email-OTP steps -> redirect with code)
//	POST /oauth/token                            (code -> bearer token; PKCE verified)
//	POST /oauth/revoke                           (RFC 7009)

// oauthScopes is the single advertised scope. crmkit does not enforce scopes
// (a token grants its workspace's full CRM); it is advertised because some
// clients expect at least one.
var oauthScopes = []string{"crm"}

// writeJSON writes v as a no-store JSON response. Used by the OAuth/MCP
// endpoints, which always speak JSON regardless of Accept.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthError writes an RFC 6749 error response ({error, error_description}).
func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// resourceURL is the protected resource identifier (the MCP endpoint).
func (s *Server) resourceURL() string { return s.cfg.Server.BaseURL + "/mcp" }

// protectedResourceMetadataURL is the RFC 9728 document URL advertised in the
// 401 WWW-Authenticate header from /mcp.
func (s *Server) protectedResourceMetadataURL() string {
	return s.cfg.Server.BaseURL + "/.well-known/oauth-protected-resource"
}

// handleOAuthProtectedResource serves RFC 9728 Protected Resource Metadata: it
// tells a client which authorization server protects /mcp.
func (s *Server) handleOAuthProtectedResource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.resourceURL(),
		"authorization_servers":    []string{s.cfg.Server.BaseURL},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         oauthScopes,
	})
}

// handleOAuthAuthorizationServer serves RFC 8414 Authorization Server Metadata:
// the endpoints and capabilities of crmkit's OAuth AS. Public, unauthenticated.
func (s *Server) handleOAuthAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	base := s.cfg.Server.BaseURL
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      oauthScopes,
	})
}

// dcrRequest is the subset of an RFC 7591 dynamic client registration request
// crmkit reads. Only redirect_uris and client_name are used; other fields are
// accepted for compatibility but ignored (crmkit is fixed to public + PKCE).
type dcrRequest struct {
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name"`
}

// handleOAuthRegister implements RFC 7591 dynamic client registration. MCP
// clients call this to register themselves before the authorization flow. It is
// public but each redirect_uri must pass the configured allowlist
// (mcp.allowed_redirect_uris, default "*").
func (s *Server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	// RFC 7591 clients send many metadata fields (grant_types, response_types,
	// token_endpoint_auth_method, scope, client_uri, contacts, ...). crmkit uses
	// only redirect_uris + client_name and ignores the rest, so decode leniently
	// (json.Unmarshal, NOT the strict decodeJSON which rejects unknown fields).
	var body dcrRequest
	raw, err := readBody(r)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "Could not read the request body.")
		return
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "Request body must be JSON.")
			return
		}
	}
	if len(body.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "At least one redirect_uri is required.")
		return
	}
	for _, u := range body.RedirectURIs {
		if !s.cfg.MCP.RedirectURIAllowed(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"redirect_uri "+u+" is not permitted by this server's allowlist.")
			return
		}
	}

	clientID, err := s.store.RegisterOAuthClient(body.RedirectURIs, strings.TrimSpace(body.ClientName))
	if err != nil {
		s.log.Error("oauth client registration failed", slog.String("error", err.Error()))
		oauthError(w, http.StatusInternalServerError, "server_error", "Could not register the client.")
		return
	}

	base := s.cfg.Server.BaseURL
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"redirect_uris":              body.RedirectURIs,
		"client_name":                body.ClientName,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"authorization_endpoint":     base + "/oauth/authorize",
		"token_endpoint":             base + "/oauth/token",
	})
}

// authorizePage is the data rendered by the /oauth/authorize login page. All
// fields are echoed into the page via html/template (auto-escaped).
type authorizePage struct {
	ClientName          string
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	State               string
	Scope               string
	Email               string
	Step                string // "email" | "code" | "workspace" | "error"
	Message             string
	Error               string
	// Workspace-selection step: a signed ticket proving the OTP was verified, and
	// the workspaces the user may connect (shown only when they belong to >1).
	Ticket     string
	Workspaces []wsOption
}

// wsOption is one selectable workspace on the authorize page.
type wsOption struct {
	ID   string
	Name string
	Role string
}

// validateAuthorizeParams reads and validates the OAuth authorize parameters
// from get (query for GET, form for POST). On success it returns a populated
// page and an empty error; otherwise the error describes what was rejected. A
// rejection here must NOT redirect (the redirect_uri may itself be the problem).
func (s *Server) validateAuthorizeParams(get func(string) string) (authorizePage, string) {
	p := authorizePage{
		ResponseType:        strings.TrimSpace(get("response_type")),
		ClientID:            strings.TrimSpace(get("client_id")),
		RedirectURI:         strings.TrimSpace(get("redirect_uri")),
		CodeChallenge:       strings.TrimSpace(get("code_challenge")),
		CodeChallengeMethod: strings.TrimSpace(get("code_challenge_method")),
		State:               get("state"),
		Scope:               strings.TrimSpace(get("scope")),
	}
	if p.CodeChallengeMethod == "" {
		p.CodeChallengeMethod = "S256"
	}
	if p.ResponseType != "code" {
		return p, `response_type must be "code".`
	}
	if p.ClientID == "" {
		return p, "client_id is required."
	}
	client, err := s.store.GetOAuthClient(p.ClientID)
	if err != nil {
		return p, "Unknown client_id. Register first via POST /oauth/register."
	}
	p.ClientName = client.Name
	if p.RedirectURI == "" || !containsString(client.RedirectURIs, p.RedirectURI) {
		return p, "redirect_uri does not match a registered URI for this client."
	}
	if p.CodeChallenge == "" {
		return p, "code_challenge (PKCE) is required."
	}
	if p.CodeChallengeMethod != "S256" {
		return p, "code_challenge_method must be S256."
	}
	return p, ""
}

// handleOAuthAuthorizeGet renders the login page that starts the authorization
// flow (the user pastes the email-OTP code, then is redirected back).
func (s *Server) handleOAuthAuthorizeGet(w http.ResponseWriter, r *http.Request) {
	p, errDesc := s.validateAuthorizeParams(r.URL.Query().Get)
	if errDesc != "" {
		s.renderAuthorize(w, http.StatusBadRequest, authorizePage{Step: "error", Error: errDesc})
		return
	}
	p.Step = "email"
	s.renderAuthorize(w, http.StatusOK, p)
}

// handleOAuthAuthorizePost advances the email-OTP login embedded in the
// authorize page. The stages are: (1) email only -> email a code; (2) email +
// code -> verify; if the user belongs to one workspace the grant is established
// and we redirect, otherwise (3) a signed ticket carries the verified identity
// to a workspace-selection step whose submit establishes the grant.
func (s *Server) handleOAuthAuthorizePost(w http.ResponseWriter, r *http.Request) {
	// Re-validate the OAuth params from the form - never trust echoed hidden
	// fields blindly (the redirect_uri in particular gates where the code goes).
	p, errDesc := s.validateAuthorizeParams(r.PostFormValue)
	if errDesc != "" {
		s.renderAuthorize(w, http.StatusBadRequest, authorizePage{Step: "error", Error: errDesc})
		return
	}

	// Stage 3: a workspace was chosen. The login ticket proves the OTP was
	// verified in a prior request (the code is single-use and already consumed).
	if ticket := strings.TrimSpace(r.PostFormValue("login_ticket")); ticket != "" {
		userID, email, ok := auth.VerifyLoginTicket(s.cfg.Server.SecretKey, ticket)
		if !ok {
			s.renderAuthorize(w, http.StatusBadRequest, authorizePage{Step: "error",
				Error: "Your sign-in step expired. Close this and reconnect to start over."})
			return
		}
		workspaceID := strings.TrimSpace(r.PostFormValue("workspace_id"))
		// Authorize the choice: the user must actually be a member of it.
		if _, err := s.store.MemberRole(workspaceID, userID); err != nil {
			workspaces, _ := s.store.ListWorkspacesForUser(userID)
			s.renderWorkspacePicker(w, p, userID, email, "Choose a workspace you belong to.", workspaces)
			return
		}
		s.finishAuthorize(w, r, p, userID, workspaceID)
		return
	}

	email := auth.NormalizeEmail(r.PostFormValue("email"))
	code := strings.TrimSpace(r.PostFormValue("code"))
	p.Email = email

	if email == "" || !strings.Contains(email, "@") {
		p.Step = "email"
		p.Error = "Enter a valid email address."
		s.renderAuthorize(w, http.StatusOK, p)
		return
	}

	// Stage 1: no code yet -> send a login code to the email.
	if code == "" {
		if !s.authLimiter.Allow("req:" + email) {
			p.Step = "email"
			p.Error = "Too many requests for this email. Wait a moment and try again."
			s.renderAuthorize(w, http.StatusTooManyRequests, p)
			return
		}
		otp := auth.GenerateCode()
		expires := time.Now().Add(time.Duration(s.cfg.Server.OTPTTLSeconds) * time.Second)
		if err := s.store.PutOTP(email, auth.HashCode(s.cfg.Server.SecretKey, email, otp), expires); err != nil {
			p.Step = "email"
			p.Error = "Could not start login. Try again."
			s.renderAuthorize(w, http.StatusInternalServerError, p)
			return
		}
		if s.cfg.Server.Local {
			s.log.Info("login code (local)", slog.String("email", email), slog.String("code", otp))
		}
		if err := s.mailer.Send(auth.LoginEmail(email, otp, s.cfg.Server.OTPTTLSeconds/60)); err != nil {
			s.log.Error("authorize login email send failed", slog.String("email", email), slog.String("error", err.Error()))
			p.Step = "email"
			p.Error = "Could not send the login email. Try again."
			s.renderAuthorize(w, http.StatusInternalServerError, p)
			return
		}
		p.Step = "code"
		p.Message = "We emailed a 6-digit code to " + email + ". Enter it below."
		s.renderAuthorize(w, http.StatusOK, p)
		return
	}

	// Stage 2: verify the code.
	if !s.authLimiter.Allow("verify:" + email) {
		p.Step = "code"
		p.Error = "Too many attempts. Wait a moment and try again."
		s.renderAuthorize(w, http.StatusTooManyRequests, p)
		return
	}
	ok, err := s.store.VerifyOTP(email, auth.HashCode(s.cfg.Server.SecretKey, email, code), time.Now())
	if err != nil {
		p.Step = "code"
		p.Error = "Something went wrong. Try again."
		s.renderAuthorize(w, http.StatusInternalServerError, p)
		return
	}
	if !ok {
		p.Step = "code"
		p.Error = "That code is wrong or expired. Check the email or start over."
		s.renderAuthorize(w, http.StatusUnauthorized, p)
		return
	}

	user, err := s.store.GetOrCreateIdentity(email)
	if err != nil {
		p.Step = "code"
		p.Error = "Could not provision your workspace. Try again."
		s.renderAuthorize(w, http.StatusInternalServerError, p)
		return
	}

	// One workspace: nothing to choose, establish the grant straight away in the
	// workspace the user actually belongs to (their stored default may be stale -
	// e.g. they were removed from it - so use the membership, not DefaultWorkspaceID).
	// Several: let the user pick which one this connection operates in.
	workspaces, _ := s.store.ListWorkspacesForUser(user.ID)
	if len(workspaces) <= 1 {
		workspaceID := user.DefaultWorkspaceID
		if len(workspaces) == 1 {
			workspaceID = workspaces[0].ID
		}
		s.finishAuthorize(w, r, p, user.ID, workspaceID)
		return
	}
	s.renderWorkspacePicker(w, p, user.ID, email, "", workspaces)
}

// renderWorkspacePicker shows the workspace-selection step, carrying a signed
// login ticket (so the next submit need not re-verify the OTP) and the list of
// workspaces the user may connect. The caller passes the already-fetched
// workspace list to avoid a duplicate query.
func (s *Server) renderWorkspacePicker(w http.ResponseWriter, p authorizePage, userID, email, errMsg string, workspaces []protocol.Workspace) {
	opts := make([]wsOption, 0, len(workspaces))
	for _, ws := range workspaces {
		opts = append(opts, wsOption{ID: ws.ID, Name: ws.Name, Role: ws.Role})
	}
	p.Step = "workspace"
	p.Email = email
	p.Error = errMsg
	p.Ticket = auth.NewLoginTicket(s.cfg.Server.SecretKey, userID, email)
	p.Workspaces = opts
	s.renderAuthorize(w, http.StatusOK, p)
}

// finishAuthorize mints the authorization code for the chosen workspace and
// redirects back to the client's redirect_uri. The workspace is now pinned: it
// flows through the code, the access token, and every refresh.
func (s *Server) finishAuthorize(w http.ResponseWriter, r *http.Request, p authorizePage, userID, workspaceID string) {
	plaintext, hash := auth.GenerateOAuthCode()
	if plaintext == "" {
		s.renderAuthorize(w, http.StatusInternalServerError, authorizePage{Step: "error",
			Error: "Could not issue an authorization code. Try again."})
		return
	}
	grant := store.AuthCode{
		ClientID:      p.ClientID,
		UserID:        userID,
		WorkspaceID:   workspaceID,
		RedirectURI:   p.RedirectURI,
		CodeChallenge: p.CodeChallenge,
		Scope:         p.Scope,
	}
	expires := time.Now().Add(time.Duration(s.cfg.MCP.AuthCodeTTLSeconds) * time.Second)
	if err := s.store.PutAuthCode(hash, grant, expires); err != nil {
		s.renderAuthorize(w, http.StatusInternalServerError, authorizePage{Step: "error",
			Error: "Could not issue an authorization code. Try again."})
		return
	}

	dest, err := url.Parse(p.RedirectURI)
	if err != nil {
		s.renderAuthorize(w, http.StatusBadRequest, authorizePage{Step: "error", Error: "Invalid redirect_uri."})
		return
	}
	q := dest.Query()
	q.Set("code", plaintext)
	if p.State != "" {
		q.Set("state", p.State)
	}
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// handleOAuthToken is the token endpoint. It supports the authorization_code
// grant (with PKCE) and the refresh_token grant (rotating the refresh token).
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "Could not parse form body.")
		return
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		s.tokenFromAuthCode(w, r)
	case "refresh_token":
		s.tokenFromRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
			`Supported grants: "authorization_code" and "refresh_token".`)
	}
}

// tokenFromAuthCode exchanges an authorization code (PKCE-verified) for a token
// pair.
func (s *Server) tokenFromAuthCode(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.PostFormValue("code"))
	clientID := strings.TrimSpace(r.PostFormValue("client_id"))
	redirectURI := strings.TrimSpace(r.PostFormValue("redirect_uri"))
	verifier := r.PostFormValue("code_verifier")
	if code == "" || clientID == "" || verifier == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request",
			"code, client_id and code_verifier are required.")
		return
	}

	grant, err := s.store.ConsumeAuthCode(auth.HashToken(code), time.Now())
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "Authorization code is invalid or expired.")
		return
	}
	if grant.ClientID != clientID || grant.RedirectURI != redirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client_id or redirect_uri does not match the code.")
		return
	}
	if !auth.VerifyPKCE(verifier, grant.CodeChallenge, "S256") {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed.")
		return
	}

	s.issueTokens(w, store.RefreshGrant{
		ClientID:    clientID,
		UserID:      grant.UserID,
		WorkspaceID: grant.WorkspaceID,
		Scope:       grant.Scope,
	})
}

// tokenFromRefresh exchanges a refresh token for a new token pair, rotating the
// refresh token (the presented one is consumed and a fresh one is issued).
func (s *Server) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	refresh := strings.TrimSpace(r.PostFormValue("refresh_token"))
	clientID := strings.TrimSpace(r.PostFormValue("client_id"))
	if refresh == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required.")
		return
	}
	grant, err := s.store.ConsumeRefreshToken(auth.HashToken(refresh), time.Now())
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "Refresh token is invalid or expired. Sign in again.")
		return
	}
	// A public client should present its client_id; if it does, it must match.
	if clientID != "" && clientID != grant.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client_id does not match the refresh token.")
		return
	}
	// Rotation supersedes the access token issued in the prior cycle: revoke it so
	// a leaked older access token cannot outlive the refresh it was paired with.
	if grant.AccessTokenHash != "" {
		if err := s.store.RevokeTokenByHash(grant.AccessTokenHash); err != nil {
			s.log.Warn("revoke superseded access token failed", slog.String("error", err.Error()))
		}
	}
	s.issueTokens(w, grant)
}

// issueTokens mints an access token (a normal crmkit bearer token) plus a
// rotating refresh token for the given identity, and writes the token response.
// The access token is named after the OAuth client so the user can recognize
// and revoke it in GET /tokens.
func (s *Server) issueTokens(w http.ResponseWriter, g store.RefreshGrant) {
	tokenName := "mcp"
	if client, err := s.store.GetOAuthClient(g.ClientID); err == nil && strings.TrimSpace(client.Name) != "" {
		tokenName = client.Name
	}

	accessPlain, accessHash := auth.GenerateToken()
	if accessPlain == "" {
		oauthError(w, http.StatusInternalServerError, "server_error", "Could not mint a token.")
		return
	}
	tokenID, err := s.store.CreateToken(g.UserID, g.WorkspaceID, tokenName, accessHash)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "Could not store the token.")
		return
	}

	refreshPlain, refreshHash := auth.GenerateRefreshToken()
	if refreshPlain == "" {
		oauthError(w, http.StatusInternalServerError, "server_error", "Could not mint a refresh token.")
		return
	}
	// Pair the refresh token with this access token so the next rotation can
	// revoke it.
	g.AccessTokenHash = accessHash
	refreshExpires := time.Now().Add(time.Duration(s.cfg.MCP.RefreshTTLSeconds) * time.Second)
	if err := s.store.PutRefreshToken(refreshHash, g, refreshExpires); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "Could not store the refresh token.")
		return
	}
	s.audit(sessionForUser(g.UserID, g.WorkspaceID, tokenID), "oauth.token", "token/"+tokenID, "mcp:"+g.ClientID)

	// No expires_in: the access token has no fixed lifetime (sliding idle window,
	// renewed on each use), so advertising an absolute deadline would mislead
	// clients into refresh-churn. Clients refresh reactively on a 401 instead.
	resp := map[string]any{
		"access_token":  accessPlain,
		"token_type":    "Bearer",
		"refresh_token": refreshPlain,
	}
	if g.Scope != "" {
		resp["scope"] = g.Scope
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleOAuthRevoke implements RFC 7009 token revocation. The token may be an
// access token or a refresh token; both are tried. It always returns 200, even
// for an unknown token, per the spec.
func (s *Server) handleOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "Could not parse form body.")
		return
	}
	if token := strings.TrimSpace(r.PostFormValue("token")); token != "" {
		hash := auth.HashToken(token)
		if err := s.store.RevokeTokenByHash(hash); err != nil {
			s.log.Warn("oauth revoke failed", slog.String("error", err.Error()))
		}
		if err := s.store.RevokeRefreshTokenByHash(hash); err != nil {
			s.log.Warn("oauth refresh revoke failed", slog.String("error", err.Error()))
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// renderAuthorize writes the authorize login page.
func (s *Server) renderAuthorize(w http.ResponseWriter, status int, p authorizePage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := authorizeTmpl.Execute(w, p); err != nil {
		s.log.Error("render authorize page failed", slog.String("error", err.Error()))
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// authorizeHTML is the login page served during the OAuth flow, embedded from
// authorize.html (edit that file as normal HTML - it is the single source of
// truth). It is an html/template: all dynamic values are auto-escaped.
//
//go:embed authorize.html
var authorizeHTML string

// authorizeTmpl is the parsed authorize page (incl. the "params" sub-template).
var authorizeTmpl = template.Must(template.New("authorize").Parse(authorizeHTML))
