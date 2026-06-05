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
