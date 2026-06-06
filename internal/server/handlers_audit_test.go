package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuditAttributesActor(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts) // me@example.com

	if status, _ := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe"}`); status != http.StatusCreated {
		t.Fatalf("create contact: %d", status)
	}

	// The audit feed names who acted.
	status, body := do(t, ts, "GET", "/audit", token, "")
	if status != http.StatusOK {
		t.Fatalf("audit: %d %q", status, body)
	}
	if !strings.Contains(body, "by=me@example.com") || !strings.Contains(body, "action=contact.create") {
		t.Fatalf("audit should attribute the actor:\n%s", body)
	}

	// Filtering by the acting member returns the entry...
	if _, body := do(t, ts, "GET", "/audit?by=me@example.com", token, ""); !strings.Contains(body, "action=contact.create") {
		t.Fatalf("?by=actor should include their actions:\n%s", body)
	}
	// ...and filtering by someone else excludes it.
	if _, body := do(t, ts, "GET", "/audit?by=someone@else.com", token, ""); strings.Contains(body, "action=contact.create") {
		t.Fatalf("?by=other should exclude this actor's actions:\n%s", body)
	}
}

func TestActivityShowsActor(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts) // me@example.com

	_, body := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe"}`)
	id := firstHandleID(body, "contact/")
	if id == "" {
		t.Fatalf("no contact id in %q", body)
	}

	if status, _ := do(t, ts, "POST", "/contacts/"+id+"/activities", token, `{"kind":"note","body":"called her"}`); status != http.StatusCreated {
		t.Fatalf("create activity: %d", status)
	}

	_, list := do(t, ts, "GET", "/contacts/"+id+"/activities", token, "")
	if !strings.Contains(list, "by=me@example.com") {
		t.Fatalf("activity line should attribute the actor:\n%s", list)
	}
}
