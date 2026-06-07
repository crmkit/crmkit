package server

import (
	"strings"
	"testing"
)

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
