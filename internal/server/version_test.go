package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestConcurrencyConflictViaAPI exercises optimistic concurrency end to end: a
// PATCH carrying a stale "version" is rejected with 412, while the same PATCH
// without a version (opt-out) wins.
func TestConcurrencyConflictViaAPI(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, b := do(t, ts, "POST", "/contacts", token, `{"name":"Jane"}`)
	id := firstHandleID(b, "contact/")
	if id == "" {
		t.Fatalf("no contact handle in %q", b)
	}
	if !strings.Contains(b, "version:") {
		t.Fatalf("create response should expose a version:\n%s", b)
	}

	// Conditional update with the current version (1) succeeds; version -> 2.
	if s, ub := do(t, ts, "PATCH", "/contacts/"+id, token, `{"version":1,"stage":"customer"}`); s != http.StatusOK {
		t.Fatalf("matching conditional update: %d %q", s, ub)
	}

	// A second update still claiming version 1 is now stale -> 412 (the clobber
	// would have set stage=lead).
	s, cb := do(t, ts, "PATCH", "/contacts/"+id, token, `{"version":1,"stage":"lead"}`)
	if s != http.StatusPreconditionFailed || !strings.Contains(cb, "version_conflict") {
		t.Fatalf("stale update should 412 version_conflict, got %d %q", s, cb)
	}

	// The conflicting write changed nothing: stage is still customer, not lead.
	if _, d := do(t, ts, "GET", "/contacts/"+id, token, ""); !strings.Contains(d, "customer") || strings.Contains(d, "lead") {
		t.Fatalf("record must be untouched after the conflict:\n%s", d)
	}

	// Omitting the version opts out of the check: last write wins.
	if s, _ := do(t, ts, "PATCH", "/contacts/"+id, token, `{"stage":"lead"}`); s != http.StatusOK {
		t.Fatalf("unconditional update should succeed, got %d", s)
	}
}

// TestIfMatchHeader checks the standard If-Match request header path.
func TestIfMatchHeader(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, b := do(t, ts, "POST", "/contacts", token, `{"name":"Jane"}`)
	id := firstHandleID(b, "contact/")

	// Stale If-Match header -> 412.
	req, _ := http.NewRequest("PATCH", ts.URL+"/contacts/"+id, strings.NewReader(`{"stage":"won"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("If-Match", "999")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match should 412, got %d", resp.StatusCode)
	}
}
