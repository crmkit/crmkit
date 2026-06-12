package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/render"
	"github.com/crmkit/crmkit/internal/store"
)

// validCampaignStatus reports whether s is an allowed campaign lifecycle state.
// Empty is allowed (the store defaults it to "active").
func validCampaignStatus(s string) bool {
	switch s {
	case "", "active", "paused", "done":
		return true
	}
	return false
}

// campaignMemberKind reports whether kind is an entity that can be a campaign
// member (a contact or a company).
func campaignMemberKind(kind string) bool {
	return kind == protocol.KindContact || kind == protocol.KindCompany
}

func (s *Server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	q, err := parseListQuery(r, campaignQuery)
	if err != nil {
		s.writeQueryError(w, r, err)
		return
	}
	list, next, err := s.store.QueryCampaigns(sess.WorkspaceID, q)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.Campaigns(list), next)
}

func (s *Server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var c protocol.Campaign
	if err := decodeJSON(r, &c); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"name":"Series-B fintechs","description":"CTOs at fintechs that raised a Series B in the last year"}.`)
		return
	}
	if strings.TrimSpace(c.Name) == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"name" is required to create a campaign.`)
		return
	}
	if !validCampaignStatus(c.Status) {
		render.Error(w, r, http.StatusBadRequest, "invalid_field", `"status" must be one of: active, paused, done.`)
		return
	}
	c.CreatedBy = sess.Email // stamp the actor; never trust a client-supplied value
	if err := s.store.CreateCampaign(sess.WorkspaceID, &c); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "campaign.create", protocol.Handle(protocol.KindCampaign, c.ID), c.Name)
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusCreated, c, render.Campaign(c))
}

func (s *Server) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCampaign)
	if !ok {
		return
	}
	c, err := s.store.GetCampaign(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "campaign")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Best-effort member counts for the objective view; a hiccup must not fail the fetch.
	if counts, err := s.store.CountCampaignMembers(sess.WorkspaceID, id); err == nil {
		c.MemberCounts = counts
	}
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Campaign(c))
}

