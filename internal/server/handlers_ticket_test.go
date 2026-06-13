package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestTicketCRUD(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// A requester contact to attach the ticket to.
	_, cb := do(t, ts, "POST", "/contacts", token, `{"name":"Jane","email":"jane@acme.com"}`)
	cid := firstHandleID(cb, "contact/")
	if cid == "" {
		t.Fatalf("no contact handle: %s", cb)
	}

	// Create a ticket, referencing the requester by handle.
	s, b := do(t, ts, "POST", "/tickets", token,
		`{"subject":"Can't log in","content":"500 on login","requester_id":"contact_`+cid+`","assignee":"agent@x.com"}`)
	if s != http.StatusCreated {
		t.Fatalf("create ticket: %d %q", s, b)
	}
	// Response shows subject, a defaulted status of open, the resolved requester
	// name, and the assignee.
	for _, want := range []string{"Can't log in", "status:", "open", "Jane", "agent@x.com"} {
		if !strings.Contains(b, want) {
			t.Fatalf("create response missing %q:\n%s", want, b)
		}
	}
	tid := firstHandleID(b, "ticket/")
	if tid == "" {
		t.Fatalf("no ticket handle: %s", b)
	}

	// List + count.
	if s, lb := do(t, ts, "GET", "/tickets", token, ""); s != 200 || !strings.Contains(lb, "Can't log in") || !strings.Contains(lb, "# 1 ticket") {
		t.Fatalf("list tickets: %d %q", s, lb)
	}

	// Filter by status.
	if _, fb := do(t, ts, "GET", "/tickets?status=open", token, ""); !strings.Contains(fb, "Can't log in") {
		t.Fatalf("status=open should match:\n%s", fb)
	}
	if _, fb := do(t, ts, "GET", "/tickets?status=solved", token, ""); strings.Contains(fb, "Can't log in") {
		t.Fatalf("status=solved must not match an open ticket:\n%s", fb)
	}

	// Update status -> solved, addressed by the bare handle.
	if s, ub := do(t, ts, "PATCH", "/tickets/"+tid, token, `{"status":"solved"}`); s != 200 || !strings.Contains(ub, "solved") {
		t.Fatalf("update status: %d %q", s, ub)
	}

	// Invalid status is rejected.
	if s, _ := do(t, ts, "PATCH", "/tickets/"+tid, token, `{"status":"banana"}`); s != http.StatusBadRequest {
		t.Fatalf("invalid status should 400, got %d", s)
	}

	// The status change is in the record-history audit.
	if _, hist := do(t, ts, "GET", "/audit?target=ticket_"+tid, token, ""); !strings.Contains(hist, "status: open -> solved") {
		t.Fatalf("audit should record the ticket status change:\n%s", hist)
	}

	// Delete is two-step: first call gates, then confirm.
	s, gb := do(t, ts, "DELETE", "/tickets/"+tid, token, "")
	if s != http.StatusConflict || !strings.Contains(gb, "confirm=") {
		t.Fatalf("delete should gate with a confirm token: %d %q", s, gb)
	}
	i := strings.LastIndex(gb, "confirm=") + len("confirm=")
	confirm := gb[i : i+8]
	if s, _ := do(t, ts, "DELETE", "/tickets/"+tid+"?confirm="+confirm, token, ""); s != 200 {
		t.Fatalf("confirmed delete should 200, got %d", s)
	}
	if s, _ := do(t, ts, "GET", "/tickets/"+tid, token, ""); s != http.StatusNotFound {
		t.Fatalf("deleted ticket should 404, got %d", s)
	}
}

