package store

import (
	"errors"
	"testing"

	"github.com/crmkit/crmkit/internal/protocol"
)

func TestOptimisticConcurrency(t *testing.T) {
	st := newTestStore(t)
	u, _ := st.GetOrCreateIdentity("u@b.com")
	ws, _ := st.CreateWorkspace(u.ID, "W")

	c := &protocol.Contact{Name: "Jane"}
	if err := st.CreateContact(ws.ID, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.Version != 1 {
		t.Fatalf("new record should be version 1, got %d", c.Version)
	}

	// A matching conditional update succeeds and bumps the version.
	c.Name = "Jane R"
	if err := st.UpdateContact(ws.ID, c, 1); err != nil {
		t.Fatalf("matching update: %v", err)
	}
	if c.Version != 2 {
		t.Fatalf("version should be 2 after update, got %d", c.Version)
	}

	// A stale conditional update (still expecting 1) is rejected, and changes
	// nothing.
	stale := &protocol.Contact{ID: c.ID, Name: "clobber"}
	if err := st.UpdateContact(ws.ID, stale, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update should ErrConflict, got %v", err)
	}
	if got, _ := st.GetContact(ws.ID, c.ID); got.Name != "Jane R" || got.Version != 2 {
		t.Fatalf("record must be untouched by the conflicting write: %q v%d", got.Name, got.Version)
	}

	// Unconditional update (ifMatch 0) always wins and still bumps the version.
	c.Name = "Jane Final"
	if err := st.UpdateContact(ws.ID, c, 0); err != nil {
		t.Fatalf("unconditional update: %v", err)
	}
	if c.Version != 3 {
		t.Fatalf("version should be 3, got %d", c.Version)
	}

	// A conditional update against a missing record is ErrNotFound, not Conflict.
	if err := st.UpdateContact(ws.ID, &protocol.Contact{ID: "c_missing"}, 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing record should ErrNotFound, got %v", err)
	}
}
