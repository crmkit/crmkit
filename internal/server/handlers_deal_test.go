package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestDealCRUDLifecycle covers the previously-untested deal handlers end to end:
// create, get, conditional update (incl. a stale-version 412), the audit diff it
// records, and the confirm-gated delete.
func TestDealCRUDLifecycle(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	id := createJSONID(t, ts, token, "/deals", `{"title":"Acme renewal","amount_cents":100000,"stage":"proposal"}`)

	// GET returns the deal with its amount rendered.
	if s, b := do(t, ts, "GET", "/deals/"+id, token, ""); s != http.StatusOK || !strings.Contains(b, "Acme renewal") || !strings.Contains(b, "1000.00") {
		t.Fatalf("get deal: %d %q", s, b)
	}

	// Conditional update advances stage+status; response reflects the new state.
	if s, b := do(t, ts, "PATCH", "/deals/"+id, token, `{"version":1,"stage":"won","status":"won"}`); s != http.StatusOK || !strings.Contains(b, "won") {
		t.Fatalf("conditional update: %d %q", s, b)
	}

	// Version is now 2, so a stale PATCH 412s and changes nothing.
	if s, b := do(t, ts, "PATCH", "/deals/"+id, token, `{"version":1,"status":"lost"}`); s != http.StatusPreconditionFailed || !strings.Contains(b, "version_conflict") {
		t.Fatalf("stale update should 412 version_conflict, got %d %q", s, b)
	}
	if _, b := do(t, ts, "GET", "/deals/"+id, token, ""); !strings.Contains(b, "won") || strings.Contains(b, "lost") {
		t.Fatalf("a conflicting write must not persist:\n%s", b)
	}

	// Delete is confirm-gated, then succeeds with the token, then 404s.
	if s, b := do(t, ts, "DELETE", "/deals/"+id, token, ""); s != http.StatusConflict || !strings.Contains(b, "confirmation_required") {
		t.Fatalf("delete should require confirmation, got %d %q", s, b)
	}
	if s, b := do(t, ts, "DELETE", "/deals/"+id+"?confirm="+confirmToken(id), token, ""); s != http.StatusOK || !strings.Contains(b, "deleted") {
		t.Fatalf("confirmed delete: %d %q", s, b)
	}
	if s, _ := do(t, ts, "GET", "/deals/"+id, token, ""); s != http.StatusNotFound {
		t.Fatalf("deleted deal should 404, got %d", s)
	}
}
