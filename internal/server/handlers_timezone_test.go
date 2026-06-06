package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestWorkspaceTimezoneFormatsReads(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts) // me@example.com, workspace auto-provisioned at UTC

	wsID := func() string {
		_, body := do(t, ts, "GET", "/workspaces", token, "")
		return firstHandleID(body, "workspace/")
	}()
	if wsID == "" {
		t.Fatal("no workspace id")
	}

	// A follow-up at a known UTC instant.
	_, body := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe","follow_up_at":"2026-06-10T16:00:00Z"}`)
	id := firstHandleID(body, "contact/")
	if id == "" {
		t.Fatalf("no contact id in %q", body)
	}

	// Default workspace tz is UTC: the time renders with a Z offset.
	if _, detail := do(t, ts, "GET", "/contacts/"+id, token, ""); !strings.Contains(detail, "2026-06-10T16:00Z") {
		t.Fatalf("UTC default should render with Z:\n%s", detail)
	}

	// Switch the workspace to Los Angeles (PDT in June = -07:00).
	if status, b := do(t, ts, "PATCH", "/workspaces/"+wsID, token, `{"timezone":"America/Los_Angeles"}`); status != http.StatusOK {
		t.Fatalf("set timezone: %d %q", status, b)
	}

	// The next read (token re-resolved) renders the same instant in -07:00.
	if _, detail := do(t, ts, "GET", "/contacts/"+id, token, ""); !strings.Contains(detail, "2026-06-10T09:00-07:00") {
		t.Fatalf("LA tz should render 16:00Z as 09:00-07:00:\n%s", detail)
	}
}

func TestWorkspaceTimezoneRejectsInvalid(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)
	_, body := do(t, ts, "GET", "/workspaces", token, "")
	wsID := firstHandleID(body, "workspace/")

	if status, b := do(t, ts, "PATCH", "/workspaces/"+wsID, token, `{"timezone":"Mars/Phobos"}`); status != http.StatusBadRequest || !strings.Contains(b, "invalid_timezone") {
		t.Fatalf("invalid timezone should 400 invalid_timezone, got %d %q", status, b)
	}
}
