package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/render"
	"github.com/crmkit/crmkit/internal/store"
)

// confirmToken derives a deterministic, stateless confirmation token for a
// destructive action on the given id. Agents echo it back as ?confirm=<token>.
func confirmToken(id string) string {
	sum := sha256.Sum256([]byte("crmkit:delete:" + id))
	return hex.EncodeToString(sum[:])[:8]
}

// requireConfirm checks the ?confirm token for a destructive request. It writes
// a 409 with the expected token and returns false when confirmation is missing.
// The confirm token is keyed off the durable internal id (stable regardless of
// how the record was referenced); the message echoes the agent's own reference
// (the short handle it used) so it reads naturally and stays token-cheap.
func (s *Server) requireConfirm(w http.ResponseWriter, r *http.Request, kind, id string) bool {
	want := confirmToken(id)
	if strings.TrimSpace(r.URL.Query().Get("confirm")) == want {
		return true
	}
	ref := r.PathValue("id")
	if ref == "" {
		ref = protocol.FormatRef(kind, id)
	}
	render.Error(w, r, http.StatusConflict, "confirmation_required",
		"Deleting "+ref+" is irreversible. Confirm with the user, then repeat with ?confirm="+want)
	return false
}

// notFound writes a 404 for a missing entity.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request, kind string) {
	render.Error(w, r, http.StatusNotFound, "not_found",
		"No "+kind+" with that id in your workspace. List them first (GET /"+kind+"s) to find the right handle.")
}

func (s *Server) serverErr(w http.ResponseWriter, r *http.Request) {
	render.Error(w, r, http.StatusInternalServerError, "server_error", "Try again shortly.")
}

// resolveID maps a path reference (a short handle in any representation, or a raw
// internal id) to the internal id for a kind. On failure it writes a 404 and
// returns ok=false, so callers `if !ok { return }`.
func (s *Server) resolveID(w http.ResponseWriter, r *http.Request, kind string) (string, bool) {
	id, err := s.store.ResolveHandle(sessionFrom(r).WorkspaceID, kind, r.PathValue("id"))
	if err != nil {
		s.notFound(w, r, kind)
		return "", false
	}
	return id, true
}

// relationID maps a relation reference from a request body (a handle, or a raw
// id) to the internal id, leniently: a blank or unresolvable value is returned
// as-is, so the render shows it as an unresolved fallback rather than failing.
func (s *Server) relationID(ws, kind, ref string) string {
	if strings.TrimSpace(ref) == "" {
		return ""
	}
	if id, err := s.store.ResolveHandle(ws, kind, ref); err == nil {
		return id
	}
	return ref
}

