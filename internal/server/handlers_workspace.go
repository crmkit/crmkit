package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crmkit/crmkit/internal/auth"
	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/render"
	"github.com/crmkit/crmkit/internal/store"
)

// requireRole resolves the current user's role in the path workspace. It writes
// the appropriate error and returns ok=false when the user is not a member, or
// when adminOnly is set and the user is not an admin.
func (s *Server) requireRole(w http.ResponseWriter, r *http.Request, workspaceID string, adminOnly bool) (string, bool) {
	sess := sessionFrom(r)
	role, err := s.store.MemberRole(workspaceID, sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		render.Error(w, r, http.StatusForbidden, "not_a_member",
			"You are not a member of workspace/"+workspaceID+". List yours with GET /workspaces.")
		return "", false
	}
	if err != nil {
		s.serverErr(w, r)
		return "", false
	}
	if adminOnly && role != protocol.RoleAdmin {
		render.Error(w, r, http.StatusForbidden, "admin_required",
			"Only an admin of workspace/"+workspaceID+" can do that.")
		return "", false
	}
	return role, true
}

// handleListWorkspaces lists the workspaces the current user belongs to.
func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	list, err := s.store.ListWorkspacesForUser(sess.UserID)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	list = localizedSlice(list, locationOf(sess))
	render.Respond(w, r, http.StatusOK, list, render.Workspaces(list))
}

type createWorkspaceBody struct {
	Name string `json:"name"`
}

// handleCreateWorkspace creates a new workspace owned by the current user.
func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var body createWorkspaceBody
	if err := decodeJSON(r, &body); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", `Send JSON: {"name":"My Team"}.`)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field", `"name" is required to create a workspace.`)
		return
	}
	if !s.enforceWorkspaceCreateQuota(w, r, sess.UserID) {
		return
	}
	ws, err := s.store.CreateWorkspace(sess.UserID, name)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "workspace.create", "workspace/"+ws.ID, name)
	ws = ws.Localized(locationOf(sess))
	text := render.WorkspaceLine(ws) + "\n# created. Mint a token to act in it: POST /workspaces/" + ws.ID + "/tokens"
	render.Respond(w, r, http.StatusCreated, ws, text)
}

// handleUpdateWorkspace updates a workspace setting - currently its display
// timezone (admin only). Instants stay UTC; this only changes how reads format.
func (s *Server) handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	workspaceID := r.PathValue("id")
	if _, ok := s.requireRole(w, r, workspaceID, true); !ok {
		return
	}
	var body struct {
		Timezone string `json:"timezone"`
	}
	if err := decodeJSON(r, &body); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", `Send JSON, e.g. {"timezone":"America/Los_Angeles"}.`)
		return
	}
	tz := strings.TrimSpace(body.Timezone)
	if tz == "" {
		render.Error(w, r, http.StatusBadRequest, "missing_field",
			`"timezone" is required - an IANA name like "America/New_York", "Europe/London", or "UTC".`)
		return
	}
	if _, err := time.LoadLocation(tz); err != nil {
		render.Error(w, r, http.StatusBadRequest, "invalid_timezone",
			"Unknown timezone "+tz+`. Use an IANA name like "America/New_York", "Europe/London", or "UTC".`)
		return
	}
	if err := s.store.SetWorkspaceTimezone(workspaceID, tz); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r, "workspace")
			return
		}
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "workspace.timezone", "workspace/"+workspaceID, tz)
	render.Respond(w, r, http.StatusOK, map[string]string{"id": workspaceID, "timezone": tz},
		"OK workspace/"+workspaceID+" timezone="+tz+"\n# times in reads now render in "+tz)
}

type mintTokenBody struct {
	TokenName string `json:"token_name,omitempty"`
}

