package render

import (
	"strings"
	"testing"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

func TestStamp(t *testing.T) {
	if got := Stamp(time.Time{}); got != "" {
		t.Errorf("zero time must render empty, got %q", got)
	}
	// UTC renders with a trailing Z and minute precision (seconds dropped).
	utc := time.Date(2026, 6, 6, 7, 14, 30, 0, time.UTC)
	if got := Stamp(utc); got != "2026-06-06T07:14Z" {
		t.Errorf("UTC stamp = %q, want 2026-06-06T07:14Z (no seconds)", got)
	}
	// A non-UTC zone renders the numeric offset instead of Z.
	pdt := time.Date(2026, 6, 6, 7, 14, 0, 0, time.FixedZone("PDT", -7*60*60))
	if got := Stamp(pdt); got != "2026-06-06T07:14-07:00" {
		t.Errorf("offset stamp = %q, want 2026-06-06T07:14-07:00", got)
	}
}

func TestDatep(t *testing.T) {
	if got := datep(nil); got != "" {
		t.Errorf("nil pointer must render empty, got %q", got)
	}
	tm := time.Date(2026, 6, 6, 7, 14, 0, 0, time.UTC)
	if got := datep(&tm); got != "2026-06-06T07:14Z" {
		t.Errorf("datep = %q, want 2026-06-06T07:14Z", got)
	}
}

func TestMoney(t *testing.T) {
	cases := []struct {
		cents    int64
		currency string
		want     string
	}{
		{0, "USD", ""},          // zero is omitted
		{1050, "", "10.50 USD"}, // empty currency defaults to USD
		{2000, "EUR", "20.00 EUR"},
		{5, "USD", "0.05 USD"}, // sub-dollar
	}
	for _, c := range cases {
		if got := money(c.cents, c.currency); got != c.want {
			t.Errorf("money(%d, %q) = %q, want %q", c.cents, c.currency, got, c.want)
		}
	}
}

func TestCustomFields(t *testing.T) {
	if got := customFields(nil); got != nil {
		t.Errorf("nil custom must yield nil, got %v", got)
	}
	got := customFields(map[string]any{"b": 2, "a": "x", "c": true})
	want := []Field{
		{Key: "custom.a", Val: "x"},
		{Key: "custom.b", Val: "2"},
		{Key: "custom.c", Val: "true"},
	}
	if len(got) != len(want) {
		t.Fatalf("customFields len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("customFields[%d] = %+v, want %+v (keys must be sorted)", i, got[i], want[i])
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	if nameOrID("Jane", "c_1") != "Jane" {
		t.Error("nameOrID must prefer the resolved name")
	}
	if nameOrID("", "c_1") != "c_1" {
		t.Error("nameOrID must fall back to the id")
	}
	if nameOrID("", "") != "" {
		t.Error("nameOrID with both empty must be empty")
	}

	if countField(0) != "" || countField(-3) != "" {
		t.Error("countField must omit non-positive counts")
	}
	if countField(7) != "7" {
		t.Error("countField(7) must render 7")
	}

	if verStr(0) != "" || verStr(-1) != "" {
		t.Error("verStr must omit non-positive versions")
	}
	if verStr(42) != "42" {
		t.Error("verStr(42) must render 42")
	}

	if fallback("", "def") != "def" {
		t.Error("fallback must use the default for empty")
	}
	if fallback("x", "def") != "x" {
		t.Error("fallback must keep a non-empty value")
	}
}

func TestInt(t *testing.T) {
	cases := []struct {
		in   string
		def  int
		want int
	}{
		{"", 5, 5},    // empty -> default
		{"abc", 5, 5}, // unparseable -> default
		{"7", 5, 7},   // parsed
		{"-3", 5, -3}, // negatives parse
		{"10x", 9, 9}, // trailing junk -> default
	}
	for _, c := range cases {
		if got := Int(c.in, c.def); got != c.want {
			t.Errorf("Int(%q, %d) = %d, want %d", c.in, c.def, got, c.want)
		}
	}
}

func TestDealsOpenPipeline(t *testing.T) {
	deals := []protocol.Deal{
		{AmountCents: 100000, Status: "open"},
		{AmountCents: 50000, Status: ""},      // empty status counts as open
		{AmountCents: 999900, Status: "won"},  // excluded
		{AmountCents: 999900, Status: "lost"}, // excluded
	}
	got := Deals(deals)
	// Only open + empty-status amounts (1000.00 + 500.00) feed the pipeline total.
	if !strings.Contains(got, "# 4 deal(s), open pipeline 1500.00 USD") {
		t.Errorf("deals summary wrong: %q", got)
	}

	if got := Deals(nil); !strings.Contains(got, "# 0 deal(s), open pipeline 0.00 USD") {
		t.Errorf("empty deals must show 0.00 USD pipeline, got %q", got)
	}
}

func TestReminders(t *testing.T) {
	due := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)
	list := []protocol.Reminder{
		{Handle: "contact_a", FollowUpAt: due, Overdue: true},
		{Handle: "deal_b", FollowUpAt: due, Overdue: false},
	}
	got := Reminders(list)
	if !strings.Contains(got, "# 2 reminder(s), 1 overdue") {
		t.Errorf("reminders summary wrong: %q", got)
	}
}

func TestReminderLineOverdue(t *testing.T) {
	due := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)
	overdue := ReminderLine(protocol.Reminder{Handle: "contact_a", FollowUpAt: due, Overdue: true})
	if !strings.Contains(overdue, "overdue=yes") {
		t.Errorf("overdue reminder must show overdue=yes, got %q", overdue)
	}
	notdue := ReminderLine(protocol.Reminder{Handle: "contact_a", FollowUpAt: due, Overdue: false})
	if strings.Contains(notdue, "overdue=") {
		t.Errorf("non-overdue reminder must omit the overdue field, got %q", notdue)
	}
}

func TestWorkspacesHint(t *testing.T) {
	one := []protocol.Workspace{{ID: "w1", Name: "Acme", Role: "admin"}}
	if got := WorkspacesHint(one); got != "" {
		t.Errorf("a single workspace must produce no hint, got %q", got)
	}
	two := []protocol.Workspace{
		{ID: "w1", Name: "Acme", Role: "admin"},
		{ID: "w2", Name: "Beta", Role: "member"},
	}
	got := WorkspacesHint(two)
	if !strings.Contains(got, "POST /workspaces/w1/tokens") || !strings.Contains(got, "POST /workspaces/w2/tokens") {
		t.Errorf("multi-workspace hint must list each workspace, got %q", got)
	}
}
