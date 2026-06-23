package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestOnBehalfOfDerivedOnObjects verifies on_behalf_of on a record (here a contact)
// is DERIVED from its activities, not a stored field: the line surfaces the full
// set of principals work was done for (so Alice AND Bob both show when both worked
// it), and the list is filterable by any one of them (case-insensitive). It also
// confirms the filter is rejected on a resource with no activity log (tasks).
func TestOnBehalfOfDerivedOnObjects(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, cb := do(t, ts, "POST", "/contacts", token, `{"name":"Jane","email":"jane@acme.com"}`)
	contact := firstHandleID(cb, "contact/")
	if contact == "" {
		t.Fatalf("no contact id in %q", cb)
	}

	// Two people did work for this contact, plus a note with no principal.
	do(t, ts, "POST", "/contacts/"+contact+"/activities", token, `{"kind":"email","body":"a","on_behalf_of":"alice@acme.com"}`)
	do(t, ts, "POST", "/contacts/"+contact+"/activities", token, `{"kind":"call","body":"b","on_behalf_of":"bob@acme.com"}`)
	do(t, ts, "POST", "/contacts/"+contact+"/activities", token, `{"kind":"note","body":"c"}`)

	// The line surfaces the derived SET (sorted, deduped, blanks skipped).
	_, list := do(t, ts, "GET", "/contacts", token, "")
	if !strings.Contains(list, "on_behalf_of=alice@acme.com,bob@acme.com") {
		t.Fatalf("contact line should surface the derived principal set alice,bob:\n%s", list)
	}

	// Filter by either principal - case-insensitive (BOB matches bob).
	_, forBob := do(t, ts, "GET", "/contacts?on_behalf_of=BOB@acme.com", token, "")
	if !strings.Contains(forBob, "contact_"+contact) {
		t.Fatalf("?on_behalf_of=BOB should match the contact (case-insensitive):\n%s", forBob)
	}
	_, forCarol := do(t, ts, "GET", "/contacts?on_behalf_of=carol@acme.com", token, "")
	if strings.Contains(forCarol, "contact_"+contact) {
		t.Fatalf("no work was done for carol; contact must not match:\n%s", forCarol)
	}

	// Not filterable where there is no activity log to derive from (tasks).
	if s, _ := do(t, ts, "GET", "/tasks?on_behalf_of=alice@acme.com", token, ""); s != http.StatusBadRequest {
		t.Fatalf("on_behalf_of on /tasks should be rejected (no activity log), got %d", s)
	}

	// Deals roll up too, and the cross-link works when an activity references the
	// deal by HANDLE (the natural agent input) - the create resolves it to the id.
	_, db := do(t, ts, "POST", "/deals", token, `{"title":"Acme renewal"}`)
	deal := firstHandleID(db, "deal/")
	if deal == "" {
		t.Fatalf("no deal id in %q", db)
	}
	do(t, ts, "POST", "/contacts/"+contact+"/activities", token,
		`{"kind":"email","body":"d","deal_id":"deal_`+deal+`","on_behalf_of":"alice@acme.com"}`)
	_, dealsForAlice := do(t, ts, "GET", "/deals?on_behalf_of=alice@acme.com", token, "")
	if !strings.Contains(dealsForAlice, "deal_"+deal) || !strings.Contains(dealsForAlice, "on_behalf_of=alice@acme.com") {
		t.Fatalf("deal linked by handle should roll up + filter by its principal:\n%s", dealsForAlice)
	}
}