// handleMintWorkspaceToken issues a token scoped to a workspace the user belongs
// to - this is how an agent "switches" workspace.
func (s *Server) handleMintWorkspaceToken(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	workspaceID := r.PathValue("id")
	if _, ok := s.requireRole(w, r, workspaceID, false); !ok {
		return
	}
	var body mintTokenBody
	if err := decodeJSON(r, &body); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", `Send JSON: {"token_name":"my agent"} (or an empty body).`)
		return
	}
	tokenName := strings.TrimSpace(body.TokenName)
	if tokenName == "" {
		tokenName = "default"
	}
	plaintext, hash := auth.GenerateToken()
	if plaintext == "" {
		s.serverErr(w, r)
		return
	}
	tokenID, err := s.store.CreateToken(sess.UserID, workspaceID, tokenName, hash)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sessionForUser(sess.UserID, workspaceID, tokenID), "token.mint", "token/"+tokenID, tokenName)

	resp := map[string]any{
		"status":       "token_minted",
		"token":        plaintext,
		"token_name":   tokenName,
		"workspace_id": workspaceID,
		"message":      "SAVE this token. Send it as Authorization: Bearer <token> to act in workspace/" + workspaceID + ".",
	}
	text := strings.TrimSpace(fmt.Sprintf(`
OK token_minted
token: %s
workspace: %s

Send it as: Authorization: Bearer %s`, plaintext, workspaceID, plaintext))
	render.Respond(w, r, http.StatusCreated, resp, text)
}

// handleListMembers lists members and pending invites of a workspace.
func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("id")
	if _, ok := s.requireRole(w, r, workspaceID, false); !ok {
		return
	}
	members, err := s.store.ListMembers(workspaceID)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	invites, err := s.store.ListInvites(workspaceID)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	members = localizedSlice(members, locationOf(sessionFrom(r)))
	resp := map[string]any{"members": members, "invites": invites}
	render.Respond(w, r, http.StatusOK, resp, render.Members(members, invites))
}

type inviteBody struct {
	Email string `json:"email"`
	Role  string `json:"role,omitempty"`
}

// handleInvite invites an email to the workspace (admin only). The invite is
// consumed into a membership the next time that email authenticates.
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	workspaceID := r.PathValue("id")
	if _, ok := s.requireRole(w, r, workspaceID, true); !ok {
		return
	}
	var body inviteBody
	if err := decodeJSON(r, &body); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", `Send JSON: {"email":"teammate@acme.com","role":"member"}.`)
		return
	}
	email := auth.NormalizeEmail(body.Email)
	if !strings.Contains(email, "@") {
		render.Error(w, r, http.StatusBadRequest, "invalid_email", `Provide a valid email to invite.`)
		return
	}
	role := strings.ToLower(strings.TrimSpace(body.Role))
	if role == "" {
		role = protocol.RoleMember
	}
	if role != protocol.RoleAdmin && role != protocol.RoleMember {
		render.Error(w, r, http.StatusBadRequest, "invalid_role", `role must be "admin" or "member".`)
		return
	}
	// A seat is a member plus any pending invite, so this also bounds invites.
	if !s.enforceWorkspaceQuota(w, r, workspaceID, "members") {
		return
	}
	inv, err := s.store.CreateInvite(workspaceID, email, role, sess.UserID)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	// Notify the invitee with login instructions (best-effort: the invite is
	// valid regardless - they join on next sign-in even if the email fails).
	if err := s.mailer.Send(auth.InviteEmail(email, s.cfg.Server.BaseURL)); err != nil {
		s.log.Warn("invite email failed", "email", email, "error", err.Error())
	}
	s.audit(sess, "member.invite", "workspace/"+workspaceID, email)
	text := fmt.Sprintf("OK invited %s as %s to workspace/%s\nThey join automatically the next time they authenticate with that email.",
		email, role, workspaceID)
	render.Respond(w, r, http.StatusCreated, inv, text)
}

// handleRemoveMember removes a member from a workspace (admin only).
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	workspaceID := r.PathValue("id")
	targetUser := r.PathValue("userId")
	if _, ok := s.requireRole(w, r, workspaceID, true); !ok {
		return
	}
	err := s.store.RemoveMember(workspaceID, targetUser)
	if errors.Is(err, store.ErrNotFound) {
		render.Error(w, r, http.StatusNotFound, "not_found", "That user is not a member of this workspace.")
		return
	}
	if errors.Is(err, store.ErrLastAdmin) {
		render.Error(w, r, http.StatusConflict, "last_admin",
			"You can't remove the last admin. Make someone else an admin first, or delete the workspace.")
		return
	}
	if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "member.remove", "workspace/"+workspaceID, targetUser)
	render.Text(w, r, http.StatusOK, "OK removed member/"+targetUser+" from workspace/"+workspaceID)
}
