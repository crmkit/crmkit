package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/render"
	"github.com/crmkit/crmkit/internal/store"
)

// searchPerType caps how many hits each entity type contributes to a /search
// response. Cross-entity search is a "jump to it" affordance, not a paginated
// list - when a type is truncated the response points at the typed endpoint
// (e.g. GET /contacts?search=) for the full, paginated view.
const searchPerType = 5

// handleSearch runs a fuzzy term across contacts, companies and deals at once -
// the "find anything" entry point - and returns grouped results. Scope it with
// ?types=contacts,companies,deals (default: all three).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	term := strings.TrimSpace(r.URL.Query().Get("q"))
	if term == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_query",
			`Pass a search term, e.g. /search?q=acme. Optionally scope with &types=contacts,companies,deals.`)
		return
	}
	want := parseSearchTypes(r.URL.Query().Get("types"))

	contacts := []protocol.Contact{}
	companies := []protocol.Company{}
	deals := []protocol.Deal{}

	if want["contacts"] {
		list, _, err := s.store.QueryContacts(sess.WorkspaceID, searchQuery(contactQuery, term))
		if err != nil {
			s.serverErr(w, r)
			return
		}
		contacts = list
	}
	if want["companies"] {
		list, _, err := s.store.QueryCompanies(sess.WorkspaceID, searchQuery(companyQuery, term))
		if err != nil {
			s.serverErr(w, r)
			return
		}
		companies = list
	}
	if want["deals"] {
		list, _, err := s.store.QueryDeals(sess.WorkspaceID, searchQuery(dealQuery, term))
		if err != nil {
			s.serverErr(w, r)
			return
		}
		deals = list
	}

	loc := locationOf(sess)
	contacts = localizedSlice(contacts, loc)
	companies = localizedSlice(companies, loc)
	deals = localizedSlice(deals, loc)

	text := render.SearchResults(term, contacts, companies, deals)
	if len(contacts) == searchPerType || len(companies) == searchPerType || len(deals) == searchPerType {
		text += "\n# tip: showing up to " + strconv.Itoa(searchPerType) +
			" per type; narrow with the typed endpoint, e.g. GET /contacts?search=" + url.QueryEscape(term)
	}
	render.Respond(w, r, http.StatusOK, map[string]any{
		"query":     term,
		"contacts":  contacts,
		"companies": companies,
		"deals":     deals,
	}, text)
}

// searchQuery builds a search-only store.Query for a type, reusing that type's
// whitelisted search columns and default sort (newest first), capped per type.
func searchQuery(cfg queryConfig, term string) store.Query {
	return store.Query{
		Search:        term,
		SearchColumns: cfg.search,
		SortColumn:    cfg.sortBy[cfg.defSort].column,
		SortDesc:      true,
		SortNumeric:   true,
		Limit:         searchPerType,
	}
}

// parseSearchTypes resolves the optional ?types= scope to the set of entity
// types to search. Empty or all-unrecognized falls back to all three, so a
// malformed scope still returns useful results rather than nothing.
func parseSearchTypes(raw string) map[string]bool {
	all := map[string]bool{"contacts": true, "companies": true, "deals": true}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return all
	}
	want := map[string]bool{}
	for _, t := range strings.Split(raw, ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); all[t] {
			want[t] = true
		}
	}
	if len(want) == 0 {
		return all
	}
	return want
}
