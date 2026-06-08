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

	if st, _ := do(t, ts, "PATCH", "/contacts/"+cid, tok, `{"follow_up_at":"2020-01-01T00:00:00Z","follow_up_note":"ping"}`); st != 200 {
		t.Fatalf("set follow-up: %d", st)
	}
	if st, body := do(t, ts, "GET", "/reminders", tok, ""); st != 200 || !strings.Contains(body, "overdue=yes") || !strings.Contains(body, "# 1 reminder") {
		t.Fatalf("reminders: %d %q", st, body)
	}
}
