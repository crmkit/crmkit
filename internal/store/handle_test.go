package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/crmkit/crmkit/internal/protocol"
)

func TestHandleResolution(t *testing.T) {
	st := newTestStore(t)
	u, _ := st.GetOrCreateIdentity("u@b.com")
	ws1, _ := st.CreateWorkspace(u.ID, "W1")
	ws2, _ := st.CreateWorkspace(u.ID, "W2")

	c := &protocol.Contact{Name: "Jane"}
	if err := st.CreateContact(ws1.ID, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(c.Handle) != 5 {
		t.Fatalf("handle %q is not length 5", c.Handle)
	}
	if strings.ContainsAny(c.Handle, "_/") {
		t.Fatalf("handle %q must be stored bare (no separator)", c.Handle)
	}

	// Resolves by bare handle, by the FormatRef wire form, and by internal id.
	for _, ref := range []string{c.Handle, protocol.FormatRef(protocol.KindContact, c.Handle), c.ID} {
		got, err := st.ResolveHandle(ws1.ID, protocol.KindContact, ref)
		if err != nil || got != c.ID {
			t.Fatalf("resolve %q: got %q err %v (want %s)", ref, got, err, c.ID)
		}
	}

	// Unknown handle, wrong kind, and another workspace all miss.
	if _, err := st.ResolveHandle(ws1.ID, protocol.KindContact, "zzzzz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown handle: want ErrNotFound, got %v", err)
	}
	if _, err := st.ResolveHandle(ws1.ID, protocol.KindCompany, c.Handle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong kind: want ErrNotFound, got %v", err)
	}
	if _, err := st.ResolveHandle(ws2.ID, protocol.KindContact, c.Handle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace: want ErrNotFound, got %v", err)
	}
}

func TestHandleUniquePerWorkspace(t *testing.T) {
	st := newTestStore(t)
	u, _ := st.GetOrCreateIdentity("u@b.com")
	ws1, _ := st.CreateWorkspace(u.ID, "W1")
	ws2, _ := st.CreateWorkspace(u.ID, "W2")

	ins := func(ws, id, handle string) error {
		_, err := st.exec(
			`INSERT INTO contacts (id, workspace_id, handle, name, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
			id, ws, handle, "x", 0, 0)
		return err
	}
	if err := ins(ws1.ID, "c_a", "dupes"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Same handle in a different workspace is allowed (scope is per workspace).
	if err := ins(ws2.ID, "c_b", "dupes"); err != nil {
		t.Fatalf("same handle in another workspace should be allowed: %v", err)
	}
	// Same (workspace, handle) is rejected by the unique index - and detected as
	// such, which is what genHandle relies on to retry.
	if err := ins(ws1.ID, "c_c", "dupes"); err == nil || !isUniqueViolation(err) {
		t.Fatalf("duplicate (workspace,handle) should be a unique violation, got %v", err)
	}
}