func (s *Server) handleUpdateCampaign(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCampaign)
	if !ok {
		return
	}
	c, err := s.store.GetCampaign(sess.WorkspaceID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "campaign")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	body, err := readBody(r)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	if err := decodeBytes(body, &c); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send a JSON object with only the fields you want to change, e.g. {"status":"done"}.`)
		return
	}
	if !validCampaignStatus(c.Status) {
		render.Error(w, r, http.StatusBadRequest, "invalid_field", `"status" must be one of: active, paused, done.`)
		return
	}
	if s.writeUpdateErr(w, r, "campaign", s.store.UpdateCampaign(sess.WorkspaceID, &c, expectedVersion(r, body))) {
		return
	}
	s.audit(sess, "campaign.update", protocol.Handle(protocol.KindCampaign, c.ID), c.Status)
	c = c.Localized(locationOf(sess))
	render.Respond(w, r, http.StatusOK, c, render.Campaign(c))
}

func (s *Server) handleDeleteCampaign(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCampaign)
	if !ok {
		return
	}
	if !s.requireConfirm(w, r, protocol.KindCampaign, id) {
		return
	}
	if err := s.store.DeleteCampaign(sess.WorkspaceID, id); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r, "campaign")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "campaign.delete", protocol.Handle(protocol.KindCampaign, id), "")
	render.Text(w, r, http.StatusOK, "OK deleted "+r.PathValue("id"))
}

// campaignParam reads an optional ?campaign= reference on a create/upsert and
// resolves it to a campaign id. It returns ("", true) when absent; when present
// but unresolvable it writes a 400 and returns ok=false, so the caller fails
// fast before writing the entity (no orphaned create on a bad campaign ref).
func (s *Server) campaignParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	ref := strings.TrimSpace(r.URL.Query().Get("campaign"))
	if ref == "" {
		return "", true
	}
	id, err := s.store.ResolveHandle(sessionFrom(r).WorkspaceID, protocol.KindCampaign, ref)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "invalid_field",
			`"campaign" did not match a campaign in your workspace. Create it first, then attach.`)
		return "", false
	}
	return id, true
}

// attachToCampaign attaches a just-created or upserted entity to a campaign when
// campaignID is set (already validated by campaignParam). The attach is
// idempotent, so re-running a create with the same campaign is a free no-op. An
// optional ?reason= carries the provenance note. On a store error it writes a
// 500 and returns false.
func (s *Server) attachToCampaign(w http.ResponseWriter, r *http.Request, sess protocol.Session, campaignID, kind, entityID string) bool {
	if campaignID == "" {
		return true
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if err := s.store.AttachCampaignMember(sess.WorkspaceID, campaignID, kind, entityID, reason, sess.Email); err != nil {
		s.serverErr(w, r)
		return false
	}
	s.audit(sess, "campaign.attach", protocol.Handle(protocol.KindCampaign, campaignID), protocol.Handle(kind, entityID))
	return true
}

// ---- campaign members ----------------------------------------------------

func (s *Server) handleListCampaignMembers(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCampaign)
	if !ok {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && !campaignMemberKind(kind) {
		render.Error(w, r, http.StatusBadRequest, "invalid_field", `"kind" must be "contact" or "company".`)
		return
	}
	limit := render.Int(r.URL.Query().Get("limit"), 200)
	list, err := s.store.ListCampaignMembers(sess.WorkspaceID, id, kind, limit)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	s.respondList(w, r, list, render.CampaignMembers(list), "")
}

// campaignMemberRequest is the body of POST /campaigns/{id}/members: an entity
// reference (a contact or company handle) plus an optional provenance note.
type campaignMemberRequest struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

func (s *Server) handleAttachCampaignMember(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCampaign)
	if !ok {
		return
	}
	var req campaignMemberRequest
	if err := decodeJSON(r, &req); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request",
			`Send JSON, e.g. {"kind":"contact","id":"contact_k7m2q","reason":"matches the brief"}.`)
		return
	}
	if !campaignMemberKind(req.Kind) {
		render.Error(w, r, http.StatusBadRequest, "invalid_field", `"kind" must be "contact" or "company".`)
		return
	}
	entityID, err := s.store.ResolveHandle(sess.WorkspaceID, req.Kind, req.ID)
	if err != nil {
		render.Error(w, r, http.StatusBadRequest, "invalid_field",
			`"id" did not match a `+req.Kind+` in your workspace. Create or look it up first, then attach it.`)
		return
	}
	if err := s.store.AttachCampaignMember(sess.WorkspaceID, id, req.Kind, entityID, req.Reason, sess.Email); err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "campaign.attach", protocol.Handle(protocol.KindCampaign, id), protocol.Handle(req.Kind, entityID))
	render.Text(w, r, http.StatusOK, "OK attached "+protocol.FormatRef(req.Kind, protocol.ParseRef(req.ID))+" to "+r.PathValue("id"))
}

func (s *Server) handleDetachCampaignMember(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := s.resolveID(w, r, protocol.KindCampaign)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if !campaignMemberKind(kind) {
		render.Error(w, r, http.StatusBadRequest, "invalid_field", `member kind must be "contact" or "company".`)
		return
	}
	entityID, err := s.store.ResolveHandle(sess.WorkspaceID, kind, r.PathValue("memberId"))
	if err != nil {
		s.notFound(w, r, kind)
		return
	}
	if err := s.store.DetachCampaignMember(sess.WorkspaceID, id, kind, entityID); errors.Is(err, store.ErrNotFound) {
		render.Error(w, r, http.StatusNotFound, "not_found", "That entity is not a member of this campaign.")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "campaign.detach", protocol.Handle(protocol.KindCampaign, id), protocol.Handle(kind, entityID))
	render.Text(w, r, http.StatusOK, "OK detached from "+r.PathValue("id"))
}
