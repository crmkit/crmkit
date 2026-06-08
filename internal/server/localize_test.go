package server

import (
	"testing"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

func TestLocationOf(t *testing.T) {
	// Empty timezone -> UTC.
	if loc := locationOf(protocol.Session{}); loc != time.UTC {
		t.Errorf("empty timezone must resolve to UTC, got %v", loc)
	}
	// A valid IANA zone resolves (tzdata is embedded, so it works on minimal hosts).
	if loc := locationOf(protocol.Session{WorkspaceTimezone: "America/Los_Angeles"}); loc.String() != "America/Los_Angeles" {
		t.Errorf("valid zone must resolve, got %v", loc)
	}
	// An unrecognised zone falls back to UTC rather than failing. This branch is
	// unreachable via HTTP (the handler validates the tz before storing it), so it
	// is only covered here - it guards an invalid zone reaching a session by some
	// other path.
	if loc := locationOf(protocol.Session{WorkspaceTimezone: "Mars/Phobos"}); loc != time.UTC {
		t.Errorf("unrecognised zone must fall back to UTC, got %v", loc)
	}
}

func TestLocalizedSlice(t *testing.T) {
	loc := time.FixedZone("EST", -5*60*60)
	utc := time.Date(2026, 6, 8, 15, 0, 0, 0, time.UTC)
	in := []protocol.Contact{{CreatedAt: utc}, {CreatedAt: utc}}

	out := localizedSlice(in, loc)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	for i, c := range out {
		if c.CreatedAt.Location() != loc || !c.CreatedAt.Equal(utc) {
			t.Errorf("out[%d] not localized in place (got %v)", i, c.CreatedAt.Location())
		}
	}
	// The input slice must be untouched.
	if in[0].CreatedAt.Location() != time.UTC {
		t.Error("localizedSlice must not mutate its input")
	}
	// Empty input yields an empty, non-nil slice.
	if got := localizedSlice([]protocol.Contact{}, loc); got == nil || len(got) != 0 {
		t.Errorf("empty input must yield an empty slice, got %v", got)
	}
}