// relationIDs resolves a comma-separated list of relation references (handles or
// raw ids) to internal ids, dropping blanks. Powers multi-id activity filters like
// ?company=company_a,company_b - "the activity for any of these records".
func (s *Server) relationIDs(ws, kind, raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, ref := range strings.Split(raw, ",") {
		if id := s.relationID(ws, kind, strings.TrimSpace(ref)); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// auditKinds is the set of entity kinds whose audit targets can be filtered by
// a record reference.
var auditKinds = map[string]bool{
	protocol.KindContact: true,
	protocol.KindCompany: true,
	protocol.KindDeal:    true,
	protocol.KindTicket:  true,
	protocol.KindTask:    true,
}

// resolveAuditTarget turns a record reference (a handle like "contact_k7m2q")
// into the stored audit target form ("contact/<internal-id>") so audit can be
// filtered to one record. A blank or unresolvable ref yields "" (no filter); an
// already-internal "kind/id" target is matched literally.
func (s *Server) resolveAuditTarget(ws, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	kind := ref
	if i := strings.IndexByte(ref, '_'); i >= 0 {
		kind = ref[:i]
	}
	if auditKinds[kind] {
		if id, err := s.store.ResolveHandle(ws, kind, ref); err == nil {
			return protocol.Handle(kind, id)
		}
	}
	return ref
}

// expectedVersion returns the record version the client expects, for a
// conditional (optimistic-concurrency) update, or 0 when none was supplied
// (meaning "no check - last write wins"). It reads the If-Match header first,
// then a "version" field in the JSON body, so agents can use either channel.
func expectedVersion(r *http.Request, body []byte) int64 {
	if m := strings.Trim(r.Header.Get("If-Match"), `" `); m != "" && m != "*" {
		if n, err := strconv.ParseInt(m, 10, 64); err == nil {
			return n
		}
	}
	var probe struct {
		Version int64 `json:"version"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Version
}

// writeUpdateErr maps a store update error to the right response and returns true
// when it wrote one. A version conflict is 412 (the If-Match precondition failed);
// the message tells the agent how to recover.
func (s *Server) writeUpdateErr(w http.ResponseWriter, r *http.Request, kind string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrConflict):
		render.Error(w, r, http.StatusPreconditionFailed, "version_conflict",
			"This "+kind+" changed since you last read it. GET it again, re-apply your change onto the current version, then retry.")
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r, kind)
	default:
		s.serverErr(w, r)
	}
	return true
}

// ---- list activity annotation --------------------------------------------

// fillContactActivity annotates a contact page with each record's activity count
// and most-recent activity time, in one batched query, so a list carries the same
// activity signal a single GET shows. Best-effort: a stats hiccup leaves the
// counts unset rather than failing the list (matching the single-record fetch).
func (s *Server) fillContactActivity(ws string, list []protocol.Contact) {
	if len(list) == 0 {
		return
	}
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	stats, err := s.store.ActivityStatsBatch(ws, protocol.KindContact, ids)
	if err != nil {
		return
	}
	principals, _ := s.store.ActivityPrincipalsBatch(ws, protocol.KindContact, ids)
	for i := range list {
		list[i].OnBehalfOf = principals[list[i].ID]
		if st, ok := stats[list[i].ID]; ok {
			list[i].ActivityCount = st.Count
			if !st.Last.IsZero() {
				t := st.Last
				list[i].LastActivityAt = &t
			}
			list[i].OutreachCount = st.Outreach
			if !st.LastOutreach.IsZero() {
				t := st.LastOutreach
				list[i].LastOutreachAt = &t
			}
		}
	}
}

// fillCompanyActivity is fillContactActivity for a company page.
func (s *Server) fillCompanyActivity(ws string, list []protocol.Company) {
	if len(list) == 0 {
		return
	}
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	stats, err := s.store.ActivityStatsBatch(ws, protocol.KindCompany, ids)
	if err != nil {
		return
	}
	principals, _ := s.store.ActivityPrincipalsBatch(ws, protocol.KindCompany, ids)
	for i := range list {
		list[i].OnBehalfOf = principals[list[i].ID]
		if st, ok := stats[list[i].ID]; ok {
			list[i].ActivityCount = st.Count
			if !st.Last.IsZero() {
				t := st.Last
				list[i].LastActivityAt = &t
			}
			list[i].OutreachCount = st.Outreach
			if !st.LastOutreach.IsZero() {
				t := st.LastOutreach
				list[i].LastOutreachAt = &t
			}
		}
	}
}

// fillDealActivity is fillContactActivity for a deal page.
func (s *Server) fillDealActivity(ws string, list []protocol.Deal) {
	if len(list) == 0 {
		return
	}
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	stats, err := s.store.ActivityStatsBatch(ws, protocol.KindDeal, ids)
	if err != nil {
		return
	}
	principals, _ := s.store.ActivityPrincipalsBatch(ws, protocol.KindDeal, ids)
	for i := range list {
		list[i].OnBehalfOf = principals[list[i].ID]
		if st, ok := stats[list[i].ID]; ok {
			list[i].ActivityCount = st.Count
			if !st.Last.IsZero() {
				t := st.Last
				list[i].LastActivityAt = &t
			}
		}
	}
}

// fillTicketActivity is fillContactActivity for a ticket page (its conversation).
func (s *Server) fillTicketActivity(ws string, list []protocol.Ticket) {
	if len(list) == 0 {
		return
	}
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	stats, err := s.store.ActivityStatsBatch(ws, protocol.KindTicket, ids)
	if err != nil {
		return
	}
	principals, _ := s.store.ActivityPrincipalsBatch(ws, protocol.KindTicket, ids)
	for i := range list {
		list[i].OnBehalfOf = principals[list[i].ID]
		if st, ok := stats[list[i].ID]; ok {
			list[i].ActivityCount = st.Count
			if !st.Last.IsZero() {
				t := st.Last
				list[i].LastActivityAt = &t
			}
		}
	}
}

// ---- contacts ------------------------------------------------------------

func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q, err := parseListQuery(r, contactQuery)
	if err != nil {
		s.writeQueryError(w, r, err)
		return
	}
	list, total, next, err := s.store.QueryContacts(sess.WorkspaceID, q)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	s.fillContactActivity(sess.WorkspaceID, list)
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Contacts(list), &total, next)
}

// handleCreateContact creates a contact, or - when an email is supplied that
// already exists in the workspace - updates that contact instead (upsert on
// email, case-insensitive). This keeps agents from creating duplicates.
func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	body, err := readBody(r)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	var c protocol.Contact
	if err := decodeBytes(body, &c); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"name":"Jane Doe","email":"jane@acme.com","company_id":"company_..."}.`)
		return
	}
	if strings.TrimSpace(c.Name) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"name" is required to create a contact.`)
		return
	}
	// Optional ?campaign= attaches the created/upserted contact to a campaign.
	// Resolve it up front so a bad ref fails before any write.
	campaignID, ok := s.campaignParam(w, r)
	if !ok {
		return
	}

	// Upsert on email when present.
	if email := strings.TrimSpace(c.Email); email != "" {
		matches, err := s.store.FindContactByEmail(sess.WorkspaceID, email)
		if err != nil {
			s.serverErr(w, r)
			return
		}
		if len(matches) > 1 {
			render.Error(w, r, http.StatusConflict, "ambiguous_match",
				"Multiple contacts already have email "+email+". Resolve the duplicates, or update one directly by its id.")
			return
		}
		if len(matches) == 1 {
			existing := matches[0]
			creator := existing.CreatedBy                        // server-owned; never overridable by the body
			if err := decodeBytes(body, &existing); err != nil { // merge provided fields onto the match
				render.Error(w, r, http.StatusBadRequest, "bad_request", "Invalid JSON body.")
				return
			}
			existing.CreatedBy = creator
			existing.CompanyID = s.relationID(sess.WorkspaceID, protocol.KindCompany, existing.CompanyID)
			if err := s.store.UpdateContact(sess.WorkspaceID, &existing, 0); err != nil {
				s.serverErr(w, r)
				return
			}
			s.audit(sess, "contact.upsert", protocol.Handle(protocol.KindContact, existing.ID), "updated")
			if !s.attachToCampaign(w, r, sess, campaignID, protocol.KindContact, existing.ID) {
				return
			}
			existing = existing.Localized(locationOf(sess))
			render.Respond(w, r, http.StatusOK, existing, render.Contact(existing)+"\n# updated")
			return
		}
	}

	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "contacts") {
		return
	}
	c.CreatedBy = sess.Email // stamp the actor; never trust a client-supplied value
	c.CompanyID = s.relationID(sess.WorkspaceID, protocol.KindCompany, c.CompanyID)
	if err := s.store.CreateContact(sess.WorkspaceID, &c); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "contact.create", protocol.Handle(protocol.KindContact, c.ID), c.Name)
	if !s.attachToCampaign(w, r, sess, campaignID, protocol.KindContact, c.ID) {
		return
	}
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, c, render.Contact(c)+"\n# created")
}

func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindContact)
	if !ok {
		return
	}
	c, err := s.store.GetContact(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "contact")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Best-effort activity summary; a stats hiccup must not fail the fetch.
	if n, last, err := s.store.ActivityStats(sess.WorkspaceID, c.ID, "", "", ""); err == nil {
		c.ActivityCount = n
		if !last.IsZero() {
			c.LastActivityAt = &last
		}
	}
	if pr, err := s.store.ActivityPrincipalsBatch(sess.WorkspaceID, protocol.KindContact, []string{c.ID}); err == nil {
		c.OnBehalfOf = pr[c.ID]
	}
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Contact(c))
}

func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindContact)
	if !ok {
		return
	}
	c, err := s.store.GetContact(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "contact")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Snapshot the pre-edit state (clone the tag slice, which decode may reuse) so
	// we can record what changed in the audit.
	before := c
	before.Tags = append([]string(nil), c.Tags...)
	// Decode patch onto the existing record so omitted fields are preserved.
	body, err := readBody(r)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	if err := decodeBytes(body, &c); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Send a JSON object with only the fields you want to change.")
		return
	}
	c.CompanyID = s.relationID(sess.WorkspaceID, protocol.KindCompany, c.CompanyID)
	if s.writeUpdateErr(w, r, "contact", s.store.UpdateContact(sess.WorkspaceID, &c, expectedVersion(r, body))) {
		return
	}
	s.audit(sess, "contact.update", protocol.Handle(protocol.KindContact, c.ID), diffContact(before, c))
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Contact(c))
}

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindContact)
	if !ok {
		return
	}
	if !s.requireConfirm(w, r, protocol.KindContact, id) {
		return
	}
	if err := s.store.DeleteContact(sess.WorkspaceID, id); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "contact")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "contact.delete", protocol.Handle(protocol.KindContact, id), "")
	render.Text(w, r, http.StatusOK, "OK deleted "+r.PathValue("id"))
}

func (s *Server) handleListContactActivities(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindContact)
	if !ok {
		return
	}
	limit := render.Int(r.URL.Query().Get("limit"), 50)
	list, err := s.store.ListActivities(sess.WorkspaceID, []string{id}, nil, nil, nil, r.URL.Query().Get("on_behalf_of"), limit)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Enveloped like the paginated lists for a uniform shape; no cursor here.
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Activities(list), nil, "")
}

func (s *Server) handleCreateContactActivity(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindContact)
	if !ok {
		return
	}
	var a protocol.Activity
	if err := decodeJSON(r, &a); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"kind":"call","body":"Left a voicemail"}. kind is one of note|call|email|meeting|task.`)
		return
	}
	if strings.TrimSpace(a.Body) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"body" is required to log an activity.`)
		return
	}
	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "activities") {
		return
	}
	a.ContactID = id
	// Resolve any cross-linked records sent in the body (handles -> internal ids), so
	// the activity attaches by id like the path record does - otherwise a handle would
	// be stored verbatim and roll-ups/filters keyed on the id would miss it.
	a.DealID = s.relationID(sess.WorkspaceID, protocol.KindDeal, a.DealID)
	a.CompanyID = s.relationID(sess.WorkspaceID, protocol.KindCompany, a.CompanyID)
	a.TicketID = s.relationID(sess.WorkspaceID, protocol.KindTicket, a.TicketID)
	a.CreatedBy = sess.Email
	if err := s.store.CreateActivity(sess.WorkspaceID, &a); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "activity.create", protocol.Handle(protocol.KindActivity, a.ID), a.Kind)
	render.Respond(w, r, http.StatusCreated, a, render.ActivityLine(a))
}

func (s *Server) handleListCompanyActivities(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCompany)
	if !ok {
		return
	}
	limit := render.Int(r.URL.Query().Get("limit"), 50)
	list, err := s.store.ListActivities(sess.WorkspaceID, nil, nil, []string{id}, nil, r.URL.Query().Get("on_behalf_of"), limit)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Activities(list), nil, "")
}

func (s *Server) handleCreateCompanyActivity(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCompany)
	if !ok {
		return
	}
	var a protocol.Activity
	if err := decodeJSON(r, &a); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"kind":"note","body":"Launched a WhatsApp agent"}. kind is one of note|call|email|meeting|task.`)
		return
	}
	if strings.TrimSpace(a.Body) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"body" is required to log an activity.`)
		return
	}
	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "activities") {
		return
	}
	a.CompanyID = id
	// Resolve cross-linked records from the body (handles -> internal ids); see
	// handleCreateContactActivity.
	a.ContactID = s.relationID(sess.WorkspaceID, protocol.KindContact, a.ContactID)
	a.DealID = s.relationID(sess.WorkspaceID, protocol.KindDeal, a.DealID)
	a.TicketID = s.relationID(sess.WorkspaceID, protocol.KindTicket, a.TicketID)
	a.CreatedBy = sess.Email
	if err := s.store.CreateActivity(sess.WorkspaceID, &a); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "activity.create", protocol.Handle(protocol.KindActivity, a.ID), a.Kind)
	a = a.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, a, render.ActivityLine(a))
}

func (s *Server) handleListDealActivities(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindDeal)
	if !ok {
		return
	}
	limit := render.Int(r.URL.Query().Get("limit"), 50)
	list, err := s.store.ListActivities(sess.WorkspaceID, nil, []string{id}, nil, nil, r.URL.Query().Get("on_behalf_of"), limit)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Activities(list), nil, "")
}

func (s *Server) handleCreateDealActivity(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindDeal)
	if !ok {
		return
	}
	var a protocol.Activity
	if err := decodeJSON(r, &a); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"kind":"meeting","body":"Walked through pricing"}. kind is one of note|call|email|meeting|task.`)
		return
	}
	if strings.TrimSpace(a.Body) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"body" is required to log an activity.`)
		return
	}
	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "activities") {
		return
	}
	a.DealID = id
	// Resolve cross-linked records from the body (handles -> internal ids); see
	// handleCreateContactActivity.
	a.ContactID = s.relationID(sess.WorkspaceID, protocol.KindContact, a.ContactID)
	a.CompanyID = s.relationID(sess.WorkspaceID, protocol.KindCompany, a.CompanyID)
	a.TicketID = s.relationID(sess.WorkspaceID, protocol.KindTicket, a.TicketID)
	a.CreatedBy = sess.Email
	if err := s.store.CreateActivity(sess.WorkspaceID, &a); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "activity.create", protocol.Handle(protocol.KindActivity, a.ID), a.Kind)
	a = a.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, a, render.ActivityLine(a))
}

// ---- companies -----------------------------------------------------------

func (s *Server) handleListCompanies(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q, err := parseListQuery(r, companyQuery)
	if err != nil {
		s.writeQueryError(w, r, err)
		return
	}
	list, total, next, err := s.store.QueryCompanies(sess.WorkspaceID, q)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	s.fillCompanyActivity(sess.WorkspaceID, list)
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Companies(list), &total, next)
}

// handleCreateCompany creates a company, or - when a domain is supplied that
// already exists in the workspace - updates that company instead (upsert on
// domain, case-insensitive).
func (s *Server) handleCreateCompany(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	body, err := readBody(r)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	var c protocol.Company
	if err := decodeBytes(body, &c); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", `Send JSON, e.g. {"name":"Acme","domain":"acme.com"}.`)
		return
	}
	if strings.TrimSpace(c.Name) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"name" is required to create a company.`)
		return
	}
	// Optional ?campaign= attaches the created/upserted company to a campaign.
	campaignID, ok := s.campaignParam(w, r)
	if !ok {
		return
	}

	if domain := strings.TrimSpace(c.Domain); domain != "" {
		matches, err := s.store.FindCompanyByDomain(sess.WorkspaceID, domain)
		if err != nil {
			s.serverErr(w, r)
			return
		}
		if len(matches) > 1 {
			render.Error(w, r, http.StatusConflict, "ambiguous_match",
				"Multiple companies already have domain "+domain+". Resolve the duplicates, or update one directly by its id.")
			return
		}
		if len(matches) == 1 {
			existing := matches[0]
			creator := existing.CreatedBy // server-owned; never overridable by the body
			if err := decodeBytes(body, &existing); err != nil {
				render.Error(w, r, http.StatusBadRequest, "bad_request", "Invalid JSON body.")
				return
			}
			existing.CreatedBy = creator
			if err := s.store.UpdateCompany(sess.WorkspaceID, &existing, 0); err != nil {
				s.serverErr(w, r)
				return
			}
			s.audit(sess, "company.upsert", protocol.Handle(protocol.KindCompany, existing.ID), "updated")
			if !s.attachToCampaign(w, r, sess, campaignID, protocol.KindCompany, existing.ID) {
				return
			}
			existing = existing.Localized(locationOf(sess))
			render.Respond(w, r, http.StatusOK, existing, render.Company(existing)+"\n# updated")
			return
		}
	}

	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "companies") {
		return
	}
	c.CreatedBy = sess.Email // stamp the actor; never trust a client-supplied value
	if err := s.store.CreateCompany(sess.WorkspaceID, &c); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "company.create", protocol.Handle(protocol.KindCompany, c.ID), c.Name)
	if !s.attachToCampaign(w, r, sess, campaignID, protocol.KindCompany, c.ID) {
		return
	}
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, c, render.Company(c)+"\n# created")
}

