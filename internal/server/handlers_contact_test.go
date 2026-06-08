package server

import (
	"net/http"
	"strings"
	"testing"
)

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
