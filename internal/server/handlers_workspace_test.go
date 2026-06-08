package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestInviteRejectsControlCharsInEmail guards the injection root cause: an invite
// email carrying a CR/LF (a header / MIME-boundary smuggling attempt) is rejected
// with 400 before any message is composed - the address must be a clean addr-spec.
func TestInviteRejectsControlCharsInEmail(t *testing.T) {
	ts := newTestServer(t)
	tok, wsID := loginAs(t, ts, "admin@acme.com")

	// JSON \r\n decodes to a real CR LF embedded mid-address; NormalizeEmail's trim
	// only strips the ends, so it survives to ValidEmail, which must reject it.
	body := `{"email":"evil@x.com\r\nBcc: victim@x.com","role":"member"}`
	if s, b := do(t, ts, "POST", "/workspaces/"+wsID+"/invites", tok, body); s != http.StatusBadRequest || !strings.Contains(b, "invalid_email") {
		t.Fatalf("invite with a CRLF email should 400 invalid_email, got %d %q", s, b)
	}

	// A clean address still works.
	if s, b := do(t, ts, "POST", "/workspaces/"+wsID+"/invites", tok, `{"email":"teammate@acme.com","role":"member"}`); s != http.StatusCreated {
		t.Fatalf("a valid invite should succeed, got %d %q", s, b)
	}
}