func (s *Server) handleGetCompany(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCompany)
	if !ok {
		return
	}
	c, err := s.store.GetCompany(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "company")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Best-effort activity summary; a stats hiccup must not fail the fetch.
	if n, last, err := s.store.ActivityStats(sess.WorkspaceID, "", "", c.ID, ""); err == nil {
		c.ActivityCount = n
		if !last.IsZero() {
			c.LastActivityAt = &last
		}
	}
	if pr, err := s.store.ActivityPrincipalsBatch(sess.WorkspaceID, protocol.KindCompany, []string{c.ID}); err == nil {
		c.OnBehalfOf = pr[c.ID]
	}
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Company(c))
}

func (s *Server) handleUpdateCompany(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCompany)
	if !ok {
		return
	}
	c, err := s.store.GetCompany(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "company")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	before := c
	before.Tags = append([]string(nil), c.Tags...)
	body, err := readBody(r)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	if err := decodeBytes(body, &c); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Send a JSON object with only the fields you want to change.")
		return
	}
	if s.writeUpdateErr(w, r, "company", s.store.UpdateCompany(sess.WorkspaceID, &c, expectedVersion(r, body))) {
		return
	}
	s.audit(sess, "company.update", protocol.Handle(protocol.KindCompany, c.ID), diffCompany(before, c))
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Company(c))
}

