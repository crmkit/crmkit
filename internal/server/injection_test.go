package server

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/crmkit/crmkit/internal/config"
	"github.com/crmkit/crmkit/internal/store"
)

// injectionServer serves the given store with a high rate limit and unlimited
// plan limits, because the battery fires a large number of requests in one go.
func injectionServer(t *testing.T, st store.Store) *httptest.Server {
	t.Helper()
	cfg := config.Default()
	cfg.Server.Local = true
	cfg.RateLimit.RPS = 100000
	cfg.RateLimit.Burst = 100000
	if pl, ok := cfg.Plans.Catalogue[cfg.Plans.Default]; ok {
		pl.MaxWorkspaces, pl.MaxMembers, pl.MaxContacts, pl.MaxCompanies, pl.MaxDeals = limitPtr(-1), limitPtr(-1), limitPtr(-1), limitPtr(-1), limitPtr(-1)
		cfg.Plans.Catalogue[cfg.Plans.Default] = pl
	}
	ts := httptest.NewServer(New(cfg, st, memoryRL(t)).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func newInjectionServer(t *testing.T) *httptest.Server {
	return injectionServer(t, newMigratedStore(t))
}

// These tests prove that user-controlled input reaching the query layer cannot
// inject SQL: values are bound parameters, and identifiers (filter fields, sort
// columns, custom keys, the cursor's sort column) are whitelist-validated. The
// strongest signal is the capstone: after a battery of injection attempts the
// database is still fully intact (no DROP/DELETE executed) - which can only be
// true if every payload was treated as data, not SQL.

// classic SQL-injection payloads, applied across every input vector below.
var injectionPayloads = []string{
	`' OR '1'='1`,
	`'; DROP TABLE contacts; --`,
	`'; DROP TABLE companies; --`,
	`1); DELETE FROM contacts WHERE 1=1; --`,
	`" OR ""="`,
	`') OR ('1'='1`,
	`x'); DROP TABLE deals;--`,
	`%' OR 1=1 --`,
}

// TestNoSQLInjection runs the battery on SQLite (the default backend).
func TestNoSQLInjection(t *testing.T) {
	ts := newInjectionServer(t)
	runInjectionBattery(t, ts, authenticate(t, ts))
}

// TestNoSQLInjectionPostgres runs the SAME battery against a real Postgres,
// covering the other dialect ($N placeholders, ILIKE, custom::jsonb ->>). Gated
// on CRMKIT_TEST_POSTGRES_DSN.
func TestNoSQLInjectionPostgres(t *testing.T) {
	dsn := os.Getenv("CRMKIT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CRMKIT_TEST_POSTGRES_DSN to run the Postgres injection battery")
	}
	st, err := store.Open("postgres", dsn, store.Options{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.ApplyMigrations(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ts := injectionServer(t, st)
	runInjectionBattery(t, ts, authenticate(t, ts))
}

func runInjectionBattery(t *testing.T, ts *httptest.Server, token string) {
	t.Helper()

	// Seed known, distinct rows in every entity.
	if s, b := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Seed","email":"jane@seed.test","stage":"lead","tags":["vip"],"custom":{"region":"emea"}}`); s != http.StatusCreated {
		t.Fatalf("seed contact: %d %q", s, b)
	}
	if s, _ := do(t, ts, "POST", "/contacts", token, `{"name":"Bob Seed","email":"bob@seed.test","stage":"won"}`); s != http.StatusCreated {
		t.Fatalf("seed contact 2")
	}
	if s, _ := do(t, ts, "POST", "/companies", token, `{"name":"Acme Seed","domain":"acme.seed","tags":["competitor"],"custom":{"region":"emea"}}`); s != http.StatusCreated {
		t.Fatalf("seed company")
	}
	if s, _ := do(t, ts, "POST", "/deals", token, `{"title":"Deal Seed","custom":{"source":"referral"}}`); s != http.StatusCreated {
		t.Fatalf("seed deal")
	}

	get := func(path string) (int, string) { return do(t, ts, "GET", path, token, "") }
	enc := url.QueryEscape

	for _, p := range injectionPayloads {
		// ---- bound VALUES: a filter value, search, tag, custom value. These are
		// bound parameters, so the payload matches literally (i.e. nothing) and
		// never executes. The key assertion: the unrelated row "Bob" must NOT
		// appear (an `OR 1=1` injection would return every contact).
		if _, b := get("/contacts?stage=" + enc(p)); strings.Contains(b, "Bob Seed") {
			t.Fatalf("stage filter injected (over-matched) with payload %q:\n%s", p, b)
		}
		if _, b := get("/contacts?search=" + enc(p)); strings.Contains(b, "Bob Seed") {
			t.Fatalf("search injected with payload %q:\n%s", p, b)
		}
		get("/contacts?tags=" + enc(p))               // bound; must not error/inject
		get("/companies?tags=" + enc(p))              //
		get("/companies?custom.region=" + enc(p))     // bound custom value
		get("/deals?custom.source=" + enc(p))         //
		get("/contacts?email=like:" + enc(p))         // bound LIKE value
		get("/contacts?stage=in:" + enc(p) + ",lead") // bound IN value
		get("/activities?contact=" + enc(p))          // bound (separate SQL builder)
		get("/activities?deal=" + enc(p))             //
		get("/audit?by=" + enc(p))                    // bound actor-email filter

		// ---- IDENTIFIERS must be whitelist-rejected, never interpolated:
		// an unknown filter field, a non-whitelisted sort column, a malformed
		// custom key. Each is a 400, not a 500 and not a silent execution.
		if s, b := get("/contacts?" + enc(p) + "=x"); s != http.StatusBadRequest || !strings.Contains(b, "invalid_filter") {
			t.Fatalf("filter field %q should be rejected, got %d %q", p, s, b)
		}
		if s, b := get("/contacts?sort=" + enc(p)); s != http.StatusBadRequest || !strings.Contains(b, "invalid_sort") {
			t.Fatalf("sort column %q should be rejected, got %d %q", p, s, b)
		}
		if s, b := get("/contacts?custom." + enc(p) + "=y"); s != http.StatusBadRequest || !strings.Contains(b, "invalid_filter") {
			t.Fatalf("custom key %q should be rejected, got %d %q", p, s, b)
		}

		// ---- the cursor's sort column is interpolated into ORDER BY, so a forged
		// cursor carrying a malicious column must be rejected.
		if s, b := get("/contacts?cursor=" + forgeCursor(p)); s != http.StatusBadRequest || !strings.Contains(b, "invalid_cursor") {
			t.Fatalf("forged cursor column %q should be rejected, got %d %q", p, s, b)
		}
	}

	// ---- capstone: nothing was dropped or deleted; every seed row survives.
	if s, b := get("/contacts"); s != 200 || !strings.Contains(b, "Jane Seed") || !strings.Contains(b, "Bob Seed") {
		t.Fatalf("contacts table not intact after injection battery: %d %q", s, b)
	}
	if s, b := get("/companies"); s != 200 || !strings.Contains(b, "Acme Seed") {
		t.Fatalf("companies table not intact: %d %q", s, b)
	}
	if s, b := get("/deals"); s != 200 || !strings.Contains(b, "Deal Seed") {
		t.Fatalf("deals table not intact: %d %q", s, b)
	}
	// The activities and audit_log tables also survive (their query builders bind
	// the filter values).
	if s, _ := get("/activities"); s != 200 {
		t.Fatalf("activities table not intact: %d", s)
	}
	if s, _ := get("/audit"); s != 200 {
		t.Fatalf("audit_log table not intact: %d", s)
	}
}

// forgeCursor builds a tampered, unsigned pagination cursor whose sort column is
// attacker-controlled (the real shape a client could craft).
func forgeCursor(col string) string {
	raw := `{"c":` + strconv.Quote(col) + `,"d":false,"n":false,"v":"x","i":"y"}`
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// A forged cursor with a LEGITIMATE column is still accepted (the validation is
// targeted at the column identifier, not at rejecting all cursors).
func TestForgedCursorWithValidColumnAccepted(t *testing.T) {
	ts := newInjectionServer(t)
	token := authenticate(t, ts)
	if s, _ := do(t, ts, "GET", "/contacts?cursor="+forgeCursor("updated_at"), token, ""); s != 200 {
		t.Fatalf("a cursor with a valid sort column should be accepted, got %d", s)
	}
}
