package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestHandlesResolveInPathsAndRelations exercises the public-handle surface end
// to end: a relation supplied as a handle resolves, and a record is addressable
// by either the bare handle or the prefixed wire form.
func TestHandlesResolveInPathsAndRelations(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// Create a company and grab its short handle.
	_, cb := do(t, ts, "POST", "/companies", token, `{"name":"Acme","domain":"acme.com"}`)
	ch := firstHandleID(cb, "company/") // bare handle
	if ch == "" {
		t.Fatalf("no company handle in %q", cb)
	}

	// Create a contact referencing the company by its public handle (prefixed form);
	// the relation must resolve to the company name in the response.
	_, b := do(t, ts, "POST", "/contacts", token, `{"name":"Jane","company_id":"company_`+ch+`"}`)
	if !strings.Contains(b, "company=Acme") && !strings.Contains(b, "company:") {
		t.Fatalf("contact should resolve the company supplied by handle:\n%s", b)
	}
	cid := firstHandleID(b, "contact/") // bare handle

	// The bare handle works as a path id...
	if s, _ := do(t, ts, "GET", "/contacts/"+cid, token, ""); s != http.StatusOK {
		t.Fatalf("bare handle path: %d", s)
	}
	// ...and so does the prefixed wire form the agent actually sees.
	if s, _ := do(t, ts, "GET", "/contacts/contact_"+cid, token, ""); s != http.StatusOK {
		t.Fatalf("prefixed handle path: %d", s)
	}
	// A bogus handle 404s rather than leaking another record.
	if s, _ := do(t, ts, "GET", "/contacts/zzzzz", token, ""); s != http.StatusNotFound {
		t.Fatalf("bogus handle should 404, got %d", s)
	}
}