func (s *Server) handleDeleteCompany(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCompany)
	if !ok {
		return
	}
	if !s.requireConfirm(w, r, protocol.KindCompany, id) {
		return
	}
	if err := s.store.DeleteCompany(sess.WorkspaceID, id); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "company")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "company.delete", protocol.Handle(protocol.KindCompany, id), "")
	render.Text(w, r, http.StatusOK, "OK deleted "+r.PathValue("id"))
}

// ---- deals ---------------------------------------------------------------

func (s *Server) handleListDeals(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q, err := parseListQuery(r, dealQuery)
	if err != nil {
		s.writeQueryError(w, r, err)
		return
	}
	list, total, next, err := s.store.QueryDeals(sess.WorkspaceID, q)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	s.fillDealActivity(sess.WorkspaceID, list)
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Deals(list), &total, next)
}

func (s *Server) handleCreateDeal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var d protocol.Deal
	if err := decodeJSON(r, &d); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"title":"Acme renewal","amount_cents":500000,"currency":"USD","stage":"proposal","contact_id":"contact_..."}.`)
		return
	}
	if strings.TrimSpace(d.Title) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"title" is required to create a deal.`)
		return
	}
	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "deals") {
		return
	}
	d.CreatedBy = sess.Email // stamp the actor; never trust a client-supplied value
	d.ContactID = s.relationID(sess.WorkspaceID, protocol.KindContact, d.ContactID)
	d.CompanyID = s.relationID(sess.WorkspaceID, protocol.KindCompany, d.CompanyID)
	if err := s.store.CreateDeal(sess.WorkspaceID, &d); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "deal.create", protocol.Handle(protocol.KindDeal, d.ID), d.Title)
	d = d.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, d, render.Deal(d))
}

