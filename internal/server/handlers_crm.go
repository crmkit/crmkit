package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
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
func (s *Server) requireConfirm(w http.ResponseWriter, r *http.Request, kind, id string) bool {
	want := confirmToken(id)
	if strings.TrimSpace(r.URL.Query().Get("confirm")) == want {
		return true
	}
	handle := protocol.Handle(kind, id)
	render.Error(w, r, http.StatusConflict, "confirmation_required",
		"Deleting "+handle+" is irreversible. Confirm with the user, then repeat with ?confirm="+want)
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

// ---- contacts ------------------------------------------------------------

func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q, err := parseListQuery(r, contactQuery)
	if err != nil {
		s.writeQueryError(w, r, err)
		return
	}
	list, next, err := s.store.QueryContacts(sess.WorkspaceID, q)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Contacts(list), next)
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
			`Send JSON, e.g. {"name":"Jane Doe","email":"jane@acme.com","company_id":"co_..."}.`)
		return
	}
	if strings.TrimSpace(c.Name) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"name" is required to create a contact.`)
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
			if err := decodeBytes(body, &existing); err != nil { // merge provided fields onto the match
				render.Error(w, r, http.StatusBadRequest, "bad_request", "Invalid JSON body.")
				return
			}
			if err := s.store.UpdateContact(sess.WorkspaceID, &existing); err != nil {
				s.serverErr(w, r)
				return
			}
			s.audit(sess, "contact.upsert", protocol.Handle(protocol.KindContact, existing.ID), "updated")
			existing = existing.Localized(locationOf(sess))
			render.Respond(w, r, http.StatusOK, existing, render.Contact(existing)+"\n# updated")
			return
		}
	}

	if !s.enforceWorkspaceQuota(w, r, sess.WorkspaceID, "contacts") {
		return
	}
	c.CreatedBy = sess.Email // stamp the actor; never trust a client-supplied value
	if err := s.store.CreateContact(sess.WorkspaceID, &c); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "contact.create", protocol.Handle(protocol.KindContact, c.ID), c.Name)
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, c, render.Contact(c)+"\n# created")
}

func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	c, err := s.store.GetContact(sess.WorkspaceID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "contact")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Best-effort activity summary; a stats hiccup must not fail the fetch.
	if n, last, err := s.store.ActivityStats(sess.WorkspaceID, c.ID, ""); err == nil {
		c.ActivityCount = n
		if !last.IsZero() {
			c.LastActivityAt = &last
		}
	}
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Contact(c))
}

func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	c, err := s.store.GetContact(sess.WorkspaceID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "contact")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Decode patch onto the existing record so omitted fields are preserved.
	if err := decodeJSON(r, &c); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Send a JSON object with only the fields you want to change.")
		return
	}
	if err := s.store.UpdateContact(sess.WorkspaceID, &c); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "contact.update", protocol.Handle(protocol.KindContact, c.ID), "")
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Contact(c))
}

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id := r.PathValue("id")
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
	render.Text(w, r, http.StatusOK, "OK deleted "+protocol.Handle(protocol.KindContact, id))
}

func (s *Server) handleListContactActivities(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id := r.PathValue("id")
	limit := render.Int(r.URL.Query().Get("limit"), 50)
	list, err := s.store.ListActivities(sess.WorkspaceID, id, "", limit)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Enveloped like the paginated lists for a uniform shape; no cursor here.
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Activities(list), "")
}

func (s *Server) handleCreateContactActivity(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id := r.PathValue("id")
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
	a.ContactID = id
	a.CreatedBy = sess.Email
	if err := s.store.CreateActivity(sess.WorkspaceID, &a); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "activity.create", protocol.Handle(protocol.KindActivity, a.ID), a.Kind)
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
	list, next, err := s.store.QueryCompanies(sess.WorkspaceID, q)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Companies(list), next)
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
			if err := decodeBytes(body, &existing); err != nil {
				render.Error(w, r, http.StatusBadRequest, "bad_request", "Invalid JSON body.")
				return
			}
			if err := s.store.UpdateCompany(sess.WorkspaceID, &existing); err != nil {
				s.serverErr(w, r)
				return
			}
			s.audit(sess, "company.upsert", protocol.Handle(protocol.KindCompany, existing.ID), "updated")
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
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, c, render.Company(c)+"\n# created")
}

func (s *Server) handleGetCompany(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	c, err := s.store.GetCompany(sess.WorkspaceID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "company")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Company(c))
}

func (s *Server) handleUpdateCompany(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	c, err := s.store.GetCompany(sess.WorkspaceID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "company")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	if err := decodeJSON(r, &c); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Send a JSON object with only the fields you want to change.")
		return
	}
	if err := s.store.UpdateCompany(sess.WorkspaceID, &c); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "company.update", protocol.Handle(protocol.KindCompany, c.ID), "")
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Company(c))
}

func (s *Server) handleDeleteCompany(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id := r.PathValue("id")
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
	render.Text(w, r, http.StatusOK, "OK deleted "+protocol.Handle(protocol.KindCompany, id))
}

// ---- deals ---------------------------------------------------------------

func (s *Server) handleListDeals(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q, err := parseListQuery(r, dealQuery)
	if err != nil {
		s.writeQueryError(w, r, err)
		return
	}
	list, next, err := s.store.QueryDeals(sess.WorkspaceID, q)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Deals(list), next)
}

func (s *Server) handleCreateDeal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var d protocol.Deal
	if err := decodeJSON(r, &d); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"title":"Acme renewal","amount_cents":500000,"currency":"USD","stage":"proposal","contact_id":"c_..."}.`)
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
	d, err := s.store.GetDeal(sess.WorkspaceID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "deal")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Best-effort activity summary; a stats hiccup must not fail the fetch.
	if n, last, err := s.store.ActivityStats(sess.WorkspaceID, "", d.ID); err == nil {
		d.ActivityCount = n
		if !last.IsZero() {
			d.LastActivityAt = &last
		}
	}
	d = d.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, d, render.Deal(d))
}

func (s *Server) handleUpdateDeal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	d, err := s.store.GetDeal(sess.WorkspaceID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "deal")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	if err := decodeJSON(r, &d); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send a JSON object with only the fields you want to change, e.g. {"stage":"won","status":"won"}.`)
		return
	}
	if err := s.store.UpdateDeal(sess.WorkspaceID, &d); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "deal.update", protocol.Handle(protocol.KindDeal, d.ID), d.Stage)
	d = d.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, d, render.Deal(d))
}

func (s *Server) handleDeleteDeal(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id := r.PathValue("id")
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
	render.Text(w, r, http.StatusOK, "OK deleted "+protocol.Handle(protocol.KindDeal, id))
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
	s.respondList(w, r, list, render.Reminders(list), "")
}

// ---- activities & audit --------------------------------------------------

func (s *Server) handleListActivities(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q := r.URL.Query()
	list, err := s.store.ListActivities(sess.WorkspaceID, q.Get("contact"), q.Get("deal"), render.Int(q.Get("limit"), 50))
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Activities(list), "")
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	by := strings.TrimSpace(r.URL.Query().Get("by"))
	list, err := s.store.ListAudit(sess.WorkspaceID, by, render.Int(r.URL.Query().Get("limit"), 50))
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
	s.respondList(w, r, list, lines.String(), "")
}
