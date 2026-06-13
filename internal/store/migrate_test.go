package store

import "testing"

// TestFollowUpBackfill exercises the v18 migration's data path: a contact with a
// follow_up_at set under the v17 schema must become an open task linked back to
// it, and the old columns must be gone afterward. The empty-DB lifecycle test
// never runs the backfill with real rows, so this seeds at v17 explicitly.
func TestFollowUpBackfill(t *testing.T) {
	st, err := openSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Apply only up to v17 (tasks table exists, follow_up columns still present),
	// then restore the full set so the second apply runs v18.
	full := migrations
	migrations = full[:17]
	if _, err := st.ApplyMigrations(); err != nil {
		migrations = full
		t.Fatalf("apply v1..v17: %v", err)
	}

	// Seed a workspace + a contact carrying a follow-up.
	const due = int64(1577836800) // 2020-01-01T00:00:00Z
	if _, err := st.exec(`INSERT INTO workspaces (id, name, created_at) VALUES (?,?,?)`, "ws_1", "Acme", 1); err != nil {
		migrations = full
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := st.exec(`INSERT INTO contacts (id, workspace_id, handle, name, follow_up_at, follow_up_note, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?)`, "c_1", "ws_1", "h1", "Jane", due, "Send quote", 1, 1); err != nil {
		migrations = full
		t.Fatalf("seed contact: %v", err)
	}

	// Apply v18: backfill + drop columns.
	migrations = full
	if _, err := st.ApplyMigrations(); err != nil {
		t.Fatalf("apply v18: %v", err)
	}

	// The follow-up is now an open task linked to the contact.
	var (
		title     string
		gotDue    int64
		contactID string
		doneAt    *int64
	)
	if err := st.queryRow(`SELECT title, due_at, contact_id, done_at FROM tasks WHERE contact_id = ?`, "c_1").
		Scan(&title, &gotDue, &contactID, &doneAt); err != nil {
		t.Fatalf("backfilled task lookup: %v", err)
	}
	if title != "Send quote" || gotDue != due || contactID != "c_1" || doneAt != nil {
		t.Fatalf("backfilled task = {title:%q due:%d contact:%q done:%v}, want {Send quote, %d, c_1, nil}", title, gotDue, contactID, doneAt, due)
	}

	// The old columns are gone.
	if err := st.queryRow(`SELECT follow_up_at FROM contacts WHERE id = ?`, "c_1").Scan(new(int64)); err == nil {
		t.Fatal("contacts.follow_up_at should have been dropped")
	}
}

// TestMigrationLifecycle covers the Option-A safety model: a freshly opened
// database has no schema (Open never writes DDL), MigrationStatus reports
// everything pending, ApplyMigrations brings it current, and a second apply is a
// no-op.
func TestMigrationLifecycle(t *testing.T) {
	st, err := openSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Fresh database: nothing applied, everything pending.
	state, err := st.MigrationStatus()
	if err != nil {
		t.Fatalf("status (fresh): %v", err)
	}
	if state.Current != 0 {
		t.Errorf("fresh current = %d, want 0", state.Current)
	}
	if state.Latest != len(migrations) {
		t.Errorf("latest = %d, want %d", state.Latest, len(migrations))
	}
	if len(state.Pending) != len(migrations) {
		t.Fatalf("pending = %d, want %d (all)", len(state.Pending), len(migrations))
	}

	// Open must not have created any application table. queryRow never leaks the
	// connection on the error path (unlike a raw Query whose rows we'd discard),
	// which matters on SQLite's single connection.
	var one int
	if err := st.queryRow(`SELECT 1 FROM workspaces`).Scan(&one); err == nil {
		t.Fatal("workspaces table should not exist before ApplyMigrations")
	}

	// Apply: every registered migration runs.
	applied, err := st.ApplyMigrations()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) != len(migrations) {
		t.Errorf("applied %d, want %d", len(applied), len(migrations))
	}

	// Now current and nothing pending.
	state, err = st.MigrationStatus()
	if err != nil {
		t.Fatalf("status (after apply): %v", err)
	}
	if state.Current != state.Latest || len(state.Pending) != 0 {
		t.Errorf("after apply: current=%d latest=%d pending=%d", state.Current, state.Latest, len(state.Pending))
	}

	// The schema is usable.
	if err := st.queryRow(`SELECT count(*) FROM workspaces`).Scan(&one); err != nil {
		t.Errorf("workspaces table should exist after ApplyMigrations: %v", err)
	}

	// Idempotent: re-applying changes nothing.
	again, err := st.ApplyMigrations()
	if err != nil {
		t.Fatalf("apply (again): %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second apply ran %d migrations, want 0", len(again))
	}
}