func (s *Server) handleGetDeal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindDeal)
	if !ok {
		return
	}
	d, err := s.store.GetDeal(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "deal")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Best-effort activity summary; a stats hiccup must not fail the fetch.
	if n, last, err := s.store.ActivityStats(sess.WorkspaceID, "", d.ID, "", ""); err == nil {
		d.ActivityCount = n
		if !last.IsZero() {
			d.LastActivityAt = &last
		}
	}
	if pr, err := s.store.ActivityPrincipalsBatch(sess.WorkspaceID, protocol.KindDeal, []string{d.ID}); err == nil {
		d.OnBehalfOf = pr[d.ID]
	}
	d = d.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, d, render.Deal(d))
}

func (s *Server) handleUpdateDeal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindDeal)
	if !ok {
		return
	}
	d, err := s.store.GetDeal(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "deal")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	before := d
	body, err := readBody(r)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	if err := decodeBytes(body, &d); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send a JSON object with only the fields you want to change, e.g. {"stage":"won","status":"won"}.`)
		return
	}
	d.ContactID = s.relationID(sess.WorkspaceID, protocol.KindContact, d.ContactID)
	d.CompanyID = s.relationID(sess.WorkspaceID, protocol.KindCompany, d.CompanyID)
	if s.writeUpdateErr(w, r, "deal", s.store.UpdateDeal(sess.WorkspaceID, &d, expectedVersion(r, body))) {
		return
	}
	s.audit(sess, "deal.update", protocol.Handle(protocol.KindDeal, d.ID), diffDeal(before, d))
	d = d.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, d, render.Deal(d))
}

func (s *Server) handleDeleteDeal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindDeal)
	if !ok {
		return
	}
	if !s.requireConfirm(w, r, protocol.KindDeal, id) {
		return
	}
	if err := s.store.DeleteDeal(sess.WorkspaceID, id); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "deal")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "deal.delete", protocol.Handle(protocol.KindDeal, id), "")
	render.Text(w, r, http.StatusOK, "OK deleted "+r.PathValue("id"))
}

// ---- tickets -------------------------------------------------------------

// validTicketStatus reports whether s is an allowed ticket status. Empty is
// accepted (create defaults it to "open"). The lifecycle is open/pending/solved
// for now; on-hold/closed come later.
func validTicketStatus(s string) bool {
	switch s {
	case "", "open", "pending", "solved":
		return true
	}
	return false
}

func (s *Server) handleListTickets(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q, err := parseListQuery(r, ticketQuery)
	if err != nil {
		s.writeQueryError(w, r, err)
		return
	}
	list, total, next, err := s.store.QueryTickets(sess.WorkspaceID, q)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	s.fillTicketActivity(sess.WorkspaceID, list)
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Tickets(list), &total, next)
}

func (s *Server) handleCreateTicket(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var t protocol.Ticket
	if err := decodeJSON(r, &t); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"subject":"Can't log in","content":"...","requester_id":"contact_...","status":"open"}.`)
		return
	}
	if strings.TrimSpace(t.Subject) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"subject" is required to create a ticket.`)
		return
	}
	if !validTicketStatus(t.Status) {
		render.Error(w, r, http.StatusBadRequest, "invalid_field", `"status" must be one of: open, pending, solved.`)
		return
	}
	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "tickets") {
		return
	}
	t.CreatedBy = sess.Email // stamp the actor; never trust a client-supplied value
	t.RequesterID = s.relationID(sess.WorkspaceID, protocol.KindContact, t.RequesterID)
	if err := s.store.CreateTicket(sess.WorkspaceID, &t); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "ticket.create", protocol.Handle(protocol.KindTicket, t.ID), t.Subject)
	t = t.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, t, render.Ticket(t))
}

func (s *Server) handleGetTicket(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindTicket)
	if !ok {
		return
	}
	t, err := s.store.GetTicket(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "ticket")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Best-effort conversation summary; a stats hiccup must not fail the fetch.
	if n, last, err := s.store.ActivityStats(sess.WorkspaceID, "", "", "", t.ID); err == nil {
		t.ActivityCount = n
		if !last.IsZero() {
			t.LastActivityAt = &last
		}
	}
	if pr, err := s.store.ActivityPrincipalsBatch(sess.WorkspaceID, protocol.KindTicket, []string{t.ID}); err == nil {
		t.OnBehalfOf = pr[t.ID]
	}
	t = t.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, t, render.Ticket(t))
}

// handleListTicketActivities returns a ticket's conversation (its activity
// timeline).
func (s *Server) handleListTicketActivities(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindTicket)
	if !ok {
		return
	}
	limit := render.Int(r.URL.Query().Get("limit"), 50)
	list, err := s.store.ListActivities(sess.WorkspaceID, nil, nil, nil, []string{id}, r.URL.Query().Get("on_behalf_of"), limit)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Activities(list), nil, "")
}

func (s *Server) handleCreateTicketActivity(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindTicket)
	if !ok {
		return
	}
	var a protocol.Activity
	if err := decodeJSON(r, &a); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"kind":"note","body":"Asked for logs"}. kind is one of note|call|email|meeting|task.`)
		return
	}
	if strings.TrimSpace(a.Body) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"body" is required to log an activity.`)
		return
	}
	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "activities") {
		return
	}
	a.TicketID = id
	// Resolve cross-linked records from the body (handles -> internal ids); see
	// handleCreateContactActivity.
	a.ContactID = s.relationID(sess.WorkspaceID, protocol.KindContact, a.ContactID)
	a.DealID = s.relationID(sess.WorkspaceID, protocol.KindDeal, a.DealID)
	a.CompanyID = s.relationID(sess.WorkspaceID, protocol.KindCompany, a.CompanyID)
	a.CreatedBy = sess.Email
	if err := s.store.CreateActivity(sess.WorkspaceID, &a); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "activity.create", protocol.Handle(protocol.KindActivity, a.ID), a.Kind)
	a = a.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, a, render.ActivityLine(a))
}

func (s *Server) handleUpdateTicket(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindTicket)
	if !ok {
		return
	}
	t, err := s.store.GetTicket(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "ticket")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	before := t
	before.Tags = append([]string(nil), t.Tags...)
	body, err := readBody(r)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	if err := decodeBytes(body, &t); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send a JSON object with only the fields you want to change, e.g. {"status":"solved"}.`)
		return
	}
	if !validTicketStatus(t.Status) {
		render.Error(w, r, http.StatusBadRequest, "invalid_field", `"status" must be one of: open, pending, solved.`)
		return
	}
	t.RequesterID = s.relationID(sess.WorkspaceID, protocol.KindContact, t.RequesterID)
	if s.writeUpdateErr(w, r, "ticket", s.store.UpdateTicket(sess.WorkspaceID, &t, expectedVersion(r, body))) {
		return
	}
	s.audit(sess, "ticket.update", protocol.Handle(protocol.KindTicket, t.ID), diffTicket(before, t))
	t = t.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, t, render.Ticket(t))
}

