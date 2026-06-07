package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestCompanyNotesSearchable(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, body := do(t, ts, "POST", "/companies", token,
		`{"name":"ZipChat AI","domain":"zipchat.ai","tags":["competitor"],"notes":"AI-powered chatbot for ecommerce businesses on Shopify"}`)
	id := firstHandleID(body, "company/")
	if id == "" {
		t.Fatalf("no company id in %q", body)
	}

	// notes round-trip on the detail.
	if _, d := do(t, ts, "GET", "/companies/"+id, token, ""); !strings.Contains(d, "notes:") || !strings.Contains(d, "ecommerce") {
		t.Fatalf("detail should show notes:\n%s", d)
	}

	// notes are now part of company fuzzy search (was name+domain only).
	if _, b := do(t, ts, "GET", "/companies?search=ecommerce", token, ""); !strings.Contains(b, "ZipChat AI") {
		t.Fatalf("?search=ecommerce should find the company by its notes:\n%s", b)
	}
	if _, b := do(t, ts, "GET", "/companies?search=Shopify", token, ""); !strings.Contains(b, "ZipChat AI") {
		t.Fatalf("?search=Shopify should match notes (case-insensitive):\n%s", b)
	}
}

func TestCompanyActivities(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, body := do(t, ts, "POST", "/companies", token, `{"name":"Nalvin","domain":"nalvin.com"}`)
	id := firstHandleID(body, "company/")
	if id == "" {
		t.Fatalf("no company id in %q", body)
	}

	// Detail shows no activity summary yet.
	if _, d := do(t, ts, "GET", "/companies/"+id, token, ""); strings.Contains(d, "activities:") {
		t.Fatalf("a company with no activities should not show an activities count:\n%s", d)
	}

	// Log a monitoring observation against the company.
	if s, b := do(t, ts, "POST", "/companies/"+id+"/activities", token, `{"kind":"note","body":"Raised a seed round"}`); s != http.StatusCreated || !strings.Contains(b, "Raised a seed round") {
		t.Fatalf("create company activity: %d %q", s, b)
	}

	// It's listed under the company...
	if _, b := do(t, ts, "GET", "/companies/"+id+"/activities", token, ""); !strings.Contains(b, "Raised a seed round") || !strings.Contains(b, "company="+id) {
		t.Fatalf("company activity list missing the entry:\n%s", b)
	}
	// ...and via the global feed filtered by company.
	if _, b := do(t, ts, "GET", "/activities?company="+id, token, ""); !strings.Contains(b, "Raised a seed round") {
		t.Fatalf("?company= filter missing the entry:\n%s", b)
	}
	// ...and the detail now summarises it.
	if _, d := do(t, ts, "GET", "/companies/"+id, token, ""); !strings.Contains(d, "activities:") || !strings.Contains(d, "last_activity:") {
		t.Fatalf("detail should summarise activities after one is logged:\n%s", d)
	}
}
