package render

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crmkit/crmkit/internal/protocol"
)

func TestWantJSON(t *testing.T) {
	cases := []struct {
		accept string
		query  string
		want   bool
	}{
		{"", "", false},
		{"text/plain", "", false},
		{"application/json", "", true},
		{"application/json, text/plain", "", true},
		{"", "format=json", true},
		{"application/json", "format=text", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/contacts?"+c.query, nil)
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		if got := WantJSON(r); got != c.want {
			t.Errorf("accept=%q query=%q: got %v want %v", c.accept, c.query, got, c.want)
		}
	}
}

func TestLineOmitsEmptyAndQuotes(t *testing.T) {
	got := Line("contact/c_1",
		F("name", "Jane Doe"),
		F("email", "jane@acme.com"),
		F("phone", ""),
	)
	if !strings.Contains(got, `name="Jane Doe"`) {
		t.Errorf("expected quoted name, got %q", got)
	}
	if strings.Contains(got, "phone=") {
		t.Errorf("expected empty phone omitted, got %q", got)
	}
	if !strings.HasPrefix(got, "contact/c_1") {
		t.Errorf("expected handle prefix, got %q", got)
	}
}

func TestContactLineGrepable(t *testing.T) {
	c := protocol.Contact{ID: "c_1", Name: "Jane", Email: "jane@acme.com", Stage: "lead"}
	line := ContactLine(c)
	if !strings.Contains(line, "stage=lead") || !strings.Contains(line, "email=jane@acme.com") {
		t.Errorf("contact line missing fields: %q", line)
	}
}

func TestRecordPadsKeys(t *testing.T) {
	got := Record(F("id", "c_1"), F("name", "Jane"), F("blank", ""))
	if strings.Contains(got, "blank") {
		t.Errorf("blank field should be omitted: %q", got)
	}
	if !strings.Contains(got, "id:") || !strings.Contains(got, "name:") {
		t.Errorf("record missing keys: %q", got)
	}
}