func (s *Server) handleDeleteTicket(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindTicket)
	if !ok {
		return
	}
	if !s.requireConfirm(w, r, protocol.KindTicket, id) {
		return
	}
	if err := s.store.DeleteTicket(sess.WorkspaceID, id); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "ticket")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "ticket.delete", protocol.Handle(protocol.KindTicket, id), "")
	render.Text(w, r, http.StatusOK, "OK deleted "+r.PathValue("id"))
}

// ---- tasks ---------------------------------------------------------------

// applyDone translates the write-only `done` convenience into DoneAt: true
// stamps now (unless already done), false clears it. The flag is then cleared so
// it never reaches the store.
func applyDone(t *protocol.Task) {
	if t.Done == nil {
		return
	}
	if *t.Done {
		if t.DoneAt == nil {
			now := time.Now()
			t.DoneAt = &now
		}
	} else {
		t.DoneAt = nil
	}
	t.Done = nil
}

// resolveTaskLinks turns any handle a client sent for a task's links into the
// internal id (a blank or already-internal value is left as-is).
func (s *Server) resolveTaskLinks(ws string, t *protocol.Task) {
	t.ContactID = s.relationID(ws, protocol.KindContact, t.ContactID)
	t.CompanyID = s.relationID(ws, protocol.KindCompany, t.CompanyID)
	t.DealID = s.relationID(ws, protocol.KindDeal, t.DealID)
	t.TicketID = s.relationID(ws, protocol.KindTicket, t.TicketID)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q, err := parseListQuery(r, taskQuery)
	if err != nil {
		s.writeQueryError(w, r, err)
		return
	}
	list, total, next, err := s.store.QueryTasks(sess.WorkspaceID, q)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Tasks(list), &total, next)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var t protocol.Task
	if err := decodeJSON(r, &t); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"title":"Send renewal quote","due_at":"2026-06-20T09:00:00Z","contact_id":"contact_.."}.`)
		return
	}
	if strings.TrimSpace(t.Title) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"title" is required to create a task.`)
		return
	}
	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "tasks") {
		return
	}
	t.CreatedBy = sess.Email // stamp the actor; never trust a client-supplied value
	applyDone(&t)
	s.resolveTaskLinks(sess.WorkspaceID, &t)
	if err := s.store.CreateTask(sess.WorkspaceID, &t); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "task.create", protocol.Handle(protocol.KindTask, t.ID), t.Title)
	t = t.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, t, render.Task(t))
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindTask)
	if !ok {
		return
	}
	t, err := s.store.GetTask(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "task")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	t = t.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, t, render.Task(t))
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindTask)
	if !ok {
		return
	}
	t, err := s.store.GetTask(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "task")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	before := t
	body, err := readBody(r)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	if err := decodeBytes(body, &t); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send a JSON object with only the fields you want to change, e.g. {"done":true} or {"due_at":"2026-06-20T09:00:00Z"}.`)
		return
	}
	if strings.TrimSpace(t.Title) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"title" cannot be blank.`)
		return
	}
	applyDone(&t)
	s.resolveTaskLinks(sess.WorkspaceID, &t)
	if s.writeUpdateErr(w, r, "task", s.store.UpdateTask(sess.WorkspaceID, &t, expectedVersion(r, body))) {
		return
	}
	s.audit(sess, "task.update", protocol.Handle(protocol.KindTask, t.ID), diffTask(before, t))
	t = t.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, t, render.Task(t))
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindTask)
	if !ok {
		return
	}
	if !s.requireConfirm(w, r, protocol.KindTask, id) {
		return
	}
	if err := s.store.DeleteTask(sess.WorkspaceID, id); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "task")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "task.delete", protocol.Handle(protocol.KindTask, id), "")
	render.Text(w, r, http.StatusOK, "OK deleted "+r.PathValue("id"))
}

