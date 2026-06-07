package store

import (
	"testing"
	"time"
)

func TestPruneAuditForPlan(t *testing.T) {
	st := newTestStore(t)

	user, err := st.GetOrCreateIdentity("u@b.com")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	ws, err := st.CreateWorkspace(user.ID, "W")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	plan, err := st.WorkspacePlan(ws.ID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	ins := func(id, wsID string, created int64) {
		if _, err := st.exec(
			`INSERT INTO audit_log (id, workspace_id, token_id, actor_email, action, target, detail, created_at)
VALUES (?,?,?,?,?,?,?,?)`,
			id, wsID, "tok", "a@b.com", "contact.create", "contact/x", "", created); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	ins("aud_old", ws.ID, time.Now().AddDate(0, 0, -200).Unix()) // 200 days old
	ins("aud_new", ws.ID, time.Now().Unix())                     // just now

	cutoff := time.Now().AddDate(0, 0, -180).Unix()

	// Pruning a different plan must not touch this workspace's audit.
	if n, err := st.PruneAuditForPlan("some-other-plan", cutoff); err != nil || n != 0 {
		t.Fatalf("cross-plan prune: n=%d err=%v (want 0, nil)", n, err)
	}

	// Pruning this workspace's plan drops only the entry past the window.
	n, err := st.PruneAuditForPlan(plan, cutoff)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}

	list, err := st.ListAudit(ws.ID, "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "aud_new" {
		t.Fatalf("expected only the recent entry to survive: %+v", list)
	}
}
