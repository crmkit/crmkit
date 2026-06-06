package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestRecordStampsCreator(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts) // me@example.com

	// A client-supplied created_by is ignored - the server stamps the real actor.
	_, body := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe","created_by":"evil@attacker.com"}`)
	id := firstHandleID(body, "contact/")
	if id == "" {
		t.Fatalf("no contact id in %q", body)
	}

	_, detail := do(t, ts, "GET", "/contacts/"+id, token, "")
	if !strings.Contains(detail, "created_by:") || !strings.Contains(detail, "me@example.com") {
		t.Fatalf("detail should stamp the real creator:\n%s", detail)
	}
	if strings.Contains(detail, "evil@attacker.com") {
		t.Fatalf("client-supplied created_by must be ignored:\n%s", detail)
	}

	// Upserting the same contact (by email) with a spoofed created_by must not
	// override the original creator, even in the echoed response.
	if status, _ := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe","email":"upsert@acme.com"}`); status != http.StatusCreated {
		t.Fatalf("seed upsert contact: %d", status)
	}
	_, up := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe","email":"upsert@acme.com","created_by":"evil@attacker.com","stage":"customer"}`)
	if !strings.Contains(up, "# updated") {
		t.Fatalf("expected an upsert (# updated):\n%s", up)
	}
	if strings.Contains(up, "evil@attacker.com") || !strings.Contains(up, "me@example.com") {
		t.Fatalf("upsert must keep the real creator in the echo, not the client value:\n%s", up)
	}
}

func TestCreatedByFilter(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts) // me@example.com

	if status, _ := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe"}`); status != http.StatusCreated {
		t.Fatalf("create: %d", status)
	}

	if _, body := do(t, ts, "GET", "/contacts?created_by=me@example.com", token, ""); !strings.Contains(body, "Jane Doe") {
		t.Fatalf("created_by= should include the creator's records:\n%s", body)
	}
	if _, body := do(t, ts, "GET", "/contacts?created_by=someone@else.com", token, ""); strings.Contains(body, "Jane Doe") {
		t.Fatalf("created_by= should exclude other members' records:\n%s", body)
	}
}

func TestDetailShowsActivitySummary(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, body := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe"}`)
	id := firstHandleID(body, "contact/")
	if id == "" {
		t.Fatalf("no contact id in %q", body)
	}

	// No activities yet: the summary is omitted.
	if _, detail := do(t, ts, "GET", "/contacts/"+id, token, ""); strings.Contains(detail, "activities:") {
		t.Fatalf("a contact with no activities should not show an activities count:\n%s", detail)
	}

	if status, _ := do(t, ts, "POST", "/contacts/"+id+"/activities", token, `{"kind":"note","body":"called her"}`); status != http.StatusCreated {
		t.Fatalf("create activity: %d", status)
	}

	_, detail := do(t, ts, "GET", "/contacts/"+id, token, "")
	if !strings.Contains(detail, "activities:") || !strings.Contains(detail, "last_activity:") {
		t.Fatalf("detail should summarise activities after one is logged:\n%s", detail)
	}
}
