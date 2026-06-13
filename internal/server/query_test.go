package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func nextCursor(body string) string {
	const m = "# next: "
	i := strings.Index(body, m)
	if i < 0 {
		return ""
	}
	s := body[i+len(m):]
	if j := strings.IndexByte(s, '\n'); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

func TestQueryFiltersAndSort(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := loginAs(t, ts, "alice@acme.com")

	do(t, ts, "POST", "/deals", tok, `{"title":"Small","amount_cents":1000,"stage":"proposal","status":"open"}`)
	do(t, ts, "POST", "/deals", tok, `{"title":"Big","amount_cents":5000,"stage":"won","status":"won"}`)
	do(t, ts, "POST", "/deals", tok, `{"title":"Mid","amount_cents":3000,"stage":"proposal","status":"open"}`)

	// Exact filter.
	if _, body := do(t, ts, "GET", "/deals?status=open", tok, ""); !strings.Contains(body, "# 2 deal") {
		t.Fatalf("status=open should match 2: %q", body)
	}
	// Numeric comparison.
	if _, body := do(t, ts, "GET", "/deals?amount_cents=gte:3000", tok, ""); !strings.Contains(body, "# 2 deal") {
		t.Fatalf("amount>=3000 should match 2: %q", body)
	}
	// Combined AND.
	if _, body := do(t, ts, "GET", "/deals?status=open&amount_cents=gte:2000", tok, ""); !strings.Contains(body, "# 1 deal") || !strings.Contains(body, "Mid") {
		t.Fatalf("status=open AND amount>=2000 should match only Mid: %q", body)
	}
	// in: operator.
	if _, body := do(t, ts, "GET", "/deals?stage=in:proposal,won", tok, ""); !strings.Contains(body, "# 3 deal") {
		t.Fatalf("stage in (proposal,won) should match 3: %q", body)
	}
	// Sort descending by amount: Big first.
	if _, body := do(t, ts, "GET", "/deals?sort=-amount_cents", tok, ""); !strings.HasPrefix(strings.TrimSpace(body), "deal_") || !strings.Contains(strings.Split(body, "\n")[0], "Big") {
		t.Fatalf("sort=-amount_cents should put Big first: %q", body)
	}
}

func TestQueryNullChecks(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := loginAs(t, ts, "alice@acme.com")

	// One task with a due date, one without.
	do(t, ts, "POST", "/tasks", tok, `{"title":"Due","due_at":"2026-01-01T00:00:00Z"}`)
	do(t, ts, "POST", "/tasks", tok, `{"title":"None"}`)

	if _, body := do(t, ts, "GET", "/tasks?due_at=not:null", tok, ""); !strings.Contains(body, "# 1 task") || !strings.Contains(body, "Due") {
		t.Fatalf("not:null should match the one with a due date: %q", body)
	}
	if _, body := do(t, ts, "GET", "/tasks?due_at=is:null", tok, ""); !strings.Contains(body, "# 1 task") || !strings.Contains(body, "None") {
		t.Fatalf("is:null should match the one without: %q", body)
	}
	// is/not only accept null.
	if st, body := do(t, ts, "GET", "/tasks?due_at=is:today", tok, ""); st != http.StatusBadRequest || !strings.Contains(body, "invalid_value") {
		t.Fatalf("is:today should 400, got %d %q", st, body)
	}
}

func TestQueryWhitelistRejects(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := loginAs(t, ts, "alice@acme.com")

	cases := []struct {
		path, code string
	}{
		{"/contacts?bogus=1", "invalid_filter"},
		{"/contacts?sort=bogus", "invalid_sort"},
		{"/deals?amount_cents=gte:notanumber", "invalid_value"},
		{"/contacts?cursor=not-base64!!", "invalid_cursor"},
	}
	for _, c := range cases {
		st, body := do(t, ts, "GET", c.path, tok, "")
		if st != http.StatusBadRequest || !strings.Contains(body, c.code) {
			t.Fatalf("%s: want 400 %s, got %d %q", c.path, c.code, st, body)
		}
	}
}

func TestQueryKeysetPagination(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := loginAs(t, ts, "alice@acme.com")

	// Create 5 distinct contacts.
	for i := 0; i < 5; i++ {
		do(t, ts, "POST", "/contacts", tok, fmt.Sprintf(`{"name":"C%d","email":"c%d@acme.com"}`, i, i))
	}

	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		path := "/contacts?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		st, body := do(t, ts, "GET", path, tok, "")
		if st != 200 {
			t.Fatalf("page %d: %d %q", page, st, body)
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "contact_") {
				id := firstHandleID(line, "contact/")
				if seen[id] {
					t.Fatalf("duplicate row across pages: %s", id)
				}
				seen[id] = true
			}
		}
		cursor = nextCursor(body)
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("expected 5 distinct contacts across pages, got %d", len(seen))
	}
}
