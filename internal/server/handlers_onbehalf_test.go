package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestActivityOnBehalfOf verifies the on_behalf_of principal: an activity records
// who an agent acted for (separate from by=, the actor that wrote it), it shows on
// the line, and the activities feed filters to one principal case-insensitively.
func TestActivityOnBehalfOf(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, b := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe","email":"jane@acme.com"}`)
	contact := firstHandleID(b, "contact/")
	if contact == "" {
		t.Fatalf("no contact id in %q", b)
	}

	// One interaction performed for Alice, one for Bob.
	if s, _ := do(t, ts, "POST", "/contacts/"+contact+"/activities", token, `{"kind":"email","body":"sent intro","on_behalf_of":"alice@acme.com"}`); s != http.StatusCreated {
		t.Fatalf("log activity for alice: %d", s)
	}
	if s, _ := do(t, ts, "POST", "/contacts/"+contact+"/activities", token, `{"kind":"call","body":"left a voicemail","on_behalf_of":"bob@acme.com"}`); s != http.StatusCreated {
		t.Fatalf("log activity for bob: %d", s)
	}

	// The line carries the principal, distinct from by= (the actor/token email).
	_, all := do(t, ts, "GET", "/contacts/"+contact+"/activities", token, "")
	if !strings.Contains(all, "on_behalf_of=alice@acme.com") || !strings.Contains(all, "on_behalf_of=bob@acme.com") {
		t.Fatalf("activity lines should show on_behalf_of for both principals:\n%s", all)
	}

	// Filter the feed to one principal - case-insensitively (ALICE matches alice).
	_, forAlice := do(t, ts, "GET", "/activities?contact="+contact+"&on_behalf_of=ALICE@acme.com", token, "")
	if !strings.Contains(forAlice, "sent intro") {
		t.Fatalf("on_behalf_of filter should include alice's activity (case-insensitive):\n%s", forAlice)
	}
	if strings.Contains(forAlice, "left a voicemail") {
		t.Fatalf("on_behalf_of=alice should exclude bob's activity:\n%s", forAlice)
	}
}