// ---- reminders -----------------------------------------------------------

func (s *Server) handleListReminders(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q := r.URL.Query()
	// Default: items due now or overdue. ?days=N looks ahead N days.
	until := time.Now()
	if days := render.Int(q.Get("days"), 0); days > 0 {
		until = until.Add(time.Duration(days) * 24 * time.Hour)
	}
	list, err := s.store.ListReminders(sess.WorkspaceID, until, render.Int(q.Get("limit"), 100))
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Reminders(list), nil, "")
}

// ---- activities & audit --------------------------------------------------

func (s *Server) handleListActivities(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q := r.URL.Query()
	// Filters accept one or more comma-separated handles (or raw ids); resolve each
	// to an internal id. Passing several - e.g. ?company=company_a,company_b - pulls
	// the activity for a whole set of records in one call (matches any of them).
	contactIDs := s.relationIDs(sess.WorkspaceID, protocol.KindContact, q.Get("contact"))
	dealIDs := s.relationIDs(sess.WorkspaceID, protocol.KindDeal, q.Get("deal"))
	companyIDs := s.relationIDs(sess.WorkspaceID, protocol.KindCompany, q.Get("company"))
	ticketIDs := s.relationIDs(sess.WorkspaceID, protocol.KindTicket, q.Get("ticket"))
	// on_behalf_of filters to the principal an agent acted for (e.g. ?on_behalf_of=alice@acme.com).
	list, err := s.store.ListActivities(sess.WorkspaceID, contactIDs, dealIDs, companyIDs, ticketIDs, q.Get("on_behalf_of"), render.Int(q.Get("limit"), 50))
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Activities(list), nil, "")
}

