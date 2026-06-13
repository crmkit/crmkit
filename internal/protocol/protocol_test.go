package protocol

import (
	"strings"
	"testing"
	"time"
)

// idAlpha mirrors the alphabet used by NewID/NewHandle; IDs must stay within it.
const idAlpha = "abcdefghijkmnpqrstuvwxyz23456789"

func TestParseRef(t *testing.T) {
	cases := map[string]string{
		"contact_k7m2q": "k7m2q", // agent-facing wire form
		"contact/k7m2q": "k7m2q", // slash form
		"k7m2q":         "k7m2q", // already bare
		"":              "",      // empty stays empty
		"deal_abc/xyz":  "xyz",   // last separator wins
		"c_3f9a":        "3f9a",  // opaque id reduces to its suffix (documented)
	}
	for in, want := range cases {
		if got := ParseRef(in); got != want {
			t.Errorf("ParseRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatRefRoundTrip(t *testing.T) {
	ref := FormatRef("contact", "k7m2q")
	if ref != "contact_k7m2q" {
		t.Fatalf("FormatRef = %q, want contact_k7m2q", ref)
	}
	if got := ParseRef(ref); got != "k7m2q" {
		t.Fatalf("ParseRef(FormatRef(...)) = %q, want k7m2q", got)
	}
	if FormatRef("contact", "") != "" {
		t.Fatal("empty handle must format to empty string (field omitted)")
	}
}

func TestHandleSplitHandle(t *testing.T) {
	k, id := SplitHandle(Handle("contact", "c_123"))
	if k != "contact" || id != "c_123" {
		t.Fatalf("SplitHandle(Handle(...)) = (%q, %q), want (contact, c_123)", k, id)
	}
	if k, id := SplitHandle("bare"); k != "" || id != "bare" {
		t.Fatalf("SplitHandle(bare) = (%q, %q), want (\"\", bare)", k, id)
	}
}

func TestNewIDFormat(t *testing.T) {
	id := NewID("c")
	if !strings.HasPrefix(id, "c_") {
		t.Fatalf("NewID missing prefix: %q", id)
	}
	body := strings.TrimPrefix(id, "c_")
	if body == "" {
		t.Fatal("NewID produced an empty body")
	}
	for _, r := range body {
		if !strings.ContainsRune(idAlpha, r) {
			t.Fatalf("NewID body has out-of-alphabet char %q in %q", r, id)
		}
	}
	if NewID("c") == id {
		t.Fatal("NewID must be unique across calls")
	}
}

func TestNewHandleFormat(t *testing.T) {
	h := NewHandle()
	if len(h) != handleLen {
		t.Fatalf("NewHandle len = %d, want %d", len(h), handleLen)
	}
	for _, r := range h {
		if !strings.ContainsRune(idAlpha, r) {
			t.Fatalf("NewHandle has out-of-alphabet char %q in %q", r, h)
		}
	}
	// Probabilistic uniqueness: a 5-char, 31-symbol space is large enough that a
	// batch of 1000 should see effectively no collisions.
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		seen[NewHandle()] = struct{}{}
	}
	if len(seen) < 995 {
		t.Fatalf("excessive handle collisions: %d unique of 1000", len(seen))
	}
}

func TestInLoc(t *testing.T) {
	loc := time.FixedZone("EST", -5*60*60)
	utc := time.Date(2026, 6, 8, 15, 0, 0, 0, time.UTC)

	got := inLoc(utc, loc)
	if !got.Equal(utc) {
		t.Error("inLoc must preserve the instant, only change its display location")
	}
	if got.Location() != loc {
		t.Errorf("inLoc location = %v, want EST", got.Location())
	}
	if z := inLoc(time.Time{}, loc); !z.IsZero() {
		t.Error("inLoc must leave the zero time untouched")
	}
	if g := inLoc(utc, nil); g.Location() != time.UTC {
		t.Error("inLoc with nil loc must return the value unchanged")
	}
}

func TestInLocPtr(t *testing.T) {
	loc := time.FixedZone("EST", -5*60*60)
	utc := time.Date(2026, 6, 8, 15, 0, 0, 0, time.UTC)

	got := inLocPtr(&utc, loc)
	if got == nil || !got.Equal(utc) || got.Location() != loc {
		t.Errorf("inLocPtr = %v, want same instant in EST", got)
	}
	if utc.Location() != time.UTC {
		t.Error("inLocPtr must not mutate the original through the pointer")
	}
	if inLocPtr(nil, loc) != nil {
		t.Error("inLocPtr(nil) must stay nil")
	}
}

func TestContactLocalized(t *testing.T) {
	loc := time.FixedZone("EST", -5*60*60)
	last := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	c := Contact{
		CreatedAt:      time.Date(2026, 6, 8, 15, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 6, 8, 16, 0, 0, 0, time.UTC),
		LastActivityAt: &last,
	}

	got := c.Localized(loc)
	if got.CreatedAt.Location() != loc || got.UpdatedAt.Location() != loc {
		t.Error("CreatedAt/UpdatedAt must be expressed in the display location")
	}
	if !got.CreatedAt.Equal(c.CreatedAt) || !got.UpdatedAt.Equal(c.UpdatedAt) {
		t.Error("Localized must preserve the underlying instants")
	}
	if got.LastActivityAt == nil || got.LastActivityAt.Location() != loc {
		t.Error("LastActivityAt pointer must be localized")
	}
	if last.Location() != time.UTC {
		t.Error("Localized must not mutate the caller's stored time through the pointer")
	}
}

// TestAllLocalized guards against a Localized method forgetting one of its time
// fields: every entity is built with all its instants set, and each is checked
// to come back expressed in the display location with the instant preserved.
func TestAllLocalized(t *testing.T) {
	loc := time.FixedZone("EST", -5*60*60)
	utc := time.Date(2026, 6, 8, 15, 0, 0, 0, time.UTC)
	ptr := func() *time.Time { u := utc; return &u }

	chk := func(name string, got time.Time) {
		t.Helper()
		if !got.Equal(utc) {
			t.Errorf("%s: instant changed (got %v)", name, got)
		}
		if got.Location() != loc {
			t.Errorf("%s: not in display location (got %v)", name, got.Location())
		}
	}
	chkPtr := func(name string, got *time.Time) {
		t.Helper()
		if got == nil {
			t.Errorf("%s: unexpectedly nil after Localized", name)
			return
		}
		chk(name, *got)
	}

	t.Run("Contact", func(t *testing.T) {
		c := Contact{CreatedAt: utc, UpdatedAt: utc, LastActivityAt: ptr()}.Localized(loc)
		chk("CreatedAt", c.CreatedAt)
		chk("UpdatedAt", c.UpdatedAt)
		chkPtr("LastActivityAt", c.LastActivityAt)
	})
	t.Run("Company", func(t *testing.T) {
		c := Company{CreatedAt: utc, UpdatedAt: utc, LastActivityAt: ptr()}.Localized(loc)
		chk("CreatedAt", c.CreatedAt)
		chk("UpdatedAt", c.UpdatedAt)
		chkPtr("LastActivityAt", c.LastActivityAt)
	})
	t.Run("Deal", func(t *testing.T) {
		d := Deal{CreatedAt: utc, UpdatedAt: utc, LastActivityAt: ptr()}.Localized(loc)
		chk("CreatedAt", d.CreatedAt)
		chk("UpdatedAt", d.UpdatedAt)
		chkPtr("LastActivityAt", d.LastActivityAt)
	})
	t.Run("Ticket", func(t *testing.T) {
		tk := Ticket{CreatedAt: utc, UpdatedAt: utc, LastActivityAt: ptr()}.Localized(loc)
		chk("CreatedAt", tk.CreatedAt)
		chk("UpdatedAt", tk.UpdatedAt)
		chkPtr("LastActivityAt", tk.LastActivityAt)
	})
	t.Run("Task", func(t *testing.T) {
		tk := Task{CreatedAt: utc, UpdatedAt: utc, DueAt: ptr(), DoneAt: ptr()}.Localized(loc)
		chk("CreatedAt", tk.CreatedAt)
		chk("UpdatedAt", tk.UpdatedAt)
		chkPtr("DueAt", tk.DueAt)
		chkPtr("DoneAt", tk.DoneAt)
	})
	t.Run("Activity", func(t *testing.T) {
		a := Activity{CreatedAt: utc}.Localized(loc)
		chk("CreatedAt", a.CreatedAt)
	})
	t.Run("Reminder", func(t *testing.T) {
		r := Reminder{FollowUpAt: utc}.Localized(loc)
		chk("FollowUpAt", r.FollowUpAt)
	})
	t.Run("Member", func(t *testing.T) {
		m := Member{CreatedAt: utc}.Localized(loc)
		chk("CreatedAt", m.CreatedAt)
	})
	t.Run("Workspace", func(t *testing.T) {
		w := Workspace{CreatedAt: utc}.Localized(loc)
		chk("CreatedAt", w.CreatedAt)
	})
}
