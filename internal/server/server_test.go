package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crmkit/crmkit/internal/config"
	"github.com/crmkit/crmkit/internal/ratelimit"
	"github.com/crmkit/crmkit/internal/store"
)

func memoryRL(t *testing.T) ratelimit.Provider {
	t.Helper()
	p, err := ratelimit.Open(ratelimit.BackendMemory, "")
	if err != nil {
		t.Fatalf("open rate limiter: %v", err)
	}
	return p
}

// newMigratedStore opens an in-memory store and applies migrations (Open no
// longer creates schema), returning a ready store closed at test end.
func newMigratedStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open("sqlite", ":memory:", store.Options{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.ApplyMigrations(); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return st
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	st := newMigratedStore(t)

	cfg := config.Default()
	cfg.Server.Local = true // echo login codes so the test can complete auth
	// The shared harness exercises behavior (auth, roles, CRUD), not quotas, so
	// give the default plan unlimited limits - otherwise the basic plan's
	// max_members=1 blocks role/escalation tests that need a second member. Quota
	// enforcement is covered separately by quota_test.go (its own low-limit config).
	if pl, ok := cfg.Plans.Catalogue[cfg.Plans.Default]; ok {
		pl.MaxWorkspaces, pl.MaxMembers, pl.MaxContacts, pl.MaxCompanies, pl.MaxDeals = limitPtr(-1), limitPtr(-1), limitPtr(-1), limitPtr(-1), limitPtr(-1)
		cfg.Plans.Catalogue[cfg.Plans.Default] = pl
	}
	srv := New(cfg, st, memoryRL(t))

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// do is a tiny request helper returning status and trimmed text body.
func do(t *testing.T, ts *httptest.Server, method, path, token, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func authenticate(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	// Request a login code (local mode echoes it as JSON local_code).
	req, _ := http.NewRequest("POST", ts.URL+"/auth/request", strings.NewReader(`{"email":"me@example.com"}`))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("auth request: %v", err)
	}
	var rr struct {
		LocalCode string `json:"local_code"`
	}
	json.NewDecoder(resp.Body).Decode(&rr)
	resp.Body.Close()
	if rr.LocalCode == "" {
		t.Fatal("expected local_code in local mode")
	}

	// Verify and capture the token.
	vreq, _ := http.NewRequest("POST", ts.URL+"/auth/verify",
		strings.NewReader(`{"email":"me@example.com","code":"`+rr.LocalCode+`"}`))
	vreq.Header.Set("Accept", "application/json")
	vresp, err := http.DefaultClient.Do(vreq)
	if err != nil {
		t.Fatalf("auth verify: %v", err)
	}
	var vr struct {
		Token string `json:"token"`
	}
	json.NewDecoder(vresp.Body).Decode(&vr)
	vresp.Body.Close()
	if vr.Token == "" {
		t.Fatal("expected token from verify")
	}
	return vr.Token
}

func firstHandleID(body, prefix string) string {
	i := strings.Index(body, prefix)
	if i < 0 && strings.HasSuffix(prefix, "/") {
		// CRM records now render as "kind_<handle>" (members/workspaces/audit keep
		// "kind/"). Fall back to the new separator; the returned bare handle works
		// directly as a path id (the server resolves it).
		prefix = strings.TrimSuffix(prefix, "/") + "_"
		i = strings.Index(body, prefix)
	}
	if i < 0 {
		return ""
	}
	rest := body[i+len(prefix):]
	if j := strings.IndexAny(rest, " \t\n"); j >= 0 {
		return rest[:j]
	}
	return rest
}

func TestHealthEndpoints(t *testing.T) {
	ts := newTestServer(t)
	if status, body := do(t, ts, "GET", "/healthz", "", ""); status != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("healthz: %d %q", status, body)
	}
	if status, _ := do(t, ts, "GET", "/readyz", "", ""); status != 200 {
		t.Fatalf("readyz: %d", status)
	}
}

func TestTokenListAndRevoke(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	status, body := do(t, ts, "GET", "/tokens", token, "")
	if status != 200 || !strings.Contains(body, "current=yes") {
		t.Fatalf("list tokens: %d %q", status, body)
	}
	id := firstHandleID(body, "token/")
	if id == "" {
		t.Fatalf("no token id in %q", body)
	}

	if status, body := do(t, ts, "DELETE", "/tokens/"+id, token, ""); status != 200 || !strings.Contains(body, "revoked") {
		t.Fatalf("revoke: %d %q", status, body)
	}
	// The revoked token no longer authenticates.
	if status, _ := do(t, ts, "GET", "/whoami", token, ""); status != http.StatusUnauthorized {
		t.Fatalf("revoked token should 401, got %d", status)
	}
}

func TestRateLimit(t *testing.T) {
	st := newMigratedStore(t)
	cfg := config.Default()
	cfg.RateLimit.RPS = 1
	cfg.RateLimit.Burst = 1
	ts := httptest.NewServer(New(cfg, st, memoryRL(t)).Handler())
	t.Cleanup(ts.Close)

	// First request through the limiter passes (401 unauth, but not limited);
	// the second from the same IP is throttled.
	if status, _ := do(t, ts, "GET", "/whoami", "", ""); status == http.StatusTooManyRequests {
		t.Fatal("first request should not be rate limited")
	}
	if status, body := do(t, ts, "GET", "/whoami", "", ""); status != http.StatusTooManyRequests || !strings.Contains(body, "rate_limited") {
		t.Fatalf("second request should be 429, got %d %q", status, body)
	}
	// Health probes are exempt from rate limiting.
	for i := 0; i < 5; i++ {
		if status, _ := do(t, ts, "GET", "/healthz", "", ""); status != 200 {
			t.Fatalf("health probe should be exempt, got %d", status)
		}
	}
}

func TestClientIPProxyHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/whoami", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	req.Header.Set("CF-Connecting-IP", "203.0.113.8")
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	req.Header.Set("X-Real-IP", "198.51.100.9")

	cfg := config.Default()
	srv := &Server{cfg: cfg}
	if got := srv.clientIP(req); got != "10.0.0.2" {
		t.Fatalf("clientIP without trust = %q, want remote addr host", got)
	}

	cfg.Server.TrustProxyHeaders = true
	srv = &Server{cfg: cfg}
	if got := srv.clientIP(req); got != "203.0.113.8" {
		t.Fatalf("clientIP with Cloudflare header = %q, want CF-Connecting-IP", got)
	}

	req.Header.Del("CF-Connecting-IP")
	if got := srv.clientIP(req); got != "198.51.100.7" {
		t.Fatalf("clientIP with X-Forwarded-For = %q, want first forwarded address", got)
	}

	req.Header.Del("X-Forwarded-For")
	if got := srv.clientIP(req); got != "198.51.100.9" {
		t.Fatalf("clientIP with X-Real-IP = %q, want real IP", got)
	}
}