func TestTicketActivities(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, b := do(t, ts, "POST", "/tickets", token, `{"subject":"Refund"}`)
	tid := firstHandleID(b, "ticket/")
	if tid == "" {
		t.Fatalf("no ticket handle: %s", b)
	}

	// No conversation summary yet.
	if _, d := do(t, ts, "GET", "/tickets/"+tid, token, ""); strings.Contains(d, "activities:") {
		t.Fatalf("a ticket with no activities should not show a count:\n%s", d)
	}

	// Log a note onto the ticket.
	if s, ab := do(t, ts, "POST", "/tickets/"+tid+"/activities", token, `{"kind":"note","body":"Asked for the order number"}`); s != http.StatusCreated || !strings.Contains(ab, "Asked for the order number") {
		t.Fatalf("create ticket activity: %d %q", s, ab)
	}

	// It's listed under the ticket, tagged with the ticket handle...
	if _, lb := do(t, ts, "GET", "/tickets/"+tid+"/activities", token, ""); !strings.Contains(lb, "Asked for the order number") || !strings.Contains(lb, "ticket=ticket_"+tid) {
		t.Fatalf("ticket activity list missing the entry:\n%s", lb)
	}
	// ...and via the global feed filtered by ticket.
	if _, gb := do(t, ts, "GET", "/activities?ticket=ticket_"+tid, token, ""); !strings.Contains(gb, "Asked for the order number") {
		t.Fatalf("?ticket= filter missing the entry:\n%s", gb)
	}
	// ...and the detail now summarises the conversation.
	if _, d := do(t, ts, "GET", "/tickets/"+tid, token, ""); !strings.Contains(d, "activities:") || !strings.Contains(d, "last_activity:") {
		t.Fatalf("ticket detail should summarise the conversation:\n%s", d)
	}
}

func TestTicketReminders(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// A task linked to a ticket, due in the past, surfaces in reminders as overdue
	// and names the ticket via the "about" column.
	_, b := do(t, ts, "POST", "/tickets", token, `{"subject":"Chase refund"}`)
	tid := firstHandleID(b, "ticket/")
	if tid == "" {
		t.Fatalf("no ticket handle: %s", b)
	}
	_, tb := do(t, ts, "POST", "/tasks", token, `{"title":"Nudge customer","due_at":"2020-01-01T09:00:00Z","ticket_id":"`+tid+`"}`)
	taskID := firstHandleID(tb, "task/")
	if taskID == "" {
		t.Fatalf("no task handle: %s", tb)
	}

	_, rb := do(t, ts, "GET", "/reminders", token, "")
	if !strings.Contains(rb, "Nudge customer") || !strings.Contains(rb, "about=ticket_"+tid) || !strings.Contains(rb, "overdue=yes") {
		t.Fatalf("reminders should surface the overdue task about the ticket:\n%s", rb)
	}

	// Completing the task removes it from reminders.
	if s, _ := do(t, ts, "PATCH", "/tasks/"+taskID, token, `{"done":true}`); s != 200 {
		t.Fatalf("complete task: %d", s)
	}
	if _, rb := do(t, ts, "GET", "/reminders", token, ""); strings.Contains(rb, "Nudge customer") {
		t.Fatalf("completed task should not appear in reminders:\n%s", rb)
	}
}

func TestTicketInSearch(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)
	do(t, ts, "POST", "/tickets", token, `{"subject":"Zephyr login bug"}`)

	// The unified search surfaces matching tickets under their own section.
	if _, b := do(t, ts, "GET", "/search?q=Zephyr", token, ""); !strings.Contains(b, "# tickets") || !strings.Contains(b, "Zephyr login bug") {
		t.Fatalf("search should include matching tickets:\n%s", b)
	}
	// ...and respects the ?types= scope.
	if _, b := do(t, ts, "GET", "/search?q=Zephyr&types=contacts", token, ""); strings.Contains(b, "Zephyr login bug") {
		t.Fatalf("types=contacts must not return tickets:\n%s", b)
	}
}

func TestTicketConcurrency(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, b := do(t, ts, "POST", "/tickets", token, `{"subject":"Billing question"}`)
	tid := firstHandleID(b, "ticket/")
	if tid == "" {
		t.Fatalf("no ticket handle: %s", b)
	}

	// Conditional update at version 1 succeeds; a second still claiming 1 is stale.
	if s, _ := do(t, ts, "PATCH", "/tickets/"+tid, token, `{"version":1,"status":"pending"}`); s != 200 {
		t.Fatalf("matching conditional update should 200, got %d", s)
	}
	if s, cb := do(t, ts, "PATCH", "/tickets/"+tid, token, `{"version":1,"status":"solved"}`); s != http.StatusPreconditionFailed || !strings.Contains(cb, "version_conflict") {
		t.Fatalf("stale update should 412 version_conflict, got %d %q", s, cb)
	}
}
