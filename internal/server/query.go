package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crmkit/crmkit/internal/render"
	"github.com/crmkit/crmkit/internal/store"
)

// colKind controls how a filter value is parsed and bound.
type colKind int

const (
	colText colKind = iota
	colInt
	colTime // RFC3339 in, stored as a unix BIGINT
)

type colSpec struct {
	column string
	kind   colKind
}

// queryConfig is the per-entity whitelist: which fields can be filtered/sorted,
// which columns a fuzzy `search` covers, and the default sort. Mapping user
// field names to real columns here is what keeps the query layer injection-safe.
type queryConfig struct {
	filter  map[string]colSpec
	sortBy  map[string]colSpec
	search  []string
	defSort string
}

var reservedParams = map[string]bool{
	"sort": true, "limit": true, "cursor": true, "search": true, "format": true,
}

// opToSQL maps the user operator token to a SQL operator. "LIKE" is a sentinel
// the store maps to LIKE/ILIKE per dialect. "is"/"not" are null checks: written
// field=is:null and field=not:null.
var opToSQL = map[string]string{
	"eq": "=", "ne": "!=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<=",
	"like": "LIKE", "in": "IN", "is": "IS NULL", "not": "IS NOT NULL",
}

// queryError is a 400-worthy validation error with an instructive hint.
type queryError struct{ code, hint string }

func (e *queryError) Error() string { return e.code }

// parseListQuery turns request query params into a validated store.Query,
// rejecting unknown fields/operators/values with instructive errors.
func parseListQuery(r *http.Request, cfg queryConfig) (store.Query, error) {
	var q store.Query
	qv := r.URL.Query()

	q.Limit = render.Int(qv.Get("limit"), 50)
	if q.Limit < 1 {
		q.Limit = 1
	}
	if q.Limit > 200 {
		q.Limit = 200
	}

	cur, err := store.DecodeCursor(qv.Get("cursor"))
	if err != nil {
		return q, &queryError{"invalid_cursor", "The cursor is malformed. Omit it to start from the first page."}
	}
	q.Cursor = cur

	q.Search = strings.TrimSpace(qv.Get("search"))
	q.SearchColumns = cfg.search

	// A cursor carries its own sort; only parse sort when starting fresh.
	if cur == nil {
		sortParam := strings.TrimSpace(qv.Get("sort"))
		if sortParam == "" {
			q.SortColumn, q.SortDesc, q.SortNumeric = cfg.sortBy[cfg.defSort].column, true, true
		} else {
			desc := false
			if strings.HasPrefix(sortParam, "-") {
				desc, sortParam = true, sortParam[1:]
			}
			spec, ok := cfg.sortBy[sortParam]
			if !ok {
				return q, &queryError{"invalid_sort", "sort= must be one of: " + keysOf(cfg.sortBy) + " (prefix with - for descending)"}
			}
			q.SortColumn, q.SortDesc, q.SortNumeric = spec.column, desc, spec.kind != colText
		}
	}

	for field, vals := range qv {
		if reservedParams[field] || len(vals) == 0 {
			continue
		}
		spec, ok := cfg.filter[field]
		if !ok {
			return q, &queryError{"invalid_filter", "unknown filter field " + field + "; filter by: " + keysOf(cfg.filter)}
		}
		f, err := buildFilter(spec, vals[0])
		if err != nil {
			return q, err
		}
		q.Filters = append(q.Filters, f)
	}
	return q, nil
}

