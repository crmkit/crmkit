package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crmkit/crmkit/internal/auth"
	"github.com/crmkit/crmkit/internal/config"
)

// captureMailer records the last email so the test can read the OTP that the
// authorize page emails out.
type captureMailer struct{ last auth.Email }

func (m *captureMailer) Send(e auth.Email) error { m.last = e; return nil }

var sixDigits = regexp.MustCompile(`\d{6}`)

// newOAuthServer builds a server wired to a capturing mailer so the test can
// read emailed login codes during the authorize flow.
func newOAuthServer(t *testing.T) (*httptest.Server, *captureMailer) {
	t.Helper()
	st := newMigratedStore(t)
	cfg := config.Default()
	cfg.Server.BaseURL = "https://api.example.test"
	// These tests exercise the OAuth flow, not quotas; give the default plan
	// unlimited limits so e.g. creating a second workspace at the picker isn't
	// blocked by the basic plan's max_workspaces=1.
	if pl, ok := cfg.Plans.Catalogue[cfg.Plans.Default]; ok {
		pl.MaxWorkspaces, pl.MaxMembers, pl.MaxContacts, pl.MaxCompanies, pl.MaxDeals = limitPtr(-1), limitPtr(-1), limitPtr(-1), limitPtr(-1), limitPtr(-1)
		cfg.Plans.Catalogue[cfg.Plans.Default] = pl
	}
	srv := New(cfg, st, memoryRL(t))
	mailer := &captureMailer{}
	srv.mailer = mailer
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, mailer
}

func pkcePair() (verifier, challenge string) {
	verifier = "this_is_a_sufficiently_long_pkce_code_verifier_0123456789"
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func postForm(t *testing.T, client *http.Client, rawurl string, form url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", rawurl, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post form %s: %v", rawurl, err)
	}
	return resp
}

func TestWellKnownMetadata(t *testing.T) {
	ts, _ := newOAuthServer(t)

	status, body := do(t, ts, "GET", "/.well-known/oauth-protected-resource", "", "")
	if status != 200 {
		t.Fatalf("PRM status %d", status)
	}
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal([]byte(body), &prm); err != nil {
		t.Fatalf("PRM not json: %v (%s)", err, body)
	}
	if prm.Resource != "https://api.example.test/mcp" || len(prm.AuthorizationServers) != 1 {
		t.Fatalf("unexpected PRM: %+v", prm)
	}

	status, body = do(t, ts, "GET", "/.well-known/oauth-authorization-server", "", "")
	if status != 200 {
		t.Fatalf("AS status %d", status)
	}
	var as struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	}
	if err := json.Unmarshal([]byte(body), &as); err != nil {
		t.Fatalf("AS not json: %v", err)
	}
	if as.RegistrationEndpoint == "" || len(as.CodeChallengeMethods) == 0 || as.CodeChallengeMethods[0] != "S256" {
		t.Fatalf("unexpected AS metadata: %+v", as)
	}
}

func TestMCPUnauthenticatedBootstrapsOAuth(t *testing.T) {
	ts, _ := newOAuthServer(t)
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	wa := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(wa, "resource_metadata=") || !strings.Contains(wa, "/.well-known/oauth-protected-resource") {
		t.Fatalf("missing/!pointing WWW-Authenticate: %q", wa)
	}
}

