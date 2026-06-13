package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestTaskCRUD(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// A contact and a deal to link the task to.
	_, cb := do(t, ts, "POST", "/contacts", token, `{"name":"Jane","email":"jane@acme.com"}`)
	cid := firstHandleID(cb, "contact/")
	_, db := do(t, ts, "POST", "/deals", token, `{"title":"Renewal"}`)
	did := firstHandleID(db, "deal/")
	if cid == "" || did == "" {
		t.Fatalf("setup handles: contact=%q deal=%q", cid, did)
	}

	// Create a task linked to both the contact and the deal.
	s, b := do(t, ts, "POST", "/tasks", token,
		`{"title":"Send quote","due_at":"2026-06-20T09:00:00Z","assignee":"agent@x.com","contact_id":"contact_`+cid+`","deal_id":"deal_`+did+`"}`)
	if s != http.StatusCreated {
		t.Fatalf("create task: %d %q", s, b)
	}
	for _, want := range []string{"Send quote", "agent@x.com", "contact_" + cid, "deal_" + did} {
		if !strings.Contains(b, want) {
			t.Fatalf("create response missing %q:\n%s", want, b)
		}
	}
	tid := firstHandleID(b, "task/")
	if tid == "" {
		t.Fatalf("no task handle: %s", b)
	}

	// Title is required.
	if s, _ := do(t, ts, "POST", "/tasks", token, `{"due_at":"2026-06-20T09:00:00Z"}`); s != http.StatusBadRequest {
		t.Fatalf("missing title should 400, got %d", s)
	}

	// List shows the open task.
	if _, body := do(t, ts, "GET", "/tasks", token, ""); !strings.Contains(body, "Send quote") || !strings.Contains(body, "# 1 task(s), 1 open") {
		t.Fatalf("list tasks:\n%s", body)
	}

	// Complete it via the {"done":true} convenience; done_at is stamped.
	// done_at is only rendered once the task is completed.
	if s, b := do(t, ts, "PATCH", "/tasks/"+tid, token, `{"done":true}`); s != http.StatusOK || !strings.Contains(b, "done_at:") {
		t.Fatalf("complete task: %d %q", s, b)
	}

	// The completion is in the record-history audit (status: open -> done).
	if _, hist := do(t, ts, "GET", "/audit?target=task_"+tid, token, ""); !strings.Contains(hist, "status: open -> done") {
		t.Fatalf("audit should record the task completion:\n%s", hist)
	}

	// Filter to open tasks (done_at=is:null) - the completed one is excluded.
	if _, body := do(t, ts, "GET", "/tasks?done_at=is:null", token, ""); !strings.Contains(body, "# 0 task(s)") {
		t.Fatalf("open filter should exclude the completed task:\n%s", body)
	}

	// Reopen it.
	if s, _ := do(t, ts, "PATCH", "/tasks/"+tid, token, `{"done":false}`); s != http.StatusOK {
		t.Fatalf("reopen task: %d", s)
	}
	if _, body := do(t, ts, "GET", "/tasks?done_at=is:null", token, ""); !strings.Contains(body, "# 1 task(s)") {
		t.Fatalf("reopened task should be open again:\n%s", body)
	}

	// Delete is two-step: first call gates with a confirm token, then confirm.
	s, gb := do(t, ts, "DELETE", "/tasks/"+tid, token, "")
	if s != http.StatusConflict || !strings.Contains(gb, "confirm=") {
		t.Fatalf("delete should gate with a confirm token: %d %q", s, gb)
	}
	i := strings.LastIndex(gb, "confirm=") + len("confirm=")
	confirm := gb[i : i+8]
	if s, _ := do(t, ts, "DELETE", "/tasks/"+tid+"?confirm="+confirm, token, ""); s != http.StatusOK {
		t.Fatalf("confirmed delete should 200, got %d", s)
	}
}
