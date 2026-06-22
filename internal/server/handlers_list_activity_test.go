package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestListShowsActivitySignal verifies a list line carries the activity count and
// recency once activity is logged, so a caller can see which records are active
// across a whole page without a per-record fetch - and that a quiet record adds
// nothing to its line.
func TestListShowsActivitySignal(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, body := do(t, ts, "POST", "/companies", token, `{"name":"Acme","domain":"acme.com"}`)
	id := firstHandleID(body, "company/")
	if id == "" {
		t.Fatalf("no company id in %q", body)
	}

	// Before any activity, the list line carries no activity signal at all.
	if _, b := do(t, ts, "GET", "/companies", token, ""); strings.Contains(b, "activities=") {
		t.Fatalf("a quiet company should add no activity field to its list line:\n%s", b)
	}

	if s, _ := do(t, ts, "POST", "/companies/"+id+"/activities", token, `{"kind":"note","body":"kickoff call"}`); s != http.StatusCreated {
		t.Fatalf("log activity: %d", s)
	}

	// Now the list line shows the count + recency, computed in one batched query.
	_, b := do(t, ts, "GET", "/companies", token, "")
	if !strings.Contains(b, "activities=1") || !strings.Contains(b, "last_activity=") {
		t.Fatalf("company list line should show activities=1 and last_activity= after logging:\n%s", b)
	}
}

// TestActivitiesMultiIDFilter verifies the feed filter accepts several
// comma-separated handles, so one call pulls the activity for a list of records
// (matching any of them) and excludes the ones not asked for.
func TestActivitiesMultiIDFilter(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	mk := func(name string) string {
		_, b := do(t, ts, "POST", "/companies", token, `{"name":"`+name+`"}`)
		id := firstHandleID(b, "company/")
		if id == "" {
			t.Fatalf("no company id in %q", b)
		}
		return id
	}
	alpha, bravo, charlie := mk("Alpha"), mk("Bravo"), mk("Charlie")

	for id, note := range map[string]string{alpha: "alpha-note", bravo: "bravo-note", charlie: "charlie-note"} {
		if s, _ := do(t, ts, "POST", "/companies/"+id+"/activities", token, `{"kind":"note","body":"`+note+`"}`); s != http.StatusCreated {
			t.Fatalf("log activity on %s: %d", id, s)
		}
	}

	// One call for two of the three companies.
	_, body := do(t, ts, "GET", "/activities?company="+alpha+","+bravo, token, "")
	if !strings.Contains(body, "alpha-note") || !strings.Contains(body, "bravo-note") {
		t.Fatalf("multi-id filter should include both requested companies:\n%s", body)
	}
	if strings.Contains(body, "charlie-note") {
		t.Fatalf("multi-id filter should exclude the company not requested:\n%s", body)
	}
}
