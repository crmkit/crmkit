package server

import (
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// locationOf resolves the session's workspace timezone to a *time.Location,
// falling back to UTC for an unset or unrecognised zone. Times are stored in
// UTC; this is only used to format reads for humans.
func locationOf(sess protocol.Session) *time.Location {
	if sess.WorkspaceTimezone == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(sess.WorkspaceTimezone); err == nil {
		return loc
	}
	return time.UTC
}

// localizedSlice returns a copy of list with each element's instants expressed
// in loc, for timezone-aware display.
func localizedSlice[T interface {
	Localized(*time.Location) T
}](list []T, loc *time.Location) []T {
	out := make([]T, len(list))
	for i, v := range list {
		out[i] = v.Localized(loc)
	}
	return out
}
