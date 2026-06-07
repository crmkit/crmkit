package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestCompanyTagsAndFilter(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	mk := func(body string) {
		if status, b := do(t, ts, "POST", "/companies", token, body); status != http.StatusCreated {
			t.Fatalf("create company: %d %q", status, b)
		}
	}
	mk(`{"name":"Acme","domain":"acme.test","tags":["competitor","fintech"]}`)
	mk(`{"name":"Beta","domain":"beta.test","tags":["watchlist"]}`)
	mk(`{"name":"Gamma","domain":"gamma.test"}`) // no tags

	// Tags round-trip on the detail.
	_, list := do(t, ts, "GET", "/companies?tags=competitor", token, "")
	id := firstHandleID(list, "company/")
	if id == "" {
		t.Fatalf("no company id in %q", list)
	}
	if _, detail := do(t, ts, "GET", "/companies/"+id, token, ""); !strings.Contains(detail, "tags:") || !strings.Contains(detail, "competitor") {
		t.Fatalf("detail should show tags:\n%s", detail)
	}

	// Single-tag filter.
	if _, b := do(t, ts, "GET", "/companies?tags=competitor", token, ""); !strings.Contains(b, "Acme") || strings.Contains(b, "Beta") {
		t.Fatalf("?tags=competitor should return only Acme:\n%s", b)
	}
	if _, b := do(t, ts, "GET", "/companies?tags=watchlist", token, ""); !strings.Contains(b, "Beta") || strings.Contains(b, "Acme") {
		t.Fatalf("?tags=watchlist should return only Beta:\n%s", b)
	}

	// A discrete tag must not match a substring of another value.
	if _, b := do(t, ts, "GET", "/companies?tags=comp", token, ""); strings.Contains(b, "Acme") {
		t.Fatalf("?tags=comp must not match the 'competitor' tag:\n%s", b)
	}

	// Multiple tags are AND-ed (record must carry all).
	if _, b := do(t, ts, "GET", "/companies?tags=competitor,fintech", token, ""); !strings.Contains(b, "Acme") {
		t.Fatalf("?tags=competitor,fintech should include Acme (has both):\n%s", b)
	}
	if _, b := do(t, ts, "GET", "/companies?tags=competitor,watchlist", token, ""); strings.Contains(b, "Acme") || strings.Contains(b, "Beta") {
		t.Fatalf("?tags=competitor,watchlist should match nothing:\n%s", b)
	}
}

func TestContactTagsFilterable(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	if status, _ := do(t, ts, "POST", "/contacts", token, `{"name":"Jane","email":"jane@x.test","tags":["vip"]}`); status != http.StatusCreated {
		t.Fatalf("create contact")
	}
	if status, _ := do(t, ts, "POST", "/contacts", token, `{"name":"John","email":"john@x.test"}`); status != http.StatusCreated {
		t.Fatalf("create contact 2")
	}
	if _, b := do(t, ts, "GET", "/contacts?tags=vip", token, ""); !strings.Contains(b, "Jane") || strings.Contains(b, "John") {
		t.Fatalf("?tags=vip should return only Jane:\n%s", b)
	}
}
