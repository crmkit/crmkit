package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestDealActivities verifies the direct deal activity endpoints: you can log an
// activity straight onto a deal (no cross-link gymnastics) and read its log back,
// and the on_behalf_of principal rolls up to the deal line/filter.
func TestDealActivities(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, db := do(t, ts, "POST", "/deals", token, `{"title":"Acme renewal"}`)
	deal := firstHandleID(db, "deal/")
	if deal == "" {
		t.Fatalf("no deal id in %q", db)
	}

	// Log straight onto the deal.
	if s, _ := do(t, ts, "POST", "/deals/"+deal+"/activities", token, `{"kind":"meeting","body":"pricing walkthrough","on_behalf_of":"alice@acme.com"}`); s != http.StatusCreated {
		t.Fatalf("log deal activity: %d", s)
	}

	// Read the deal's activity log back.
	_, log := do(t, ts, "GET", "/deals/"+deal+"/activities", token, "")
	if !strings.Contains(log, "pricing walkthrough") || !strings.Contains(log, "on_behalf_of=alice@acme.com") {
		t.Fatalf("deal activity log should show the entry + principal:\n%s", log)
	}

	// And it rolls up to the deal line / filter.
	_, deals := do(t, ts, "GET", "/deals?on_behalf_of=alice@acme.com", token, "")
	if !strings.Contains(deals, "deal_"+deal) {
		t.Fatalf("deal should be filterable by the principal of its own activity:\n%s", deals)
	}
}
