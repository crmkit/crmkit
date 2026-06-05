package store

import (
	"testing"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

func newTestStore(t *testing.T) *sqlStore {
	t.Helper()
	st, err := openSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	// Open no longer creates schema; tests run the migrations explicitly.
	if _, err := st.ApplyMigrations(); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return st
}

func TestOTPLifecycle(t *testing.T) {
	st := newTestStore(t)
	email := "user@example.com"

	if err := st.PutOTP(email, "hash-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("put otp: %v", err)
	}

	// Wrong code does not verify.
	if ok, _ := st.VerifyOTP(email, "wrong", time.Now()); ok {
		t.Fatal("wrong code should not verify")
	}
	// Correct code verifies and is then consumed.
	if ok, _ := st.VerifyOTP(email, "hash-1", time.Now()); !ok {
		t.Fatal("correct code should verify")
	}
	if ok, _ := st.VerifyOTP(email, "hash-1", time.Now()); ok {
		t.Fatal("code should be single-use")
	}
}

func TestIdentityAndTokenProvisioning(t *testing.T) {
	st := newTestStore(t)
	user, err := st.GetOrCreateIdentity("a@b.com")
	if err != nil {
		t.Fatalf("get or create identity: %v", err)
	}
	if user.DefaultWorkspaceID == "" {
		t.Fatal("expected a default workspace")
	}
	// First login makes the user an owner of their default workspace.
	if role, err := st.MemberRole(user.DefaultWorkspaceID, user.ID); err != nil || role != protocol.RoleAdmin {
		t.Fatalf("expected owner membership, got role=%q err=%v", role, err)
	}
	// Same email returns the same identity.
	user2, _ := st.GetOrCreateIdentity("a@b.com")
	if user2.ID != user.ID {
		t.Fatal("expected stable user for same email")
	}

	tokenID, err := st.CreateToken(user.ID, user.DefaultWorkspaceID, "default", "tokhash")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	sess, err := st.ResolveToken("tokhash")
	if err != nil {
		t.Fatalf("resolve token: %v", err)
	}
	if sess.WorkspaceID != user.DefaultWorkspaceID || sess.TokenID != tokenID {
		t.Fatal("resolved session mismatch")
	}

	if err := st.RevokeToken(user.DefaultWorkspaceID, tokenID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := st.ResolveToken("tokhash"); err == nil {
		t.Fatal("revoked token should not resolve")
	}
}

func TestTokenSlidingExpiry(t *testing.T) {
	st := newTestStore(t)
	st.SetTokenIdleTTL(time.Hour)
	user, _ := st.GetOrCreateIdentity("a@b.com")

	if _, err := st.CreateToken(user.ID, user.DefaultWorkspaceID, "default", "th"); err != nil {
		t.Fatalf("create token: %v", err)
	}
	// Fresh token resolves.
	if _, err := st.ResolveToken("th"); err != nil {
		t.Fatalf("fresh token should resolve: %v", err)
	}

	// Simulate the token going idle past the TTL by backdating last_used_at.
	stale := time.Now().Add(-2 * time.Hour).Unix()
	if _, err := st.db.Exec(`UPDATE tokens SET last_used_at = ? WHERE token_hash = ?`, stale, "th"); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, err := st.ResolveToken("th"); err != ErrTokenExpired {
		t.Fatalf("idle token should be expired, got %v", err)
	}

	// With expiry disabled, the same stale token resolves again.
	st.SetTokenIdleTTL(0)
	if _, err := st.db.Exec(`UPDATE tokens SET last_used_at = ? WHERE token_hash = ?`, stale, "th"); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, err := st.ResolveToken("th"); err != nil {
		t.Fatalf("expiry disabled should resolve: %v", err)
	}
}

func TestInviteJoinAndIsolation(t *testing.T) {
	st := newTestStore(t)
	owner, _ := st.GetOrCreateIdentity("owner@acme.com")
	team := owner.DefaultWorkspaceID

	// Invite a teammate who hasn't signed up yet.
	if _, err := st.CreateInvite(team, "mate@acme.com", protocol.RoleMember, owner.ID); err != nil {
		t.Fatalf("invite: %v", err)
	}

	// They authenticate -> invite is consumed into a membership.
	mate, _ := st.GetOrCreateIdentity("mate@acme.com")
	if role, err := st.MemberRole(team, mate.ID); err != nil || role != protocol.RoleMember {
		t.Fatalf("expected member of invited workspace, got role=%q err=%v", role, err)
	}

	// A token the mate mints for the shared workspace resolves.
	if _, err := st.CreateToken(mate.ID, team, "mate-token", "mh"); err != nil {
		t.Fatalf("mate token: %v", err)
	}
	if sess, err := st.ResolveToken("mh"); err != nil || sess.WorkspaceID != team {
		t.Fatalf("mate token should resolve to shared workspace: %v", err)
	}

	// The mate belongs to two workspaces: their own + the shared one.
	wss, _ := st.ListWorkspacesForUser(mate.ID)
	if len(wss) != 2 {
		t.Fatalf("expected 2 workspaces for mate, got %d", len(wss))
	}

	// Removing the mate instantly invalidates their token for that workspace.
	if err := st.RemoveMember(team, mate.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if _, err := st.ResolveToken("mh"); err == nil {
		t.Fatal("token should stop resolving once membership is removed")
	}

	// Cannot remove the last owner.
	if err := st.RemoveMember(team, owner.ID); err != ErrLastAdmin {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}
}

func TestEscalationLifecycle(t *testing.T) {
	st := newTestStore(t)
	user, _ := st.GetOrCreateIdentity("a@b.com")

	if err := st.PutEscalation(user.ID, "workspace.delete", "ws_x", "hash-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("put escalation: %v", err)
	}
	// A code issued for one action/target must not verify a different one.
	if ok, _ := st.VerifyEscalation(user.ID, "member.promote", "ws_x", "hash-1", time.Now()); ok {
		t.Fatal("escalation must be scoped to its action")
	}
	// Wrong code fails; correct code verifies once then is consumed.
	if ok, _ := st.VerifyEscalation(user.ID, "workspace.delete", "ws_x", "nope", time.Now()); ok {
		t.Fatal("wrong code should not verify")
	}
	if ok, _ := st.VerifyEscalation(user.ID, "workspace.delete", "ws_x", "hash-1", time.Now()); !ok {
		t.Fatal("correct code should verify")
	}
	if ok, _ := st.VerifyEscalation(user.ID, "workspace.delete", "ws_x", "hash-1", time.Now()); ok {
		t.Fatal("escalation should be single-use")
	}
}

func TestRoleChangeAndWorkspaceDeletion(t *testing.T) {
	st := newTestStore(t)
	admin, _ := st.GetOrCreateIdentity("admin@acme.com")
	team := admin.DefaultWorkspaceID

	// Invite + join a member.
	_, _ = st.CreateInvite(team, "mate@acme.com", protocol.RoleMember, admin.ID)
	mate, _ := st.GetOrCreateIdentity("mate@acme.com")

	// Cannot demote the only admin.
	if err := st.SetMemberRole(team, admin.ID, protocol.RoleMember); err != ErrLastAdmin {
		t.Fatalf("expected ErrLastAdmin demoting sole admin, got %v", err)
	}
	// Promote the member to admin, then demotion of the original is allowed.
	if err := st.SetMemberRole(team, mate.ID, protocol.RoleAdmin); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if role, _ := st.MemberRole(team, mate.ID); role != protocol.RoleAdmin {
		t.Fatalf("expected mate to be admin, got %q", role)
	}

	// Put some data, then delete the whole workspace.
	_ = st.CreateContact(team, &protocol.Contact{Name: "Doomed"})
	if err := st.DeleteWorkspace(team); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	if _, err := st.MemberRole(team, admin.ID); err != ErrNotFound {
		t.Fatalf("memberships should be gone, got %v", err)
	}

	// The admin's default workspace was cleared; next login self-heals it.
	healed, _ := st.GetOrCreateIdentity("admin@acme.com")
	if healed.DefaultWorkspaceID == "" || healed.DefaultWorkspaceID == team {
		t.Fatalf("expected a fresh default workspace, got %q", healed.DefaultWorkspaceID)
	}
	if role, err := st.MemberRole(healed.DefaultWorkspaceID, healed.ID); err != nil || role != protocol.RoleAdmin {
		t.Fatalf("expected admin of healed workspace, got role=%q err=%v", role, err)
	}
}

func TestContactCRUDAndIsolation(t *testing.T) {
	st := newTestStore(t)
	userA, _ := st.GetOrCreateIdentity("a@b.com")
	userB, _ := st.GetOrCreateIdentity("c@d.com")
	wsA := userA.DefaultWorkspaceID
	wsB := userB.DefaultWorkspaceID

	c := &protocol.Contact{Name: "Jane", Email: "jane@acme.com", Stage: "lead", Tags: []string{"vip"}, Custom: map[string]any{"src": "web"}}
	if err := st.CreateContact(wsA, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.ID == "" {
		t.Fatal("expected generated id")
	}

	got, err := st.GetContact(wsA, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Jane" || got.Custom["src"] != "web" || len(got.Tags) != 1 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	// Tenant isolation: workspace B cannot see workspace A's contact.
	if _, err := st.GetContact(wsB, c.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound across workspaces, got %v", err)
	}

	// Search via the query layer.
	list, _, err := st.QueryContacts(wsA, Query{Search: "acme", SearchColumns: []string{"name", "email"}, SortColumn: "updated_at", SortDesc: true, SortNumeric: true, Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("search: err=%v n=%d", err, len(list))
	}

	// Update.
	got.Stage = "qualified"
	if err := st.UpdateContact(wsA, &got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reread, _ := st.GetContact(wsA, c.ID)
	if reread.Stage != "qualified" {
		t.Fatalf("update not persisted: %q", reread.Stage)
	}

	// Delete.
	if err := st.DeleteContact(wsA, c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetContact(wsA, c.ID); err != ErrNotFound {
		t.Fatalf("expected deleted, got %v", err)
	}
}

func TestReminders(t *testing.T) {
	st := newTestStore(t)
	user, _ := st.GetOrCreateIdentity("a@b.com")
	ws := user.DefaultWorkspaceID

	past := time.Now().Add(-time.Hour)
	soon := time.Now().Add(48 * time.Hour)
	overdue := &protocol.Contact{Name: "Overdue Co", FollowUpAt: &past, FollowUpNote: "ping"}
	if err := st.CreateContact(ws, overdue); err != nil {
		t.Fatalf("create contact: %v", err)
	}
	_ = st.CreateDeal(ws, &protocol.Deal{Title: "Future deal", FollowUpAt: &soon})
	_ = st.CreateContact(ws, &protocol.Contact{Name: "No followup"}) // no follow_up_at

	// Due now: only the overdue contact.
	now, err := st.ListReminders(ws, time.Now(), 50)
	if err != nil {
		t.Fatalf("list reminders: %v", err)
	}
	if len(now) != 1 || now[0].Kind != protocol.KindContact || !now[0].Overdue || now[0].Note != "ping" {
		t.Fatalf("expected 1 overdue contact, got %+v", now)
	}

	// Look ahead a week: both, soonest first.
	ahead, _ := st.ListReminders(ws, time.Now().Add(7*24*time.Hour), 50)
	if len(ahead) != 2 {
		t.Fatalf("expected 2 reminders within a week, got %d", len(ahead))
	}
	if !ahead[0].FollowUpAt.Before(ahead[1].FollowUpAt) {
		t.Fatal("reminders should be sorted soonest first")
	}

	// follow_up persists and clears.
	got, _ := st.GetContact(ws, overdue.ID)
	if got.FollowUpAt == nil {
		t.Fatal("follow_up_at should persist")
	}
	got.FollowUpAt = nil
	got.FollowUpNote = ""
	_ = st.UpdateContact(ws, &got)
	if rec, _ := st.GetContact(ws, overdue.ID); rec.FollowUpAt != nil {
		t.Fatal("follow_up_at should clear to nil")
	}
}

func TestFindContactByEmail(t *testing.T) {
	st := newTestStore(t)
	user, _ := st.GetOrCreateIdentity("a@b.com")
	ws := user.DefaultWorkspaceID

	_ = st.CreateContact(ws, &protocol.Contact{Name: "Jane", Email: "jane@acme.com"})

	// Case-insensitive match.
	got, err := st.FindContactByEmail(ws, "JANE@ACME.com")
	if err != nil || len(got) != 1 {
		t.Fatalf("expected 1 case-insensitive match, got n=%d err=%v", len(got), err)
	}
	// No match.
	if got, _ := st.FindContactByEmail(ws, "nobody@acme.com"); len(got) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(got))
	}
	// Ambiguous: two contacts with the same email (created directly).
	_ = st.CreateContact(ws, &protocol.Contact{Name: "Jane Two", Email: "jane@acme.com"})
	if got, _ := st.FindContactByEmail(ws, "jane@acme.com"); len(got) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(got))
	}
}

func TestDealFilters(t *testing.T) {
	st := newTestStore(t)
	user, _ := st.GetOrCreateIdentity("a@b.com")
	ws := user.DefaultWorkspaceID

	_ = st.CreateDeal(ws, &protocol.Deal{Title: "Open one", Stage: "proposal", AmountCents: 1000})
	_ = st.CreateDeal(ws, &protocol.Deal{Title: "Won one", Stage: "closed", Status: "won", AmountCents: 2000})

	q := Query{
		Filters:    []QFilter{{Column: "status", Op: "=", Value: "open"}},
		SortColumn: "updated_at", SortDesc: true, SortNumeric: true, Limit: 10,
	}
	open, _, err := st.QueryDeals(ws, q)
	if err != nil {
		t.Fatalf("query deals: %v", err)
	}
	if len(open) != 1 || open[0].Title != "Open one" {
		t.Fatalf("status filter failed: %+v", open)
	}

	// amount filter via gte.
	big, _, _ := st.QueryDeals(ws, Query{
		Filters:    []QFilter{{Column: "amount_cents", Op: ">=", Value: int64(2000)}},
		SortColumn: "updated_at", SortDesc: true, SortNumeric: true, Limit: 10,
	})
	if len(big) != 1 || big[0].Title != "Won one" {
		t.Fatalf("amount filter failed: %+v", big)
	}
}
