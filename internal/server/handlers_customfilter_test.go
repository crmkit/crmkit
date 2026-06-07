package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestCustomFieldFilter(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	mk := func(body string) {
		if status, b := do(t, ts, "POST", "/companies", token, body); status != http.StatusCreated {
			t.Fatalf("create company: %d %q", status, b)
		}
	}
	mk(`{"name":"Acme","domain":"acme.test","custom":{"region":"emea","arr_stage":"seed"}}`)
	mk(`{"name":"Beta","domain":"beta.test","custom":{"region":"amer"}}`)
	mk(`{"name":"Gamma","domain":"gamma.test"}`) // no custom

	// Exact match on a custom key.
	if _, b := do(t, ts, "GET", "/companies?custom.region=emea", token, ""); !strings.Contains(b, "Acme") || strings.Contains(b, "Beta") {
		t.Fatalf("custom.region=emea should return only Acme:\n%s", b)
	}
	if _, b := do(t, ts, "GET", "/companies?custom.arr_stage=seed", token, ""); !strings.Contains(b, "Acme") || strings.Contains(b, "Beta") {
		t.Fatalf("custom.arr_stage=seed should return only Acme:\n%s", b)
	}
	if _, b := do(t, ts, "GET", "/companies?custom.region=amer", token, ""); !strings.Contains(b, "Beta") || strings.Contains(b, "Acme") {
		t.Fatalf("custom.region=amer should return only Beta:\n%s", b)
	}

	// Contains match.
	if _, b := do(t, ts, "GET", "/companies?custom.region=like:em", token, ""); !strings.Contains(b, "Acme") || strings.Contains(b, "Beta") {
		t.Fatalf("custom.region=like:em should match emea (Acme):\n%s", b)
	}

	// A key not present on any record matches nothing (and doesn't error).
	if status, b := do(t, ts, "GET", "/companies?custom.nope=x", token, ""); status != 200 || strings.Contains(b, "Acme") || strings.Contains(b, "Beta") {
		t.Fatalf("custom.nope should match nothing: %d %q", status, b)
	}

	// An invalid key is rejected with an instructive error (not a 500/injection).
	if status, b := do(t, ts, "GET", "/companies?custom.bad-key=x", token, ""); status != http.StatusBadRequest || !strings.Contains(b, "invalid_filter") {
		t.Fatalf("custom.bad-key should 400 invalid_filter, got %d %q", status, b)
	}
}

func TestCustomFieldFilterOnDeals(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	if status, _ := do(t, ts, "POST", "/deals", token, `{"title":"Big","custom":{"source":"referral"}}`); status != http.StatusCreated {
		t.Fatalf("create deal")
	}
	if status, _ := do(t, ts, "POST", "/deals", token, `{"title":"Small","custom":{"source":"ads"}}`); status != http.StatusCreated {
		t.Fatalf("create deal 2")
	}
	if _, b := do(t, ts, "GET", "/deals?custom.source=referral", token, ""); !strings.Contains(b, "Big") || strings.Contains(b, "Small") {
		t.Fatalf("custom.source=referral should return only Big:\n%s", b)
	}
}
