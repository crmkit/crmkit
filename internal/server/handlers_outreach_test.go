package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestOutreachFilter verifies the activity-log-derived outreach filters on
// contacts: a contact counts as "contacted" only once it carries an outreach-kind
// activity (call/email/meeting). So last_outreach=is:null finds the never-reached,
// outreach_count=gte:1 finds the reached, and a logged note does NOT count as
// outreach (the whole point - this is what stops agents reaching for a tag).
func TestOutreachFilter(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	mk := func(name string) string {
		_, b := do(t, ts, "POST", "/contacts", token, `{"name":"`+name+`"}`)
		id := firstHandleID(b, "contact/")
		if id == "" {
			t.Fatalf("no contact id in %q", b)
		}
		return id
	}
	alice := mk("Alice") // emailed -> outreach
	bob := mk("Bob")     // only a note -> not outreach
	carol := mk("Carol") // never touched

	if s, _ := do(t, ts, "POST", "/contacts/"+alice+"/activities", token, `{"kind":"email","body":"intro"}`); s != http.StatusCreated {
		t.Fatalf("log email on alice: %d", s)
	}
	if s, _ := do(t, ts, "POST", "/contacts/"+bob+"/activities", token, `{"kind":"note","body":"saw on LinkedIn"}`); s != http.StatusCreated {
		t.Fatalf("log note on bob: %d", s)
	}

	has := func(body, id string) bool { return strings.Contains(body, "contact_"+id) }

	// Never contacted: Bob (a note doesn't count) and Carol; not Alice.
	_, never := do(t, ts, "GET", "/contacts?last_outreach=is:null", token, "")
	if has(never, alice) {
		t.Fatalf("alice was emailed; she must not appear in last_outreach=is:null:\n%s", never)
	}
	if !has(never, bob) || !has(never, carol) {
		t.Fatalf("bob (note only) and carol (untouched) must appear in last_outreach=is:null:\n%s", never)
	}

	// Reached at least once: only Alice.
	_, reached := do(t, ts, "GET", "/contacts?outreach_count=gte:1", token, "")
	if !has(reached, alice) {
		t.Fatalf("alice was emailed; she must appear in outreach_count=gte:1:\n%s", reached)
	}
	if has(reached, bob) || has(reached, carol) {
		t.Fatalf("only alice was reached; bob/carol must not appear in outreach_count=gte:1:\n%s", reached)
	}
}

// TestOutreachListSignal verifies the list line shows the outreach figure agents
// filter on, and that it counts outreach kinds only: Alice (emailed) shows
// outreach=1, while Bob (a note) shows the all-kinds activities=1 but no outreach
// field - so what the agent sees matches what last_outreach/outreach_count filter.
func TestOutreachListSignal(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	mk := func(name string) string {
		_, b := do(t, ts, "POST", "/contacts", token, `{"name":"`+name+`"}`)
		id := firstHandleID(b, "contact/")
		if id == "" {
			t.Fatalf("no contact id in %q", b)
		}
		return id
	}
	alice, bob := mk("Alice"), mk("Bob")
	if s, _ := do(t, ts, "POST", "/contacts/"+alice+"/activities", token, `{"kind":"email","body":"intro"}`); s != http.StatusCreated {
		t.Fatalf("log email on alice: %d", s)
	}
	if s, _ := do(t, ts, "POST", "/contacts/"+bob+"/activities", token, `{"kind":"note","body":"saw on LinkedIn"}`); s != http.StatusCreated {
		t.Fatalf("log note on bob: %d", s)
	}

	lineFor := func(body, id string) string {
		for _, ln := range strings.Split(body, "\n") {
			if strings.Contains(ln, "contact_"+id) {
				return ln
			}
		}
		return ""
	}

	_, body := do(t, ts, "GET", "/contacts", token, "")

	if al := lineFor(body, alice); !strings.Contains(al, "outreach=1") || !strings.Contains(al, "last_outreach=") {
		t.Fatalf("alice's line should show outreach=1 and last_outreach= after an email:\n%s", al)
	}
	// Bob's note is activity, but not outreach: all-kinds signal yes, outreach no.
	bl := lineFor(body, bob)
	if !strings.Contains(bl, "activities=1") {
		t.Fatalf("bob's note should still show activities=1:\n%s", bl)
	}
	if strings.Contains(bl, "outreach=") {
		t.Fatalf("bob was only noted, not contacted; his line must carry no outreach field:\n%s", bl)
	}
}