// TestOAuthToMCPEndToEnd drives the whole connector flow: dynamic client
// registration -> authorize (email-OTP) -> token -> MCP initialize/tools.
func TestOAuthToMCPEndToEnd(t *testing.T) {
	ts, mailer := newOAuthServer(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	redirectURI := "https://client.example/callback"

	// 1) Dynamic client registration.
	status, body := do(t, ts, "POST", "/oauth/register", "",
		`{"redirect_uris":["`+redirectURI+`"],"client_name":"Test Client"}`)
	if status != http.StatusCreated {
		t.Fatalf("register: %d %q", status, body)
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal([]byte(body), &reg); err != nil || reg.ClientID == "" {
		t.Fatalf("register response: %v (%s)", err, body)
	}

	verifier, challenge := pkcePair()
	common := url.Values{
		"response_type":         {"code"},
		"client_id":             {reg.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"xyz123"},
		"scope":                 {"crm"},
	}

	// 2) Authorize page renders the email form.
	authzURL := ts.URL + "/oauth/authorize?" + common.Encode()
	gresp, err := http.Get(authzURL)
	if err != nil {
		t.Fatalf("authorize get: %v", err)
	}
	gbody, _ := io.ReadAll(gresp.Body)
	gresp.Body.Close()
	if gresp.StatusCode != 200 || !strings.Contains(string(gbody), "Email me a code") {
		t.Fatalf("authorize page: %d %q", gresp.StatusCode, gbody)
	}

	// 3) Submit email -> code is emailed.
	step1 := cloneValues(common)
	step1.Set("email", "owner@example.com")
	r1 := postForm(t, noRedirect, ts.URL+"/oauth/authorize", step1)
	b1, _ := io.ReadAll(r1.Body)
	r1.Body.Close()
	if r1.StatusCode != 200 || !strings.Contains(string(b1), "6-digit") {
		t.Fatalf("authorize step1: %d %q", r1.StatusCode, b1)
	}
	code := sixDigits.FindString(mailer.last.Text)
	if code == "" {
		t.Fatalf("no OTP captured from email: %q", mailer.last.Text)
	}

	// 4) Submit the code -> redirect back with an authorization code.
	step2 := cloneValues(common)
	step2.Set("email", "owner@example.com")
	step2.Set("code", code)
	r2 := postForm(t, noRedirect, ts.URL+"/oauth/authorize", step2)
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	// Single-workspace users now also see the picker; complete it.
	loc := completePicker(t, ts, noRedirect, common, "owner@example.com", b2)
	if loc.Query().Get("state") != "xyz123" {
		t.Fatalf("state not echoed: %q", loc.RawQuery)
	}
	authCode := loc.Query().Get("code")
	if authCode == "" {
		t.Fatalf("no auth code in redirect: %q", loc.RawQuery)
	}

	// 5) Exchange the code for an access token (PKCE verified).
	tok := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"redirect_uri":  {redirectURI},
		"client_id":     {reg.ClientID},
		"code_verifier": {verifier},
	}
	tr := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", tok)
	tbody, _ := io.ReadAll(tr.Body)
	tr.Body.Close()
	if tr.StatusCode != 200 {
		t.Fatalf("token: %d %q", tr.StatusCode, tbody)
	}
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(tbody, &tokenResp); err != nil || tokenResp.AccessToken == "" {
		t.Fatalf("token response: %v (%s)", err, tbody)
	}
	if !strings.HasPrefix(tokenResp.AccessToken, "ck_") || tokenResp.TokenType != "Bearer" {
		t.Fatalf("unexpected token: %+v", tokenResp)
	}
	if !strings.HasPrefix(tokenResp.RefreshToken, "ckr_") {
		t.Fatalf("expected a refresh token: %+v", tokenResp)
	}
	// No expires_in is advertised (sliding idle expiry, not a fixed lifetime).
	if tokenResp.ExpiresIn != 0 {
		t.Fatalf("did not expect expires_in, got %d", tokenResp.ExpiresIn)
	}
	access := tokenResp.AccessToken
	refresh := tokenResp.RefreshToken

	// A used authorization code cannot be replayed.
	tr2 := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", tok)
	tr2.Body.Close()
	if tr2.StatusCode == 200 {
		t.Fatal("authorization code should be single-use")
	}

	// 6) MCP initialize with the bearer token.
	status, body = do(t, ts, "POST", "/mcp", access, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if status != 200 || !strings.Contains(body, `"serverInfo"`) || !strings.Contains(body, "crmkit") {
		t.Fatalf("initialize: %d %q", status, body)
	}

	// 7) tools/list exposes the single generic request tool, whose description
	// points at GET /help for the full manual.
	status, body = do(t, ts, "POST", "/mcp", access, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if status != 200 || !strings.Contains(body, `"request"`) || !strings.Contains(body, "/help") {
		t.Fatalf("tools/list: %d %q", status, body)
	}

	// 8) request POST /contacts returns the plain-text record verbatim.
	call := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request","arguments":{"method":"POST","path":"/contacts","body":{"name":"Jane Doe","email":"jane@acme.com","stage":"lead"}}}}`
	status, body = do(t, ts, "POST", "/mcp", access, call)
	if status != 200 || !strings.Contains(body, "contact_") || !strings.Contains(body, "Jane Doe") {
		t.Fatalf("tools/call: %d %q", status, body)
	}
	if strings.Contains(body, `"isError":true`) {
		t.Fatalf("tools/call reported error: %q", body)
	}

	// 9) A notification (no id) yields no body.
	status, _ = do(t, ts, "POST", "/mcp", access, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if status != http.StatusAccepted {
		t.Fatalf("notification should be 202, got %d", status)
	}

	// 10) Refresh: exchange the refresh token for a fresh pair (rotation). Done
	// last because it revokes the access token used above.
	refreshForm := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {reg.ClientID},
	}
	rr := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", refreshForm)
	rbody, _ := io.ReadAll(rr.Body)
	rr.Body.Close()
	if rr.StatusCode != 200 {
		t.Fatalf("refresh: %d %q", rr.StatusCode, rbody)
	}
	var refreshed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rbody, &refreshed); err != nil || refreshed.AccessToken == "" || refreshed.RefreshToken == "" {
		t.Fatalf("refresh response: %v (%s)", err, rbody)
	}
	if refreshed.AccessToken == access || refreshed.RefreshToken == refresh {
		t.Fatal("refresh should rotate both tokens")
	}
	// The old refresh token is single-use (rotation revokes it).
	old := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", refreshForm)
	old.Body.Close()
	if old.StatusCode == 200 {
		t.Fatal("rotated refresh token should not be reusable")
	}
	// The freshly minted access token authenticates against /mcp.
	if status, _ := do(t, ts, "POST", "/mcp", refreshed.AccessToken, `{"jsonrpc":"2.0","id":9,"method":"ping"}`); status != 200 {
		t.Fatalf("refreshed access token should work on /mcp, got %d", status)
	}
	// Rotation revoked the superseded access token: the old one no longer works.
	if status, _ := do(t, ts, "POST", "/mcp", access, `{"jsonrpc":"2.0","id":10,"method":"ping"}`); status != http.StatusUnauthorized {
		t.Fatalf("superseded access token should be revoked after refresh, got %d", status)
	}
}

func TestOAuthRegisterRejectsDisallowedRedirect(t *testing.T) {
	st := newMigratedStore(t)
	cfg := config.Default()
	cfg.MCP.AllowedRedirectURIs = []string{"https://trusted.example/*"}
	srv := New(cfg, st, memoryRL(t))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	if status, _ := do(t, ts, "POST", "/oauth/register", "", `{"redirect_uris":["https://evil.example/cb"]}`); status != http.StatusBadRequest {
		t.Fatalf("disallowed redirect should be 400, got %d", status)
	}
	if status, _ := do(t, ts, "POST", "/oauth/register", "", `{"redirect_uris":["https://trusted.example/cb"]}`); status != http.StatusCreated {
		t.Fatalf("allowed redirect should be 201, got %d", status)
	}
}

var ticketRe = regexp.MustCompile(`name="login_ticket" value="([^"]+)"`)
var wsIDRe = regexp.MustCompile(`name="workspace_id"\s+value="([^"]+)"`)

// completePicker finishes the workspace-selection step (now shown to every user,
// single- or multi-workspace) by submitting the signed ticket and the first
// offered workspace, returning the redirect Location.
func completePicker(t *testing.T, ts *httptest.Server, client *http.Client, common url.Values, email string, pickerBody []byte) *url.URL {
	t.Helper()
	tm := ticketRe.FindSubmatch(pickerBody)
	wm := wsIDRe.FindSubmatch(pickerBody)
	if tm == nil || wm == nil {
		t.Fatalf("picker missing login_ticket/workspace_id:\n%s", pickerBody)
	}
	step := cloneValues(common)
	step.Set("email", email)
	step.Set("login_ticket", string(tm[1]))
	step.Set("workspace_id", string(wm[1]))
	r := postForm(t, client, ts.URL+"/oauth/authorize", step)
	r.Body.Close()
	if r.StatusCode != http.StatusFound {
		t.Fatalf("workspace step should redirect, got %d", r.StatusCode)
	}
	loc, err := url.Parse(r.Header.Get("Location"))
	if err != nil {
		t.Fatalf("redirect parse: %v", err)
	}
	return loc
}

// TestOAuthWorkspaceSelection verifies that a user belonging to more than one
// workspace is shown the picker after entering the code, and that the chosen
// workspace is what the issued token operates in.
func TestOAuthWorkspaceSelection(t *testing.T) {
	st := newMigratedStore(t)
	cfg := config.Default()
	cfg.Server.BaseURL = "https://api.example.test"
	srv := New(cfg, st, memoryRL(t))
	mailer := &captureMailer{}
	srv.mailer = mailer
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Pre-provision a user with a second workspace so the picker appears.
	user, err := st.GetOrCreateIdentity("multi@example.com")
	if err != nil {
		t.Fatalf("provision user: %v", err)
	}
	second, err := st.CreateWorkspace(user.ID, "Second Team")
	if err != nil {
		t.Fatalf("create second workspace: %v", err)
	}

	redirectURI := "https://client.example/cb"
	_, body := do(t, ts, "POST", "/oauth/register", "", `{"redirect_uris":["`+redirectURI+`"],"client_name":"C"}`)
	var reg struct {
		ClientID string `json:"client_id"`
	}
	json.Unmarshal([]byte(body), &reg)

	_, challenge := pkcePair()
	verifier, _ := pkcePair()
	common := url.Values{
		"response_type":         {"code"},
		"client_id":             {reg.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"s"},
		"scope":                 {"crm"},
	}

	// Email step.
	step1 := cloneValues(common)
	step1.Set("email", "multi@example.com")
	postForm(t, noRedirect, ts.URL+"/oauth/authorize", step1).Body.Close()
	code := sixDigits.FindString(mailer.last.Text)

	// Code step -> the workspace picker (not a redirect), listing both workspaces.
	step2 := cloneValues(common)
	step2.Set("email", "multi@example.com")
	step2.Set("code", code)
	r2 := postForm(t, noRedirect, ts.URL+"/oauth/authorize", step2)
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != 200 || !strings.Contains(string(b2), "Choose a workspace") || !strings.Contains(string(b2), "Second Team") {
		t.Fatalf("expected workspace picker: %d %q", r2.StatusCode, b2)
	}
	m := ticketRe.FindStringSubmatch(string(b2))
	if m == nil {
		t.Fatalf("no login_ticket in picker: %q", b2)
	}

	// Choose the second workspace.
	step3 := cloneValues(common)
	step3.Set("email", "multi@example.com")
	step3.Set("login_ticket", m[1])
	step3.Set("workspace_id", second.ID)
	r3 := postForm(t, noRedirect, ts.URL+"/oauth/authorize", step3)
	r3.Body.Close()
	if r3.StatusCode != http.StatusFound {
		t.Fatalf("workspace choice should redirect, got %d", r3.StatusCode)
	}
	loc, _ := url.Parse(r3.Header.Get("Location"))
	authCode := loc.Query().Get("code")
	if authCode == "" {
		t.Fatalf("no auth code in redirect: %q", loc.RawQuery)
	}

	// Exchange and confirm the token operates in the chosen workspace.
	tok := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"redirect_uri":  {redirectURI},
		"client_id":     {reg.ClientID},
		"code_verifier": {verifier},
	}
	tr := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", tok)
	tbody, _ := io.ReadAll(tr.Body)
	tr.Body.Close()
	var tk struct {
		AccessToken string `json:"access_token"`
	}
	json.Unmarshal(tbody, &tk)
	if tk.AccessToken == "" {
		t.Fatalf("no access token: %s", tbody)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+tk.AccessToken)
	req.Header.Set("Accept", "application/json")
	wresp, _ := http.DefaultClient.Do(req)
	var who struct {
		WorkspaceID string `json:"workspace_id"`
	}
	json.NewDecoder(wresp.Body).Decode(&who)
	wresp.Body.Close()
	if who.WorkspaceID != second.ID {
		t.Fatalf("token should operate in chosen workspace %s, got %s", second.ID, who.WorkspaceID)
	}

	// A forged/invalid ticket is rejected.
	bad := cloneValues(common)
	bad.Set("login_ticket", "not.a.valid.ticket")
	bad.Set("workspace_id", second.ID)
	rb := postForm(t, noRedirect, ts.URL+"/oauth/authorize", bad)
	rb.Body.Close()
	if rb.StatusCode == http.StatusFound {
		t.Fatal("a forged login ticket must not yield a redirect/code")
	}
}

// TestDCRAcceptsExtraFields verifies dynamic client registration ignores the
// extra RFC 7591 metadata real clients send rather than rejecting the request.
func TestDCRAcceptsExtraFields(t *testing.T) {
	ts, _ := newOAuthServer(t)
	body := `{
		"redirect_uris": ["https://client.example/cb"],
		"client_name": "Real Client",
		"grant_types": ["authorization_code", "refresh_token"],
		"response_types": ["code"],
		"token_endpoint_auth_method": "none",
		"scope": "crm",
		"client_uri": "https://client.example",
		"contacts": ["dev@client.example"]
	}`
	status, resp := do(t, ts, "POST", "/oauth/register", "", body)
	if status != http.StatusCreated {
		t.Fatalf("registration with standard RFC 7591 fields should succeed, got %d %q", status, resp)
	}
	if !strings.Contains(resp, `"client_id"`) {
		t.Fatalf("expected a client_id in response: %q", resp)
	}
}

// TestMCPRequestTool exercises the single generic request tool: an allowed CRM
// call runs, a write works, and disallowed paths/methods are refused as tool
// errors (never executed).
func TestMCPRequestTool(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// Allowed read.
	status, body := do(t, ts, "POST", "/mcp", token,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"request","arguments":{"method":"GET","path":"/whoami"}}}`)
	if status != 200 || !strings.Contains(body, "me@example.com") || strings.Contains(body, `"isError":true`) {
		t.Fatalf("request GET /whoami: %d %q", status, body)
	}

	// Allowed write (with a body) and a query string.
	status, body = do(t, ts, "POST", "/mcp", token,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request","arguments":{"method":"POST","path":"/contacts","body":{"name":"Leia"}}}}`)
	if status != 200 || !strings.Contains(body, "contact_") || strings.Contains(body, `"isError":true`) {
		t.Fatalf("request POST /contacts: %d %q", status, body)
	}

	// A path outside the CRM allowlist (the auth plane) is refused, not executed.
	status, body = do(t, ts, "POST", "/mcp", token,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request","arguments":{"method":"POST","path":"/auth/request","body":{"email":"x@y.z"}}}}`)
	if status != 200 || !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "not permitted") {
		t.Fatalf("blocked path should be refused: %d %q", status, body)
	}

	// An unsupported method is refused.
	status, body = do(t, ts, "POST", "/mcp", token,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"request","arguments":{"method":"FETCH","path":"/whoami"}}}`)
	if status != 200 || !strings.Contains(body, `"isError":true`) {
		t.Fatalf("bad method should be refused: %d %q", status, body)
	}
}

// TestOAuthCreateWorkspaceAtPicker verifies a user can create a brand-new
// workspace at the picker (instead of selecting an existing one), and the minted
// token operates in it.
func TestOAuthCreateWorkspaceAtPicker(t *testing.T) {
	ts, mailer := newOAuthServer(t)
	redirectURI := "https://client.example/cb"
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	_, body := do(t, ts, "POST", "/oauth/register", "", `{"redirect_uris":["`+redirectURI+`"]}`)
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal([]byte(body), &reg); err != nil || reg.ClientID == "" {
		t.Fatalf("register: %v (%s)", err, body)
	}
	verifier, challenge := pkcePair()
	common := url.Values{
		"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {redirectURI},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"st"}, "scope": {"crm"},
	}
	const email = "founder@example.com"

	s1 := cloneValues(common)
	s1.Set("email", email)
	postForm(t, noRedirect, ts.URL+"/oauth/authorize", s1).Body.Close()
	otp := sixDigits.FindString(mailer.last.Text)

	s2 := cloneValues(common)
	s2.Set("email", email)
	s2.Set("code", otp)
	r2 := postForm(t, noRedirect, ts.URL+"/oauth/authorize", s2)
	pickerBody, _ := io.ReadAll(r2.Body)
	r2.Body.Close()

	// At the picker, create a new workspace instead of picking the existing one.
	tm := ticketRe.FindSubmatch(pickerBody)
	if tm == nil {
		t.Fatalf("no login_ticket in picker:\n%s", pickerBody)
	}
	s3 := cloneValues(common)
	s3.Set("email", email)
	s3.Set("login_ticket", string(tm[1]))
	s3.Set("new_workspace", "Project X")
	r3 := postForm(t, noRedirect, ts.URL+"/oauth/authorize", s3)
	r3.Body.Close()
	if r3.StatusCode != http.StatusFound {
		t.Fatalf("create-workspace step should redirect, got %d", r3.StatusCode)
	}
	loc, _ := url.Parse(r3.Header.Get("Location"))
	authCode := loc.Query().Get("code")
	if authCode == "" {
		t.Fatalf("no auth code in redirect: %q", loc.RawQuery)
	}

	tr := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {authCode}, "redirect_uri": {redirectURI},
		"client_id": {reg.ClientID}, "code_verifier": {verifier},
	})
	tb, _ := io.ReadAll(tr.Body)
	tr.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(tb, &tok); tok.AccessToken == "" {
		t.Fatalf("no access token: %s", tb)
	}

	// The new workspace exists and the token works.
	if status, list := do(t, ts, "GET", "/workspaces", tok.AccessToken, ""); status != 200 || !strings.Contains(list, "Project X") {
		t.Fatalf("new workspace 'Project X' not present: %d %q", status, list)
	}
}

// obtainAuthCode registers a client and runs the authorize flow (single-
// workspace user) to return a fresh authorization code plus the client id and
// PKCE verifier needed to redeem it.
func obtainAuthCode(t *testing.T, ts *httptest.Server, mailer *captureMailer, email, redirectURI string) (clientID, verifier, code string) {
	t.Helper()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	_, body := do(t, ts, "POST", "/oauth/register", "", `{"redirect_uris":["`+redirectURI+`"]}`)
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal([]byte(body), &reg); err != nil || reg.ClientID == "" {
		t.Fatalf("register: %v (%s)", err, body)
	}
	v, challenge := pkcePair()
	common := url.Values{
		"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {redirectURI},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"st"}, "scope": {"crm"},
	}
	s1 := cloneValues(common)
	s1.Set("email", email)
	postForm(t, noRedirect, ts.URL+"/oauth/authorize", s1).Body.Close()
	otp := sixDigits.FindString(mailer.last.Text)
	s2 := cloneValues(common)
	s2.Set("email", email)
	s2.Set("code", otp)
	r2 := postForm(t, noRedirect, ts.URL+"/oauth/authorize", s2)
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	loc := completePicker(t, ts, noRedirect, common, email, b2)
	if loc.Query().Get("code") == "" {
		t.Fatalf("no auth code in redirect: %q", loc.RawQuery)
	}
	return reg.ClientID, v, loc.Query().Get("code")
}

// TestOAuthTokenRejections covers the token endpoint's invalid_grant paths:
// wrong PKCE verifier, mismatched client_id, mismatched redirect_uri, plus an
// unsupported grant type.
func TestOAuthTokenRejections(t *testing.T) {
	ts, mailer := newOAuthServer(t)
	redirectURI := "https://client.example/cb"

	cases := []struct {
		name  string
		email string
		mut   func(url.Values)
	}{
		{"bad pkce", "p1@example.com", func(v url.Values) { v.Set("code_verifier", "the-wrong-verifier") }},
		{"client mismatch", "p2@example.com", func(v url.Values) { v.Set("client_id", "not-the-client") }},
		{"redirect mismatch", "p3@example.com", func(v url.Values) { v.Set("redirect_uri", "https://evil.example/cb") }},
	}
	for _, c := range cases {
		clientID, verifier, code := obtainAuthCode(t, ts, mailer, c.email, redirectURI)
		form := url.Values{
			"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
			"client_id": {clientID}, "code_verifier": {verifier},
		}
		c.mut(form)
		r := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", form)
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "invalid_grant") {
			t.Fatalf("%s: expected 400 invalid_grant, got %d %s", c.name, r.StatusCode, b)
		}
	}

	r := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", url.Values{"grant_type": {"password"}})
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "unsupported_grant_type") {
		t.Fatalf("unsupported grant: %d %s", r.StatusCode, b)
	}
}

// TestOAuthRevoke verifies /oauth/revoke kills both access and refresh tokens.
func TestOAuthRevoke(t *testing.T) {
	ts, mailer := newOAuthServer(t)
	redirectURI := "https://client.example/cb"
	clientID, verifier, code := obtainAuthCode(t, ts, mailer, "revoke@example.com", redirectURI)

	r := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"client_id": {clientID}, "code_verifier": {verifier},
	})
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	var tk struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(b, &tk)
	if tk.AccessToken == "" || tk.RefreshToken == "" {
		t.Fatalf("token response: %s", b)
	}

	// Access token works, then is revoked.
	if status, _ := do(t, ts, "POST", "/mcp", tk.AccessToken, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); status != 200 {
		t.Fatalf("access token should work pre-revoke, got %d", status)
	}
	postForm(t, http.DefaultClient, ts.URL+"/oauth/revoke", url.Values{"token": {tk.AccessToken}}).Body.Close()
	if status, _ := do(t, ts, "POST", "/mcp", tk.AccessToken, `{"jsonrpc":"2.0","id":2,"method":"ping"}`); status != http.StatusUnauthorized {
		t.Fatalf("revoked access token should 401, got %d", status)
	}

	// Refresh token is revoked too: it can no longer be exchanged.
	postForm(t, http.DefaultClient, ts.URL+"/oauth/revoke", url.Values{"token": {tk.RefreshToken}}).Body.Close()
	rr := postForm(t, http.DefaultClient, ts.URL+"/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {tk.RefreshToken}, "client_id": {clientID},
	})
	rr.Body.Close()
	if rr.StatusCode != http.StatusBadRequest {
		t.Fatalf("revoked refresh token should not exchange, got %d", rr.StatusCode)
	}
}

// TestOAuthAuthorizeRejectsBadParams covers the authorize page's validation:
// unknown client, wrong response_type, and an unregistered redirect_uri must not
// reach the login form.
func TestOAuthAuthorizeRejectsBadParams(t *testing.T) {
	ts, _ := newOAuthServer(t)
	redirectURI := "https://client.example/cb"

	// Unknown client_id.
	q := url.Values{"response_type": {"code"}, "client_id": {"mcpc_nope"}, "redirect_uri": {redirectURI},
		"code_challenge": {"c"}, "code_challenge_method": {"S256"}}
	if status, body := do(t, ts, "GET", "/oauth/authorize?"+q.Encode(), "", ""); status != http.StatusBadRequest || !strings.Contains(body, "Unknown client") {
		t.Fatalf("unknown client: %d %q", status, body)
	}

	// Register a real client for the remaining cases.
	_, rb := do(t, ts, "POST", "/oauth/register", "", `{"redirect_uris":["`+redirectURI+`"]}`)
	var reg struct {
		ClientID string `json:"client_id"`
	}
	json.Unmarshal([]byte(rb), &reg)

	// Wrong response_type.
	q2 := url.Values{"response_type": {"token"}, "client_id": {reg.ClientID}, "redirect_uri": {redirectURI},
		"code_challenge": {"c"}, "code_challenge_method": {"S256"}}
	if status, body := do(t, ts, "GET", "/oauth/authorize?"+q2.Encode(), "", ""); status != http.StatusBadRequest || !strings.Contains(body, "response_type") {
		t.Fatalf("bad response_type: %d %q", status, body)
	}

	// redirect_uri not registered for this client.
	q3 := url.Values{"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {"https://evil.example/cb"},
		"code_challenge": {"c"}, "code_challenge_method": {"S256"}}
	if status, body := do(t, ts, "GET", "/oauth/authorize?"+q3.Encode(), "", ""); status != http.StatusBadRequest || !strings.Contains(body, "redirect_uri") {
		t.Fatalf("unregistered redirect_uri: %d %q", status, body)
	}
}

// TestMCPProtocol covers the JSON-RPC surface: GET is rejected, unknown methods
// and tools error cleanly, ping works, and a batch returns an array.
func TestMCPProtocol(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	if status, _ := do(t, ts, "GET", "/mcp", token, ""); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp should be 405, got %d", status)
	}
	if _, body := do(t, ts, "POST", "/mcp", token, `{"jsonrpc":"2.0","id":1,"method":"frobnicate"}`); !strings.Contains(body, "-32601") {
		t.Fatalf("unknown method should be -32601: %q", body)
	}
	if _, body := do(t, ts, "POST", "/mcp", token, `{"jsonrpc":"2.0","id":2,"method":"ping"}`); !strings.Contains(body, `"result":{}`) {
		t.Fatalf("ping should return empty result: %q", body)
	}
	if _, body := do(t, ts, "POST", "/mcp", token, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}`); !strings.Contains(body, "unknown tool") {
		t.Fatalf("unknown tool should error: %q", body)
	}
	// Batch: one request + one notification -> a one-element array.
	status, body := do(t, ts, "POST", "/mcp", token,
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"}]`)
	if status != 200 || !strings.HasPrefix(strings.TrimSpace(body), "[") {
		t.Fatalf("batch should return a JSON array: %d %q", status, body)
	}
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
