package store

import (
	"testing"

	"github.com/crmkit/crmkit/internal/protocol"
)

func TestTotals(t *testing.T) {
	st := newTestStore(t)

	z, err := st.Totals()
	if err != nil {
		t.Fatalf("totals on empty db: %v", err)
	}
	for _, k := range []string{"users", "workspaces", "contacts", "companies", "deals"} {
		if z[k] != 0 {
			t.Fatalf("empty db %s = %d, want 0", k, z[k])
		}
	}

	// Two users, each with a default workspace (the third call is idempotent).
	a, err := st.GetOrCreateIdentity("a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOrCreateIdentity("b@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOrCreateIdentity("a@example.com"); err != nil {
		t.Fatal(err)
	}
	// Some CRM rows in a's workspace.
	ws := a.DefaultWorkspaceID
	if err := st.CreateContact(ws, &protocol.Contact{Name: "Jane"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateCompany(ws, &protocol.Company{Name: "ACME"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeal(ws, &protocol.Deal{Title: "Renewal"}); err != nil {
		t.Fatal(err)
	}

	want := map[string]int{"users": 2, "workspaces": 2, "contacts": 1, "companies": 1, "deals": 1}

	// Collection.
	got, err := st.Totals()
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("totals[%s] = %d, want %d (full: %v)", k, got[k], v, got)
		}
	}

	// Individual methods agree with the collection.
	for name, fn := range map[string]func() (int, error){
		"users": st.CountUsers, "workspaces": st.CountWorkspaces,
		"contacts": st.CountContacts, "companies": st.CountCompanies, "deals": st.CountDeals,
	} {
		n, err := fn()
		if err != nil || n != want[name] {
			t.Fatalf("Count %s = %d (err %v), want %d", name, n, err, want[name])
		}
	}
}
