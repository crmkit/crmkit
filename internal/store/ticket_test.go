package store

import (
	"errors"
	"testing"

	"github.com/crmkit/crmkit/internal/protocol"
)

func TestTicketStoreCRUDAndIsolation(t *testing.T) {
	st := newTestStore(t)
	u, _ := st.GetOrCreateIdentity("u@b.com")
	wsA, _ := st.CreateWorkspace(u.ID, "A")
	wsB, _ := st.CreateWorkspace(u.ID, "B")

	tk := &protocol.Ticket{Subject: "Help"}
	if err := st.CreateTicket(wsA.ID, tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Sensible defaults are assigned.
	if tk.Status != "open" || tk.Version != 1 || tk.Handle == "" {
		t.Fatalf("create defaults: status=%q version=%d handle=%q", tk.Status, tk.Version, tk.Handle)
	}

	// Visible in its own workspace...
	if got, err := st.GetTicket(wsA.ID, tk.ID); err != nil || got.Subject != "Help" {
		t.Fatalf("get in A: %+v err %v", got, err)
	}
	// ...and never in another (tenant isolation).
	if _, err := st.GetTicket(wsB.ID, tk.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ticket must not leak across workspaces, got %v", err)
	}

	// Delete removes it.
	if err := st.DeleteTicket(wsA.ID, tk.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetTicket(wsA.ID, tk.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted ticket should be gone, got %v", err)
	}
}
