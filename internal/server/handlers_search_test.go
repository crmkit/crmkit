package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedSearchFixtures creates one of each entity type all sharing the term
// "acme" via a different search column (company name, contact email, deal
// title) so a cross-entity search must hit all three code paths.
func seedSearchFixtures(t *testing.T, ts *httptest.Server, token string) {
	t.Helper()
	if status, body := do(t, ts, "POST", "/companies", token, `{"name":"Acme Corp","domain":"acme.com"}`); status != http.StatusCreated {
		t.Fatalf("create company: %d %q", status, body)
	}
	if status, body := do(t, ts, "POST", "/contacts", token, `{"name":"Jane Doe","email":"jane@acme.com"}`); status != http.StatusCreated {
		t.Fatalf("create contact: %d %q", status, body)
	}
	if status, body := do(t, ts, "POST", "/deals", token, `{"title":"Acme renewal"}`); status != http.StatusCreated {
		t.Fatalf("create deal: %d %q", status, body)
	}
}

func TestSearchGroupsAllTypes(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)
	seedSearchFixtures(t, ts, token)

	status, body := do(t, ts, "GET", "/search?q=acme", token, "")
	if status != http.StatusOK {
		t.Fatalf("search: %d %q", status, body)
	}
	for _, want := range []string{"# contacts", "# companies", "# deals", "Acme Corp", "Jane Doe", "Acme renewal", `for "acme"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("search text missing %q in:\n%s", want, body)
		}
	}
}

func TestSearchJSONShape(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)
	seedSearchFixtures(t, ts, token)

	status, body := do(t, ts, "GET", "/search?q=acme&format=json", token, "")
	if status != http.StatusOK {
		t.Fatalf("search json: %d %q", status, body)
	}
	var got struct {
		Query     string           `json:"query"`
		Contacts  []map[string]any `json:"contacts"`
		Companies []map[string]any `json:"companies"`
		Deals     []map[string]any `json:"deals"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if got.Query != "acme" || len(got.Contacts) != 1 || len(got.Companies) != 1 || len(got.Deals) != 1 {
		t.Fatalf("unexpected shape: %+v", got)
	}
	// Relation resolution flows through search results.
	if got.Companies[0]["name"] != "Acme Corp" {
		t.Fatalf("company name not resolved: %+v", got.Companies[0])
	}
}

func TestSearchTypesFilter(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)
	seedSearchFixtures(t, ts, token)

	status, body := do(t, ts, "GET", "/search?q=acme&types=companies", token, "")
	if status != http.StatusOK {
		t.Fatalf("scoped search: %d %q", status, body)
	}
	if !strings.Contains(body, "# companies") || strings.Contains(body, "# contacts") || strings.Contains(body, "# deals") {
		t.Fatalf("types=companies should return only the companies group:\n%s", body)
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)
	if status, body := do(t, ts, "GET", "/search", token, ""); status != http.StatusBadRequest || !strings.Contains(body, "missing_query") {
		t.Fatalf("empty q should 400 missing_query, got %d %q", status, body)
	}
}
