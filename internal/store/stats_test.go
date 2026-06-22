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

func TestActivityStatsBatch(t *testing.T) {
	st := newTestStore(t)
	a, err := st.GetOrCreateIdentity("a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ws := a.DefaultWorkspaceID

	active := &protocol.Company{Name: "Active Co"}
	quiet := &protocol.Company{Name: "Quiet Co"}
	if err := st.CreateCompany(ws, active); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateCompany(ws, quiet); err != nil {
		t.Fatal(err)
	}

	// Two activities on the active company, none on the quiet one.
	for _, body := range []string{"first", "second"} {
		if err := st.CreateActivity(ws, &protocol.Activity{CompanyID: active.ID, Kind: "note", Body: body}); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := st.ActivityStatsBatch(ws, protocol.KindCompany, []string{active.ID, quiet.ID})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if got := stats[active.ID].Count; got != 2 {
		t.Fatalf("active count = %d, want 2", got)
	}
	if stats[active.ID].Last.IsZero() {
		t.Fatalf("active company should report a non-zero last-activity time")
	}
	// A record with no activity is absent from the map - callers treat missing as zero.
	if _, ok := stats[quiet.ID]; ok {
		t.Fatalf("quiet company has no activity and should be absent, got %+v", stats[quiet.ID])
	}

	// An unknown kind or empty id set yields an empty map without error.
	if m, err := st.ActivityStatsBatch(ws, "bogus", []string{active.ID}); err != nil || len(m) != 0 {
		t.Fatalf("unknown kind: len=%d err=%v, want 0 / nil", len(m), err)
	}
	if m, err := st.ActivityStatsBatch(ws, protocol.KindCompany, nil); err != nil || len(m) != 0 {
		t.Fatalf("empty ids: len=%d err=%v, want 0 / nil", len(m), err)
	}
}
