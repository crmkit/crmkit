package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crmkit/crmkit/internal/config"
)

// TestPlanQuota verifies per-plan create limits are enforced and surfaced.
func TestPlanQuota(t *testing.T) {
	st := newMigratedStore(t)
	cfg := config.Default()
	cfg.Server.Local = true // loginAs reads the echoed local code
	cfg.Plans.Catalogue["basic"] = config.PlanLimits{
		MaxWorkspaces: 1, MaxMembers: 1, MaxContacts: 2, MaxCompanies: -1, MaxDeals: -1,
	}
	ts := httptest.NewServer(New(cfg, st, memoryRL(t)).Handler())
	t.Cleanup(ts.Close)

	token, _ := loginAs(t, ts, "quota@example.com")

	// Two contacts allowed (no email → always create, not upsert).
	for i := 0; i < 2; i++ {
		if status, body := do(t, ts, "POST", "/contacts", token, fmt.Sprintf(`{"name":"C%d"}`, i)); status != http.StatusCreated {
			t.Fatalf("contact %d should be created: %d %q", i, status, body)
		}
	}
	// Third exceeds max_contacts=2.
	if status, body := do(t, ts, "POST", "/contacts", token, `{"name":"C3"}`); status != http.StatusForbidden || !strings.Contains(body, "plan_limit_reached") {
		t.Fatalf("3rd contact should be plan-limited: %d %q", status, body)
	}
	// Companies are unlimited (-1) on this plan.
	if status, body := do(t, ts, "POST", "/companies", token, `{"name":"Acme"}`); status != http.StatusCreated {
		t.Fatalf("unlimited companies should be allowed: %d %q", status, body)
	}
	// User already owns the default workspace; MaxWorkspaces=1 → reject a second.
	if status, body := do(t, ts, "POST", "/workspaces", token, `{"name":"Second"}`); status != http.StatusForbidden || !strings.Contains(body, "plan_limit_reached") {
		t.Fatalf("2nd workspace should be plan-limited: %d %q", status, body)
	}
	// whoami surfaces plan + usage.
	if status, body := do(t, ts, "GET", "/whoami", token, ""); status != 200 || !strings.Contains(body, "plan:") || !strings.Contains(body, "contacts:") {
		t.Fatalf("whoami should show plan + usage: %d %q", status, body)
	}
}

// TestActivityQuota verifies the activity backstop is a single per-workspace cap,
// shared across contact and company activities.
func TestActivityQuota(t *testing.T) {
	st := newMigratedStore(t)
	cfg := config.Default()
	cfg.Server.Local = true
	cfg.Plans.Catalogue["basic"] = config.PlanLimits{
		MaxWorkspaces: 1, MaxMembers: 1, MaxContacts: -1, MaxCompanies: -1, MaxDeals: -1, MaxActivities: 1,
	}
	ts := httptest.NewServer(New(cfg, st, memoryRL(t)).Handler())
	t.Cleanup(ts.Close)

	token, _ := loginAs(t, ts, "act@example.com")
	_, b := do(t, ts, "POST", "/contacts", token, `{"name":"Jane"}`)
	contactID := firstHandleID(b, "contact/")
	_, cb := do(t, ts, "POST", "/companies", token, `{"name":"Acme"}`)
	companyID := firstHandleID(cb, "company/")

	// First activity allowed.
	_, ab := do(t, ts, "POST", "/contacts/"+contactID+"/activities", token, `{"kind":"note","body":"one"}`)
	actID := firstHandleID(ab, "activity/")
	if actID == "" {
		t.Fatalf("no activity id: %q", ab)
	}
	// Second contact activity exceeds max_activities=1.
	if s, body := do(t, ts, "POST", "/contacts/"+contactID+"/activities", token, `{"body":"two"}`); s != http.StatusForbidden || !strings.Contains(body, "plan_limit_reached") {
		t.Fatalf("2nd activity should be plan-limited: %d %q", s, body)
	}
	// A company activity counts against the SAME workspace cap, so it's blocked too.
	if s, body := do(t, ts, "POST", "/companies/"+companyID+"/activities", token, `{"body":"three"}`); s != http.StatusForbidden || !strings.Contains(body, "plan_limit_reached") {
		t.Fatalf("company activity should share the workspace activity cap: %d %q", s, body)
	}

	// Deleting an activity frees room under the cap - the hard block is recoverable.
	if s, _ := do(t, ts, "DELETE", "/activities/"+actID, token, ""); s != http.StatusOK {
		t.Fatalf("delete activity should succeed: %d", s)
	}
	if s, body := do(t, ts, "POST", "/companies/"+companyID+"/activities", token, `{"body":"now there is room"}`); s != http.StatusCreated {
		t.Fatalf("after deleting, a new activity should be allowed: %d %q", s, body)
	}
}
