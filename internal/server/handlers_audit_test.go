package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuditAttributesActor(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts) // me@example.com

	if status, _ := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe"}`); status != http.StatusCreated {
		t.Fatalf("create contact: %d", status)
	}

	// The audit feed names who acted.
	status, body := do(t, ts, "GET", "/audit", token, "")
	if status != http.StatusOK {
		t.Fatalf("audit: %d %q", status, body)
	}
	if !strings.Contains(body, "by=me@example.com") || !strings.Contains(body, "action=contact.create") {
		t.Fatalf("audit should attribute the actor:\n%s", body)
	}

	// Filtering by the acting member returns the entry...
	if _, body := do(t, ts, "GET", "/audit?by=me@example.com", token, ""); !strings.Contains(body, "action=contact.create") {
		t.Fatalf("?by=actor should include their actions:\n%s", body)
	}
	// ...and filtering by someone else excludes it.
	if _, body := do(t, ts, "GET", "/audit?by=someone@else.com", token, ""); strings.Contains(body, "action=contact.create") {
		t.Fatalf("?by=other should exclude this actor's actions:\n%s", body)
	}
}

func TestActivityShowsActor(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts) // me@example.com

	_, body := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe"}`)
	id := firstHandleID(body, "contact/")
	if id == "" {
		t.Fatalf("no contact id in %q", body)
	}

	if status, _ := do(t, ts, "POST", "/contacts/"+id+"/activities", token, `{"kind":"note","body":"called her"}`); status != http.StatusCreated {
		t.Fatalf("create activity: %d", status)
	}

	_, list := do(t, ts, "GET", "/contacts/"+id+"/activities", token, "")
	if !strings.Contains(list, "by=me@example.com") {
		t.Fatalf("activity line should attribute the actor:\n%s", list)
	}
}

// TestAuditRecordsDiffAndFiltersByTarget covers the record-history role of the
// audit log: an update records what changed in the detail, and ?target= scopes
// the log to a single record.
func TestAuditRecordsDiffAndFiltersByTarget(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// Two contacts; edit each so both have update history.
	_, a := do(t, ts, "POST", "/contacts", token, `{"name":"Jane","stage":"lead"}`)
	ja := firstHandleID(a, "contact/")
	_, b := do(t, ts, "POST", "/contacts", token, `{"name":"Bob","stage":"lead"}`)
	jb := firstHandleID(b, "contact/")

	do(t, ts, "PATCH", "/contacts/"+ja, token, `{"stage":"customer","owner":"alice"}`)
	do(t, ts, "PATCH", "/contacts/"+jb, token, `{"stage":"churned"}`)

	// ?target= scopes the audit to Jane: her change is present...
	_, hist := do(t, ts, "GET", "/audit?target=contact_"+ja, token, "")
	if !strings.Contains(hist, "stage: lead -> customer") || !strings.Contains(hist, "owner: (none) -> alice") {
		t.Fatalf("audit should record the field diff for Jane:\n%s", hist)
	}
	// ...and Bob's change is NOT (it's a different record).
	if strings.Contains(hist, "churned") {
		t.Fatalf("?target= must not leak another record's history:\n%s", hist)
	}
	// The create is in Jane's history too (same target).
	if !strings.Contains(hist, "action=contact.create") {
		t.Fatalf("record history should include the create:\n%s", hist)
	}
}
