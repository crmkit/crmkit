package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// loginAs runs the full OTP flow for an arbitrary email (dev mode echoes the
// code) and returns the bearer token plus the user's default workspace id.
func loginAs(t *testing.T, ts *httptest.Server, email string) (token, workspaceID string) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/auth/request", strings.NewReader(`{"email":"`+email+`"}`))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("auth request %s: %v", email, err)
	}
	var rr struct {
		LocalCode string `json:"local_code"`
	}
	json.NewDecoder(resp.Body).Decode(&rr)
	resp.Body.Close()
	if rr.LocalCode == "" {
		t.Fatalf("no local code for %s", email)
	}

	vreq, _ := http.NewRequest("POST", ts.URL+"/auth/verify",
		strings.NewReader(`{"email":"`+email+`","code":"`+rr.LocalCode+`"}`))
	vreq.Header.Set("Accept", "application/json")
	vresp, err := http.DefaultClient.Do(vreq)
	if err != nil {
		t.Fatalf("auth verify %s: %v", email, err)
	}
	var vr struct {
		Token       string `json:"token"`
		WorkspaceID string `json:"workspace_id"`
	}
	json.NewDecoder(vresp.Body).Decode(&vr)
	vresp.Body.Close()
	if vr.Token == "" {
		t.Fatalf("no token for %s", email)
	}
	return vr.Token, vr.WorkspaceID
}

// firstToken extracts the first "ck_…" bearer token from a body.
func firstToken(body string) string {
	i := strings.Index(body, "ck_")
	if i < 0 {
		return ""
	}
	s := body[i:]
	end := 0
	for end < len(s) {
		c := s[end]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			end++
			continue
		}
		break
	}
	return s[:end]
}

// memberID looks up a member's user id by email from the members listing.
func memberID(t *testing.T, ts *httptest.Server, adminToken, wsID, email string) string {
	t.Helper()
	_, body := do(t, ts, "GET", "/workspaces/"+wsID+"/members", adminToken, "")
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "member/") && strings.Contains(line, "email="+email) {
			return firstHandleID(line, "member/")
		}
	}
	return ""
}

// localCode extracts the "(local code: NNNNNN)" value from an escalation response.
func localCode(body string) string {
	const marker = "local code: "
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	s := body[i+len(marker):]
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return s[:end]
}

func TestMemberCannotPerformAdminActions(t *testing.T) {
	ts := newTestServer(t)
	adminTok, wsA := loginAs(t, ts, "alice@acme.com")

	// Admin invites a member (this admin action should succeed).
	if st, body := do(t, ts, "POST", "/workspaces/"+wsA+"/invites", adminTok, `{"email":"bob@acme.com"}`); st != http.StatusCreated {
		t.Fatalf("admin invite should succeed: %d %q", st, body)
	}

	// Bob joins on login, then mints a token scoped to workspace A.
	bobDefault, _ := loginAs(t, ts, "bob@acme.com")
	st, body := do(t, ts, "POST", "/workspaces/"+wsA+"/tokens", bobDefault, "")
	if st != http.StatusCreated {
		t.Fatalf("member should be able to mint a workspace token: %d %q", st, body)
	}
	bobA := firstToken(body)
	if bobA == "" {
		t.Fatalf("no token minted: %q", body)
	}
	bobID := memberID(t, ts, adminTok, wsA, "bob@acme.com")

	// Every admin-only endpoint must reject the member with 403 admin_required.
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/workspaces/" + wsA + "/invites", `{"email":"carol@acme.com"}`},
		{"POST", "/workspaces/" + wsA + "/members/" + bobID + "/role", `{"role":"admin"}`},
		{"DELETE", "/workspaces/" + wsA + "/members/" + bobID, ""},
		{"DELETE", "/workspaces/" + wsA, ""},
	} {
		st, body := do(t, ts, c.method, c.path, bobA, c.body)
		if st != http.StatusForbidden || !strings.Contains(body, "admin_required") {
			t.Fatalf("member %s %s: want 403 admin_required, got %d %q", c.method, c.path, st, body)
		}
	}
}