// buildFilter parses a "[op:]value" token into a store.QFilter.
func buildFilter(spec colSpec, raw string) (store.QFilter, error) {
	op, val := "eq", raw
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		if _, ok := opToSQL[raw[:i]]; ok {
			op, val = raw[:i], raw[i+1:]
		}
	}
	f := store.QFilter{Column: spec.column, Op: opToSQL[op]}

	switch op {
	case "is", "not":
		if !strings.EqualFold(val, "null") {
			return f, &queryError{"invalid_value", spec.column + "=" + op + ": only supports null (use " + op + ":null)"}
		}
		return f, nil
	case "in":
		for _, p := range strings.Split(val, ",") {
			v, err := coerce(spec, strings.TrimSpace(p))
			if err != nil {
				return f, err
			}
			f.Values = append(f.Values, v)
		}
		return f, nil
	case "like":
		if spec.kind != colText {
			return f, &queryError{"invalid_op", "like is only valid on text fields"}
		}
		f.Value = "%" + val + "%"
		return f, nil
	default:
		v, err := coerce(spec, val)
		if err != nil {
			return f, err
		}
		f.Value = v
		return f, nil
	}
}

func coerce(spec colSpec, val string) (any, error) {
	switch spec.kind {
	case colInt:
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return nil, &queryError{"invalid_value", spec.column + " expects an integer"}
		}
		return n, nil
	case colTime:
		t, err := time.Parse(time.RFC3339, val)
		if err != nil {
			return nil, &queryError{"invalid_value", spec.column + " expects an RFC3339 timestamp like 2026-01-31T00:00:00Z"}
		}
		return t.Unix(), nil
	default:
		return val, nil
	}
}

func keysOf(m map[string]colSpec) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

// writeQueryError renders a parse/validation failure as a 400.
func (s *Server) writeQueryError(w http.ResponseWriter, r *http.Request, err error) {
	if qe, ok := err.(*queryError); ok {
		render.Error(w, r, http.StatusBadRequest, qe.code, qe.hint)
		return
	}
	render.Error(w, r, http.StatusBadRequest, "bad_request", err.Error())
}

// respondList writes a paginated list: plain-text lines + a "# next:" cursor
// trailer, or JSON {items, next_cursor}.
func (s *Server) respondList(w http.ResponseWriter, r *http.Request, items any, text, next string) {
	if next != "" {
		text += "\n# next: " + next
	}
	render.Respond(w, r, http.StatusOK, map[string]any{"items": items, "next_cursor": next}, text)
}

// ---- per-entity whitelists -----------------------------------------------

var contactQuery = queryConfig{
	filter: map[string]colSpec{
		"name": {"name", colText}, "email": {"email", colText}, "phone": {"phone", colText},
		"company_id": {"company_id", colText}, "owner": {"owner", colText}, "stage": {"stage", colText},
		"created_at": {"created_at", colTime}, "updated_at": {"updated_at", colTime}, "follow_up_at": {"follow_up_at", colTime},
	},
	sortBy: map[string]colSpec{
		"created_at": {"created_at", colTime}, "updated_at": {"updated_at", colTime}, "name": {"name", colText},
	},
	search:  []string{"name", "email", "phone", "company_id", "stage"},
	defSort: "updated_at",
}

var companyQuery = queryConfig{
	filter: map[string]colSpec{
		"name": {"name", colText}, "domain": {"domain", colText},
		"created_at": {"created_at", colTime}, "updated_at": {"updated_at", colTime},
	},
	sortBy: map[string]colSpec{
		"created_at": {"created_at", colTime}, "updated_at": {"updated_at", colTime}, "name": {"name", colText},
	},
	search:  []string{"name", "domain"},
	defSort: "updated_at",
}

var dealQuery = queryConfig{
	filter: map[string]colSpec{
		"title": {"title", colText}, "stage": {"stage", colText}, "status": {"status", colText},
		"contact_id": {"contact_id", colText}, "company_id": {"company_id", colText}, "currency": {"currency", colText},
		"amount_cents": {"amount_cents", colInt},
		"created_at":   {"created_at", colTime}, "updated_at": {"updated_at", colTime}, "follow_up_at": {"follow_up_at", colTime},
	},
	sortBy: map[string]colSpec{
		"created_at": {"created_at", colTime}, "updated_at": {"updated_at", colTime},
		"amount_cents": {"amount_cents", colInt}, "title": {"title", colText},
	},
	search:  []string{"title"},
	defSort: "updated_at",
}