func TestUnauthenticatedIsRejected(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, "GET", "/contacts", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", status)
	}
	if !strings.Contains(body, "auth_required") {
		t.Fatalf("expected instructive error, got %q", body)
	}
}

func TestFullContactFlowPlainText(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// whoami works with the token and surfaces the workspace name and the
	// display timezone (default UTC) so the agent knows the offset to write in.
	if status, body := do(t, ts, "GET", "/whoami", token, ""); status != 200 ||
		!strings.Contains(body, "me@example.com") || !strings.Contains(body, "workspace_name:") ||
		!strings.Contains(body, "timezone:") || !strings.Contains(body, "UTC") {
		t.Fatalf("whoami failed: %d %q", status, body)
	}

	// Create a contact; response is plain text with a handle.
	status, body := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe","email":"jane@acme.com","stage":"lead"}`)
	if status != http.StatusCreated {
		t.Fatalf("create contact: %d %q", status, body)
	}
	if !strings.Contains(body, "contact_") || !strings.Contains(body, "Jane Doe") {
		t.Fatalf("unexpected create body: %q", body)
	}

	// List contains the contact and a count summary.
	status, body = do(t, ts, "GET", "/contacts", token, "")
	if status != 200 || !strings.Contains(body, "jane@acme.com") || !strings.Contains(body, "# 1 contact") {
		t.Fatalf("list contacts: %d %q", status, body)
	}
}

func TestDeleteRequiresConfirmation(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// Create a contact and capture its id from JSON.
	req, _ := http.NewRequest("POST", ts.URL+"/contacts", strings.NewReader(`{"name":"Temp"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("no id returned")
	}

	// First delete attempt is gated.
	status, body := do(t, ts, "DELETE", "/contacts/"+created.ID, token, "")
	if status != http.StatusConflict || !strings.Contains(body, "confirmation_required") || !strings.Contains(body, "confirm=") {
		t.Fatalf("expected confirmation gate, got %d %q", status, body)
	}

	// Extract the confirm token and retry.
	confirm := confirmToken(created.ID)
	status, body = do(t, ts, "DELETE", "/contacts/"+created.ID+"?confirm="+confirm, token, "")
	if status != 200 || !strings.Contains(body, "deleted") {
		t.Fatalf("expected successful delete, got %d %q", status, body)
	}
}

func TestJSONNegotiation(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	req, _ := http.NewRequest("GET", ts.URL+"/contacts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected json content-type, got %q", ct)
	}
	var page struct {
		Items      []any  `json:"items"`
		Total      *int   `json:"total"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("expected json {items,total,next_cursor}, got error %v", err)
	}
	// A paginated list reports its total; with a single page it equals the count
	// of returned rows.
	if page.Total == nil {
		t.Fatalf("expected a total in the list response, got none")
	}
	if page.NextCursor == "" && *page.Total != len(page.Items) {
		t.Fatalf("single-page total %d != items %d", *page.Total, len(page.Items))
	}
}
