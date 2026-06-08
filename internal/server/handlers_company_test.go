package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createJSONID creates an entity via POST <path> and returns its durable
// internal id, captured from the JSON response. Tests that delete need this
// because the confirmation token is keyed off the internal id (see confirmToken).
func createJSONID(t *testing.T, ts *httptest.Server, token, path, body string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create %s: status %d %q", path, resp.StatusCode, b)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil || created.ID == "" {
		t.Fatalf("create %s: no id in response (err=%v)", path, err)
	}
	return created.ID
}

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

func TestDeleteActivity(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, b := do(t, ts, "POST", "/contacts", token, `{"name":"Jane"}`)
	cid := firstHandleID(b, "contact/")
	_, ab := do(t, ts, "POST", "/contacts/"+cid+"/activities", token, `{"kind":"note","body":"a mistake"}`)
	aid := firstHandleID(ab, "activity/")
	if aid == "" {
		t.Fatalf("no activity id in %q", ab)
	}

	// One-shot delete (no confirm step).
	if s, body := do(t, ts, "DELETE", "/activities/"+aid, token, ""); s != http.StatusOK || !strings.Contains(body, "deleted") {
		t.Fatalf("delete activity: %d %q", s, body)
	}
	// Gone from the contact's timeline.
	if _, l := do(t, ts, "GET", "/contacts/"+cid+"/activities", token, ""); strings.Contains(l, "a mistake") {
		t.Fatalf("deleted activity still listed:\n%s", l)
	}
	// Re-deleting is a 404.
	if s, _ := do(t, ts, "DELETE", "/activities/"+aid, token, ""); s != http.StatusNotFound {
		t.Fatalf("re-delete should 404, got %d", s)
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
	if _, b := do(t, ts, "GET", "/companies/"+id+"/activities", token, ""); !strings.Contains(b, "Raised a seed round") || !strings.Contains(b, "company=company_"+id) {
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

// TestCompanyUpdateAndDeleteLifecycle covers the previously-untested company
// mutation handlers end to end: get, conditional update (incl. a stale-version
// 412), and the confirm-gated delete.
func TestCompanyUpdateAndDeleteLifecycle(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	id := createJSONID(t, ts, token, "/companies", `{"name":"Acme","domain":"acme.com"}`)

	// GET returns the record.
	if s, b := do(t, ts, "GET", "/companies/"+id, token, ""); s != http.StatusOK || !strings.Contains(b, "Acme") || !strings.Contains(b, "acme.com") {
		t.Fatalf("get company: %d %q", s, b)
	}

	// Conditional update with the current version (1) succeeds and changes the name.
	if s, b := do(t, ts, "PATCH", "/companies/"+id, token, `{"version":1,"name":"Acme Inc"}`); s != http.StatusOK || !strings.Contains(b, "Acme Inc") {
		t.Fatalf("conditional update: %d %q", s, b)
	}

	// The version is now 2, so a PATCH still claiming version 1 is stale -> 412.
	if s, b := do(t, ts, "PATCH", "/companies/"+id, token, `{"version":1,"name":"Stale"}`); s != http.StatusPreconditionFailed || !strings.Contains(b, "version_conflict") {
		t.Fatalf("stale update should 412 version_conflict, got %d %q", s, b)
	}
	if _, b := do(t, ts, "GET", "/companies/"+id, token, ""); strings.Contains(b, "Stale") {
		t.Fatalf("a conflicting write must not persist:\n%s", b)
	}

	// Delete is confirm-gated, then succeeds with the token, then 404s.
	if s, b := do(t, ts, "DELETE", "/companies/"+id, token, ""); s != http.StatusConflict || !strings.Contains(b, "confirmation_required") {
		t.Fatalf("delete should require confirmation, got %d %q", s, b)
	}
	if s, b := do(t, ts, "DELETE", "/companies/"+id+"?confirm="+confirmToken(id), token, ""); s != http.StatusOK || !strings.Contains(b, "deleted") {
		t.Fatalf("confirmed delete: %d %q", s, b)
	}
	if s, _ := do(t, ts, "GET", "/companies/"+id, token, ""); s != http.StatusNotFound {
		t.Fatalf("deleted company should 404, got %d", s)
	}
}

func TestUpsertCompanyByDomain(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := loginAs(t, ts, "alice@acme.com")

	if st, body := do(t, ts, "POST", "/companies", tok, `{"name":"Acme","domain":"acme.com"}`); st != http.StatusCreated || !strings.Contains(body, "# created") {
		t.Fatalf("create company: %d %q", st, body)
	}
	st, body := do(t, ts, "POST", "/companies", tok, `{"name":"Acme Inc","domain":"ACME.com"}`)
	if st != http.StatusOK || !strings.Contains(body, "# updated") || !strings.Contains(body, "Acme Inc") {
		t.Fatalf("upsert company: %d %q", st, body)
	}
	if _, body := do(t, ts, "GET", "/companies", tok, ""); !strings.Contains(body, "# 1 company") {
		t.Fatalf("expected 1 company, got %q", body)
	}
}
