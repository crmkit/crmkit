package server

import (
	"testing"

	"github.com/crmkit/crmkit/internal/protocol"
)

func TestChangeDetail(t *testing.T) {
	// Only changed fields appear; an empty "before" renders as (none).
	got := changeDetail(
		[3]string{"stage", "lead", "customer"},
		[3]string{"owner", "", "alice"},
		[3]string{"name", "Jane", "Jane"}, // unchanged -> omitted
	)
	want := "stage: lead -> customer; owner: (none) -> alice"
	if got != want {
		t.Errorf("changeDetail = %q, want %q", got, want)
	}

	// Nothing meaningful changed -> empty detail.
	if got := changeDetail([3]string{"name", "Jane", "Jane"}); got != "" {
		t.Errorf("all-unchanged must yield empty detail, got %q", got)
	}
}

func TestOrNone(t *testing.T) {
	if orNone("") != "(none)" || orNone("   ") != "(none)" {
		t.Error("blank/whitespace must render as (none)")
	}
	if orNone("alice") != "alice" {
		t.Error("non-blank must pass through")
	}
}

func TestOrName(t *testing.T) {
	if orName("Acme", "co_1") != "Acme" {
		t.Error("orName must prefer the resolved name")
	}
	if orName("", "co_1") != "co_1" {
		t.Error("orName must fall back to the handle")
	}
	if orName("", "") != "" {
		t.Error("orName with both empty must be empty")
	}
}

func TestMoney2(t *testing.T) {
	cases := map[int64]string{
		0:     "",       // zero is omitted (currency-less; cf. render.money)
		100:   "1.00",   //
		12345: "123.45", //
		5:     "0.05",   //
	}
	for cents, want := range cases {
		if got := money2(cents); got != want {
			t.Errorf("money2(%d) = %q, want %q", cents, got, want)
		}
	}
}

func TestDiffContact(t *testing.T) {
	before := protocol.Contact{Name: "Jane", Email: "j@x.com", Stage: "lead"}
	after := protocol.Contact{Name: "Jane Doe", Email: "j@x.com", Stage: "customer"}
	got := diffContact(before, after)
	want := "name: Jane -> Jane Doe; stage: lead -> customer"
	if got != want {
		t.Errorf("diffContact = %q, want %q", got, want)
	}

	// Relations resolve via orName: a name on the before, only a handle on the after.
	got = diffContact(
		protocol.Contact{CompanyName: "Acme"},
		protocol.Contact{CompanyHandle: "co_9"},
	)
	if got != "company: Acme -> co_9" {
		t.Errorf("diffContact company = %q, want company: Acme -> co_9", got)
	}

	// Contract: notes/custom are intentionally NOT diffed (keeps the audit detail
	// scannable). Changing only those must produce no detail.
	noisy := diffContact(
		protocol.Contact{Name: "Jane"},
		protocol.Contact{Name: "Jane", Notes: "long note", Custom: map[string]any{"k": "v"}},
	)
	if noisy != "" {
		t.Errorf("notes/custom must be excluded from the diff, got %q", noisy)
	}
}

func TestDiffDealAmount(t *testing.T) {
	// A zero->nonzero amount renders the before as (none) via money2("")->orNone.
	got := diffDeal(
		protocol.Deal{AmountCents: 0, Status: "open"},
		protocol.Deal{AmountCents: 50000, Status: "won"},
	)
	want := "status: open -> won; amount: (none) -> 500.00"
	if got != want {
		t.Errorf("diffDeal = %q, want %q", got, want)
	}
}

func TestDiffCompanyAndTicket(t *testing.T) {
	c := diffCompany(
		protocol.Company{Name: "Acme", Domain: "acme.com", Tags: []string{"a"}},
		protocol.Company{Name: "Acme Inc", Domain: "acme.com", Tags: []string{"a", "b"}},
	)
	if c != "name: Acme -> Acme Inc; tags: a -> a,b" {
		t.Errorf("diffCompany = %q", c)
	}

	tk := diffTicket(
		protocol.Ticket{Subject: "Help", Status: "open"},
		protocol.Ticket{Subject: "Help", Status: "solved", Assignee: "alice"},
	)
	if tk != "status: open -> solved; assignee: (none) -> alice" {
		t.Errorf("diffTicket = %q", tk)
	}
}