// handleDeleteActivity removes one activity. Unlike contacts/companies/deals it
// is a one-shot delete (no confirm step): an activity is a single low-stakes log
// line, and you may prune several to free room under the activity quota.
func (s *Server) handleDeleteActivity(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindActivity)
	if !ok {
		return
	}
	if err := s.store.DeleteActivity(sess.WorkspaceID, id); errors.Is(err, store.ErrNotFound) {
		render.Error(w, r, http.StatusNotFound, "not_found", "No activity with that id in your workspace.")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "activity.delete", protocol.Handle(protocol.KindActivity, id), "")
	render.Text(w, r, http.StatusOK, "OK deleted "+r.PathValue("id"))
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	by := strings.TrimSpace(r.URL.Query().Get("by"))
	target := s.resolveAuditTarget(sess.WorkspaceID, r.URL.Query().Get("target"))
	list, err := s.store.ListAudit(sess.WorkspaceID, by, target, render.Int(r.URL.Query().Get("limit"), 50))
	if err != nil {
		s.serverErr(w, r)
		return
	}
	loc := locationOf(sess)
	lines := strings.Builder{}
	for _, e := range list {
		lines.WriteString(render.Line("audit/"+e.ID,
			render.F("by", e.ActorEmail),
			render.F("action", e.Action),
			render.F("target", e.Target),
			render.F("detail", e.Detail),
			render.F("at", render.Stamp(e.CreatedAt.In(loc))),
		))
		lines.WriteByte('\n')
	}
	fmt.Fprintf(&lines, "# %d audit entry(ies)", len(list))
	s.respondList(w, r, list, lines.String(), nil, "")
}
