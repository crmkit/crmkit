package store

import "testing"

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
