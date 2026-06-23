package server

import (
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
)

// plainIdentRe is a lowercase SQL identifier with no metacharacters - the only
// shape a bare identifier interpolated into a query may take.
var plainIdentRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// safeColumn reports whether an interpolated filter column is permitted: either a
// plain identifier or one of the vetted, code-controlled derived expressions
// (derivedExprs). Both are free of user input, which is the property that keeps
// interpolating them injection-safe.
func safeColumn(col string) bool {
	return plainIdentRe.MatchString(col) || derivedExprs[col]
}

// TestQueryIdentifiersAndOpsAreSafe is the structural guard: it asserts the
// PROPERTY the injection tests sample for. buildListSQL interpolates only
// identifiers drawn from these whitelists (filter/sort/search columns) and
// operators from opToSQL; everything else is a bound parameter. If anyone adds a
// filter/sort field whose column carries SQL metacharacters, or an operator that
// isn't a known SQL operator, this fails - regardless of which payloads the
// example-based tests happen to try.
func TestQueryIdentifiersAndOpsAreSafe(t *testing.T) {
	configs := map[string]queryConfig{"contacts": contactQuery, "companies": companyQuery, "deals": dealQuery}
	for name, cfg := range configs {
		for field, spec := range cfg.filter {
			if !safeColumn(spec.column) {
				t.Errorf("%s: filter %q maps to unsafe column %q", name, field, spec.column)
			}
		}
		for field, spec := range cfg.sortBy {
			if !plainIdentRe.MatchString(spec.column) {
				t.Errorf("%s: sort %q maps to non-identifier column %q", name, field, spec.column)
			}
		}
		for _, col := range cfg.search {
			if !plainIdentRe.MatchString(col) {
				t.Errorf("%s: search column %q is not a plain identifier", name, col)
			}
		}
	}

	// Operators are interpolated into "<col> <op> ?", so they must be from a known
	// set, never derived from user text.
	allowedOps := map[string]bool{
		"=": true, "!=": true, ">": true, ">=": true, "<": true, "<=": true,
		"LIKE": true, "IN": true, "IS NULL": true, "IS NOT NULL": true,
	}
	for token, op := range opToSQL {
		if !allowedOps[op] {
			t.Errorf("opToSQL[%q] = %q is not an allowed SQL operator", token, op)
		}
	}
}

// FuzzParseListQuery throws arbitrary field/value pairs at the query parser and
// asserts the invariant that protects against injection: any filter it produces
// references only a plain-identifier column (a bound JSON key is validated), and
// any cursor uses a whitelisted sortable column. Anything else must be a rejected
// error, never a silently unsafe Query. Run as a normal test it exercises the
// seed corpus; `go test -fuzz` explores further.
func FuzzParseListQuery(f *testing.F) {
	for _, s := range [][2]string{
		{"stage", "lead"},
		{"custom.region", "emea"},
		{"custom.region", "like:em"},
		{"sort", "-name"},
		{"cursor", "deadbeef"},
		{"tags", "vip,competitor"},
		{"email", "like:a%b"},
		{"amount_cents", "gte:100"},
		{"'; DROP TABLE contacts;--", "1"},
		{"custom.bad key", "v"},
		{"custom.x'); DROP", "v"},
	} {
		f.Add(s[0], s[1])
	}

	f.Fuzz(func(t *testing.T, field, value string) {
		vals := url.Values{}
		vals.Set(field, value)
		r := httptest.NewRequest("GET", "/?"+vals.Encode(), nil)

		q, err := parseListQuery(r, contactQuery)
		if err != nil {
			return // rejected input is the safe outcome
		}
		for _, flt := range q.Filters {
			if !safeColumn(flt.Column) {
				t.Fatalf("parse produced unsafe filter column %q from %q=%q", flt.Column, field, value)
			}
			if flt.JSONKey != "" && !customKeyRe.MatchString(flt.JSONKey) {
				t.Fatalf("parse produced unvalidated JSON key %q from %q=%q", flt.JSONKey, field, value)
			}
		}
		if q.Cursor != nil && !contactQuery.sortableColumn(q.Cursor.Col) {
			t.Fatalf("parse produced cursor with non-sortable column %q from %q=%q", q.Cursor.Col, field, value)
		}
	})
}