func TestNonMemberRejected(t *testing.T) {
	ts := newTestServer(t)
	_, wsA := loginAs(t, ts, "alice@acme.com")
	carolTok, _ := loginAs(t, ts, "carol@acme.com") // never invited to A

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/workspaces/" + wsA + "/tokens", ""},
		{"GET", "/workspaces/" + wsA + "/members", ""},
		{"POST", "/workspaces/" + wsA + "/invites", `{"email":"x@y.com"}`},
	} {
		st, body := do(t, ts, c.method, c.path, carolTok, c.body)
		if st != http.StatusForbidden || !strings.Contains(body, "not_a_member") {
			t.Fatalf("non-member %s %s: want 403 not_a_member, got %d %q", c.method, c.path, st, body)
		}
	}
}

func TestCrossWorkspaceIsolationViaAPI(t *testing.T) {
	ts := newTestServer(t)
	aliceTok, _ := loginAs(t, ts, "alice@acme.com")

	st, body := do(t, ts, "POST", "/contacts", aliceTok, `{"name":"Secret Lead","email":"secret@acme.com"}`)
	if st != http.StatusCreated {
		t.Fatalf("create contact: %d %q", st, body)
	}
	cid := firstHandleID(body, "contact/")

	// Bob operates in his own workspace and must not see Alice's data.
	bobTok, _ := loginAs(t, ts, "bob@other.com")
	if st, body := do(t, ts, "GET", "/contacts", bobTok, ""); st != 200 || !strings.Contains(body, "# 0 contact") {
		t.Fatalf("bob should see no contacts: %d %q", st, body)
	}
	if st, _ := do(t, ts, "GET", "/contacts/"+cid, bobTok, ""); st != http.StatusNotFound {
		t.Fatalf("bob fetching alice's contact should 404, got %d", st)
	}
}

func TestPromoteToAdminRequiresEscalation(t *testing.T) {
	ts := newTestServer(t)
	aliceTok, wsA := loginAs(t, ts, "alice@acme.com")
	do(t, ts, "POST", "/workspaces/"+wsA+"/invites", aliceTok, `{"email":"bob@acme.com"}`)
	loginAs(t, ts, "bob@acme.com")
	bobID := memberID(t, ts, aliceTok, wsA, "bob@acme.com")
	if bobID == "" {
		t.Fatal("bob should be a member")
	}
	path := "/workspaces/" + wsA + "/members/" + bobID + "/role"

	// No code -> escalation challenge.
	st, body := do(t, ts, "POST", path, aliceTok, `{"role":"admin"}`)
	if st != http.StatusForbidden || !strings.Contains(body, "escalation_required") {
		t.Fatalf("want escalation_required, got %d %q", st, body)
	}
	code := localCode(body)
	if code == "" {
		t.Fatalf("no local code in %q", body)
	}
	// Wrong code rejected.
	if st, b := do(t, ts, "POST", path+"?code=000000", aliceTok, `{"role":"admin"}`); st != http.StatusForbidden || !strings.Contains(b, "invalid_code") {
		t.Fatalf("wrong code: want invalid_code, got %d %q", st, b)
	}
	// Correct code completes the promotion.
	if st, b := do(t, ts, "POST", path+"?code="+code, aliceTok, `{"role":"admin"}`); st != 200 || !strings.Contains(b, "role=admin") {
		t.Fatalf("correct code: want success, got %d %q", st, b)
	}
}

func TestDeleteWorkspaceRequiresEscalation(t *testing.T) {
	ts := newTestServer(t)
	aliceTok, wsA := loginAs(t, ts, "alice@acme.com")

	st, body := do(t, ts, "DELETE", "/workspaces/"+wsA, aliceTok, "")
	if st != http.StatusForbidden || !strings.Contains(body, "escalation_required") {
		t.Fatalf("want escalation_required, got %d %q", st, body)
	}
	code := localCode(body)
	if st, b := do(t, ts, "DELETE", "/workspaces/"+wsA+"?code="+code, aliceTok, ""); st != 200 || !strings.Contains(b, "deleted") {
		t.Fatalf("delete with code: %d %q", st, b)
	}
}

