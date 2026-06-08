package store

import (
	"testing"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// TestSortVals covers the per-entity cursor sort-key extractors. contactSortVal
// is exercised by the contact query tests; ticket/company/deal are not, so they
// are pinned directly here (pure functions, no DB).
func TestSortVals(t *testing.T) {
	created := time.Unix(1000, 0)
	updated := time.Unix(2000, 0)

	tk := protocol.Ticket{Subject: "Help", CreatedAt: created, UpdatedAt: updated}
	if got := ticketSortVal(tk, "subject"); got != "Help" {
		t.Errorf("ticketSortVal subject = %q", got)
	}
	if got := ticketSortVal(tk, "created_at"); got != "1000" {
		t.Errorf("ticketSortVal created_at = %q", got)
	}
	if got := ticketSortVal(tk, "whatever"); got != "2000" { // default -> updated_at
		t.Errorf("ticketSortVal default = %q", got)
	}

	co := protocol.Company{Name: "Acme", CreatedAt: created, UpdatedAt: updated}
	if got := companySortVal(co, "name"); got != "Acme" {
		t.Errorf("companySortVal name = %q", got)
	}
	if got := companySortVal(co, "created_at"); got != "1000" {
		t.Errorf("companySortVal created_at = %q", got)
	}
	if got := companySortVal(co, ""); got != "2000" {
		t.Errorf("companySortVal default = %q", got)
	}

	// Deal has an amount_cents sort column the others don't.
	d := protocol.Deal{Title: "Renewal", AmountCents: 50000, CreatedAt: created, UpdatedAt: updated}
	if got := dealSortVal(d, "title"); got != "Renewal" {
		t.Errorf("dealSortVal title = %q", got)
	}
	if got := dealSortVal(d, "amount_cents"); got != "50000" {
		t.Errorf("dealSortVal amount_cents = %q", got)
	}
	if got := dealSortVal(d, "created_at"); got != "1000" {
		t.Errorf("dealSortVal created_at = %q", got)
	}
	if got := dealSortVal(d, "x"); got != "2000" {
		t.Errorf("dealSortVal default = %q", got)
	}
}
