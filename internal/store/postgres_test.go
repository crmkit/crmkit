package store

import (
	"os"
	"testing"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// pgTestStore opens a Postgres-backed store for tests when CRMKIT_TEST_POSTGRES_DSN
// is set, and resets the schema so each run starts clean. Without the env var the
// Postgres integration tests are skipped (so `go test` stays hermetic by default).
func pgTestStore(t *testing.T) *sqlStore {
	t.Helper()
	dsn := os.Getenv("CRMKIT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CRMKIT_TEST_POSTGRES_DSN to run Postgres integration tests")
	}
	st, err := openPostgres(dsn, Options{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	// Open no longer creates schema; ensure it exists (idempotent) before reset.
	if _, err := st.ApplyMigrations(); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	// Start from a clean slate.
	for _, tbl := range []string{
		"oauth_refresh_tokens", "oauth_codes", "oauth_clients",
		"audit_log", "activities", "deals", "companies", "contacts",
		"tokens", "escalations", "otps", "invites", "memberships", "users", "workspaces",
	} {
		if _, err := st.db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("reset %s: %v", tbl, err)
		}
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestPostgresPoolConfig verifies the configurable pool size reaches the
// underlying *sql.DB.
func TestPostgresPoolConfig(t *testing.T) {
	dsn := os.Getenv("CRMKIT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CRMKIT_TEST_POSTGRES_DSN to run Postgres integration tests")
	}
	st, err := openPostgres(dsn, Options{MaxOpenConns: 7, MaxIdleConns: 3, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer st.Close()
	if got := st.db.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("expected MaxOpenConnections=7, got %d", got)
	}
}

// TestPostgresEndToEnd exercises the same behaviors as the SQLite suite against
// a real Postgres, proving the dialect layer (placeholder rebind, ILIKE, schema)
// is correct.
func TestPostgresEndToEnd(t *testing.T) {
	st := pgTestStore(t)

	// Identity + workspace provisioning + admin membership.
	admin, err := st.GetOrCreateIdentity("admin@acme.com")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	team := admin.DefaultWorkspaceID
	if role, _ := st.MemberRole(team, admin.ID); role != protocol.RoleAdmin {
		t.Fatalf("expected admin, got %q", role)
	}

	// Token round-trip + sliding expiry.
	st.SetTokenIdleTTL(time.Hour)
	if _, err := st.CreateToken(admin.ID, team, "default", "tokhash"); err != nil {
		t.Fatalf("create token: %v", err)
	}
	if sess, err := st.ResolveToken("tokhash"); err != nil || sess.WorkspaceID != team {
		t.Fatalf("resolve token: %v", err)
	}

	// Contacts: create + case-insensitive search (exercises ILIKE) + isolation.
	c := &protocol.Contact{Name: "Jane Doe", Email: "jane@ACME.com", Stage: "lead", Tags: []string{"vip"}, Custom: map[string]any{"src": "web"}}
	if err := st.CreateContact(team, c); err != nil {
		t.Fatalf("create contact: %v", err)
	}
	// lowercase search vs mixed-case data exercises ILIKE on Postgres.
	found, _, err := st.QueryContacts(team, Query{Search: "acme", SearchColumns: []string{"name", "email"}, SortColumn: "updated_at", SortDesc: true, SortNumeric: true, Limit: 10})
	if err != nil || len(found) != 1 {
		t.Fatalf("ILIKE search failed: err=%v n=%d", err, len(found))
	}
	if found[0].Custom["src"] != "web" || len(found[0].Tags) != 1 {
		t.Fatalf("json roundtrip mismatch: %+v", found[0])
	}

	// Invite -> join -> shared workspace visibility.
	if _, err := st.CreateInvite(team, "mate@acme.com", protocol.RoleMember, admin.ID); err != nil {
		t.Fatalf("invite: %v", err)
	}
	mate, _ := st.GetOrCreateIdentity("mate@acme.com")
	if role, err := st.MemberRole(team, mate.ID); err != nil || role != protocol.RoleMember {
		t.Fatalf("invite join failed: role=%q err=%v", role, err)
	}

	// Deals + pipeline filter.
	_ = st.CreateDeal(team, &protocol.Deal{Title: "Acme renewal", Stage: "proposal", AmountCents: 500000, Currency: "USD", ContactID: c.ID})
	open, _, err := st.QueryDeals(team, Query{
		Filters:    []QFilter{{Column: "status", Op: "=", Value: "open"}, {Column: "amount_cents", Op: ">=", Value: int64(100000)}},
		SortColumn: "amount_cents", SortDesc: true, SortNumeric: true, Limit: 10,
	})
	if err != nil || len(open) != 1 {
		t.Fatalf("deal filter: err=%v n=%d", err, len(open))
	}

	// Escalation single-use + scope.
	if err := st.PutEscalation(admin.ID, "workspace.delete", team, "h1", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("put escalation: %v", err)
	}
	if ok, _ := st.VerifyEscalation(admin.ID, "workspace.delete", team, "h1", time.Now()); !ok {
		t.Fatal("escalation should verify")
	}
	if ok, _ := st.VerifyEscalation(admin.ID, "workspace.delete", team, "h1", time.Now()); ok {
		t.Fatal("escalation should be single-use")
	}

	// Cascade delete + audit.
	_ = st.WriteAudit(team, "tok", "me@example.com", "contact.create", "contact/"+c.ID, "Jane Doe")
	if err := st.DeleteWorkspace(team); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	if _, err := st.GetContact(team, c.ID); err != ErrNotFound {
		t.Fatalf("expected cascade delete, got %v", err)
	}
}

// TestPostgresOAuth runs the MCP OAuth store layer against a real Postgres,
// proving the schema and the single-statement DELETE ... RETURNING consume path
// work under pgx (not just SQLite). Mirrors oauth_test.go.
func TestPostgresOAuth(t *testing.T) {
	st := pgTestStore(t)
	now := time.Now()

	// Client registration round-trip.
	clientID, err := st.RegisterOAuthClient([]string{"https://app.example/cb"}, "PG Client")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	c, err := st.GetOAuthClient(clientID)
	if err != nil || c.Name != "PG Client" || len(c.RedirectURIs) != 1 {
		t.Fatalf("get client: %v %+v", err, c)
	}
	if _, err := st.GetOAuthClient("mcpc_missing"); err != ErrNotFound {
		t.Fatalf("missing client should be ErrNotFound, got %v", err)
	}

	// Authorization code: single-use via DELETE ... RETURNING.
	code := AuthCode{ClientID: clientID, UserID: "u1", WorkspaceID: "w1", RedirectURI: "https://app.example/cb", CodeChallenge: "chal", Scope: "crm"}
	if err := st.PutAuthCode("ac-1", code, now.Add(time.Minute)); err != nil {
		t.Fatalf("put auth code: %v", err)
	}
	got, err := st.ConsumeAuthCode("ac-1", now)
	if err != nil || got.CodeChallenge != "chal" || got.WorkspaceID != "w1" {
		t.Fatalf("consume auth code: %v %+v", err, got)
	}
	if _, err := st.ConsumeAuthCode("ac-1", now); err != ErrNotFound {
		t.Fatalf("auth code should be single-use, got %v", err)
	}
	if err := st.PutAuthCode("ac-exp", code, now.Add(-time.Second)); err != nil {
		t.Fatalf("put expired: %v", err)
	}
	if _, err := st.ConsumeAuthCode("ac-exp", now); err != ErrNotFound {
		t.Fatalf("expired auth code should be ErrNotFound, got %v", err)
	}

	// Refresh token: rotation single-use + paired access hash.
	rg := RefreshGrant{ClientID: clientID, UserID: "u1", WorkspaceID: "w1", Scope: "crm", AccessTokenHash: "acc-hash"}
	if err := st.PutRefreshToken("rt-1", rg, now.Add(time.Hour)); err != nil {
		t.Fatalf("put refresh: %v", err)
	}
	rgot, err := st.ConsumeRefreshToken("rt-1", now)
	if err != nil || rgot.AccessTokenHash != "acc-hash" {
		t.Fatalf("consume refresh: %v %+v", err, rgot)
	}
	if _, err := st.ConsumeRefreshToken("rt-1", now); err != ErrNotFound {
		t.Fatalf("refresh should be single-use, got %v", err)
	}

	// Revoke-by-hash for a real access token row, and for a refresh token.
	user, _ := st.GetOrCreateIdentity("pgoauth@acme.com")
	if _, err := st.CreateToken(user.ID, user.DefaultWorkspaceID, "t", "tok-hash"); err != nil {
		t.Fatalf("create token: %v", err)
	}
	if err := st.RevokeTokenByHash("tok-hash"); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if _, err := st.ResolveToken("tok-hash"); err != ErrNotFound {
		t.Fatalf("revoked token should not resolve, got %v", err)
	}
	if err := st.PutRefreshToken("rt-x", rg, now.Add(time.Hour)); err != nil {
		t.Fatalf("put refresh x: %v", err)
	}
	if err := st.RevokeRefreshTokenByHash("rt-x"); err != nil {
		t.Fatalf("revoke refresh: %v", err)
	}
	if _, err := st.ConsumeRefreshToken("rt-x", now); err != ErrNotFound {
		t.Fatalf("revoked refresh should be gone, got %v", err)
	}
}