func TestUpsertContactByEmail(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := loginAs(t, ts, "alice@acme.com")

	// First POST creates.
	st, body := do(t, ts, "POST", "/contacts", tok, `{"name":"Jane Doe","email":"jane@acme.com","stage":"lead"}`)
	if st != http.StatusCreated || !strings.Contains(body, "# created") {
		t.Fatalf("first create: %d %q", st, body)
	}

	// Second POST with the same email (different case) updates, not duplicates;
	// partial merge keeps the unspecified stage.
	st, body = do(t, ts, "POST", "/contacts", tok, `{"name":"Jane Updated","email":"JANE@acme.com"}`)
	if st != http.StatusOK || !strings.Contains(body, "# updated") {
		t.Fatalf("upsert update: %d %q", st, body)
	}
	if !strings.Contains(body, "Jane Updated") || !strings.Contains(body, "lead") {
		t.Fatalf("expected merged record (new name, kept stage): %q", body)
	}

	// A different email creates a second contact.
	if st, _ := do(t, ts, "POST", "/contacts", tok, `{"name":"Bob","email":"bob@acme.com"}`); st != http.StatusCreated {
		t.Fatalf("distinct email should create: %d", st)
	}

	// Exactly two contacts exist.
	if _, body := do(t, ts, "GET", "/contacts", tok, ""); !strings.Contains(body, "# 2 contact") {
		t.Fatalf("expected 2 contacts, got %q", body)
	}

	// A contact with no email is a plain create each time (no key to match).
	do(t, ts, "POST", "/contacts", tok, `{"name":"Anon One"}`)
	do(t, ts, "POST", "/contacts", tok, `{"name":"Anon Two"}`)
	if _, body := do(t, ts, "GET", "/contacts", tok, ""); !strings.Contains(body, "# 4 contact") {
		t.Fatalf("email-less contacts should not upsert, got %q", body)
	}
}

func TestUpsertCompanyByDomain(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := loginAs(t, ts, "alice@acme.com")

	if st, body := do(t, ts, "POST", "/companies", tok, `{"name":"Acme","domain":"acme.com"}`); st != http.StatusCreated || !strings.Contains(body, "# created") {
		t.Fatalf("create company: %d %q", st, body)
	}
	st, body := do(t, ts, "POST", "/companies", tok, `{"name":"Acme Inc","domain":"ACME.com"}`)
	if st != http.StatusOK || !strings.Contains(body, "# updated") || !strings.Contains(body, "Acme Inc") {
		t.Fatalf("upsert company: %d %q", st, body)
	}
	if _, body := do(t, ts, "GET", "/companies", tok, ""); !strings.Contains(body, "# 1 company") {
		t.Fatalf("expected 1 company, got %q", body)
	}
}

func TestRemindersEndpoint(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := loginAs(t, ts, "alice@acme.com")

	st, body := do(t, ts, "POST", "/contacts", tok, `{"name":"Jane Doe"}`)
	if st != http.StatusCreated {
		t.Fatalf("create contact: %d %q", st, body)
	}
	cid := firstHandleID(body, "contact/")

	if st, _ := do(t, ts, "PATCH", "/contacts/"+cid, tok, `{"follow_up_at":"2020-01-01T00:00:00Z","follow_up_note":"ping"}`); st != 200 {
		t.Fatalf("set follow-up: %d", st)
	}
	if st, body := do(t, ts, "GET", "/reminders", tok, ""); st != 200 || !strings.Contains(body, "overdue=yes") || !strings.Contains(body, "# 1 reminder") {
		t.Fatalf("reminders: %d %q", st, body)
	}
}
