package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestRemindersEndpoint(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := loginAs(t, ts, "alice@acme.com")

	st, body := do(t, ts, "POST", "/contacts", tok, `{"name":"Jane Doe"}`)
	if st != http.StatusCreated {
		t.Fatalf("create contact: %d %q", st, body)
	}
	cid := firstHandleID(body, "contact/")

	// An overdue, open task linked to the contact surfaces as a reminder.
	if st, b := do(t, ts, "POST", "/tasks", tok, `{"title":"ping","due_at":"2020-01-01T00:00:00Z","contact_id":"`+cid+`"}`); st != http.StatusCreated {
		t.Fatalf("create task: %d %q", st, b)
	}
	if st, body := do(t, ts, "GET", "/reminders", tok, ""); st != 200 || !strings.Contains(body, "overdue=yes") || !strings.Contains(body, "# 1 reminder") || !strings.Contains(body, "title=ping") {
		t.Fatalf("reminders: %d %q", st, body)
	}
}
