package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crmkit/crmkit/internal/auth"
	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/render"
	"github.com/crmkit/crmkit/internal/store"
)

// escalationTTL returns how long an emailed step-up code stays valid, from
// config (with a sane fallback).
func (s *Server) escalationTTL() time.Duration {
	if s.cfg.Server.EscalationTTLSeconds > 0 {
		return time.Duration(s.cfg.Server.EscalationTTLSeconds) * time.Second
	}
	return 10 * time.Minute
}

// stepUpCode extracts an escalation code from the request (query or header).
func stepUpCode(r *http.Request) string {
	if c := strings.TrimSpace(r.URL.Query().Get("code")); c != "" {
		return c
	}
	return strings.TrimSpace(r.Header.Get("X-Escalation-Code"))
}

// requireEscalation enforces an email step-up ("are you sure?") for a sensitive
// action. With no code it creates a challenge, emails the acting user a code,
// and returns false. With a valid code it consumes the challenge and returns
// true. The challenge is bound to (user, action, target) so a code for one
// operation cannot authorize another.
func (s *Server) requireEscalation(w http.ResponseWriter, r *http.Request, sess protocol.Session, action, target, desc string) bool {
	code := stepUpCode(r)
	if code == "" {
		c := auth.GenerateCode()
		if err := s.store.PutEscalation(sess.UserID, action, target,
			auth.HashStepUp(s.cfg.Server.SecretKey, sess.UserID, action, target, c), time.Now().Add(s.escalationTTL())); err != nil {
			s.serverErr(w, r)
			return false
		}
		if s.cfg.Server.Local {
			s.log.Info("escalation code (local)",
				slog.String("email", sess.Email), slog.String("action", action), slog.String("code", c))
		}
		if err := s.mailer.Send(auth.EscalationEmail(sess.Email, desc, c, int(s.escalationTTL().Minutes()))); err != nil {
			s.log.Error("escalation email send failed", slog.String("email", sess.Email), slog.String("action", action), slog.String("error", err.Error()))
		}

		hint := fmt.Sprintf("This action (%s) needs email confirmation. A code was sent to %s. Ask the user for it, then repeat this exact request with ?code=<code>.", desc, sess.Email)
		if s.cfg.Server.Local {
			hint += " (local code: " + c + ")"
		}
		render.Error(w, r, http.StatusForbidden, "escalation_required", hint)
		return false
	}

	ok, err := s.store.VerifyEscalation(sess.UserID, action, target,
		auth.HashStepUp(s.cfg.Server.SecretKey, sess.UserID, action, target, code), time.Now())
	if err != nil {
		s.serverErr(w, r)
		return false
	}
	if !ok {
		render.Error(w, r, http.StatusForbidden, "invalid_code",
			"That confirmation code is wrong or expired. Repeat the request with no code to get a fresh one.")
		return false
	}
	return true
}

type setRoleBody struct {
	Role string `json:"role"`
}

// handleSetMemberRole changes a member's role. Promotion to admin requires an
// email step-up; demotion is a direct admin action (guarded against removing
// the last admin).
func (s *Server) handleSetMemberRole(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	workspaceID := r.PathValue("id")
	targetUser := r.PathValue("userId")
	if _, ok := s.requireRole(w, r, workspaceID, true); !ok {
		return
	}

	var body setRoleBody
	if err := decodeJSON(r, &body); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", `Send JSON: {"role":"admin"} or {"role":"member"}.`)
		return
	}
	role := strings.ToLower(strings.TrimSpace(body.Role))
	if role != protocol.RoleAdmin && role != protocol.RoleMember {
		render.Error(w, r, http.StatusBadRequest, "invalid_role", `role must be "admin" or "member".`)
		return
	}

	// Escalating someone to admin is the standard step-up procedure.
	if role == protocol.RoleAdmin {
		if !s.requireEscalation(w, r, sess, "member.promote", workspaceID+":"+targetUser, "promote a member to admin") {
			return
		}
	}

	switch err := s.store.SetMemberRole(workspaceID, targetUser, role); {
	case errors.Is(err, store.ErrNotFound):
		render.Error(w, r, http.StatusNotFound, "not_found", "That user is not a member of this workspace.")
		return
	case errors.Is(err, store.ErrLastAdmin):
		render.Error(w, r, http.StatusConflict, "last_admin", "You can't demote the last admin. Promote someone else first.")
		return
	case err != nil:
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "member.role", "workspace/"+workspaceID, targetUser+"="+role)
	render.Text(w, r, http.StatusOK, "OK member/"+targetUser+" role="+role)
}

// handleDeleteWorkspace permanently deletes a workspace and all its data. It is
// admin-only and gated behind the email step-up procedure.
func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	workspaceID := r.PathValue("id")
	if _, ok := s.requireRole(w, r, workspaceID, true); !ok {
		return
	}
	if !s.requireEscalation(w, r, sess, "workspace.delete", workspaceID, "delete this workspace and all its data") {
		return
	}

	if err := s.store.DeleteWorkspace(workspaceID); errors.Is(err, store.ErrNotFound) {
		render.Error(w, r, http.StatusNotFound, "not_found", "No such workspace.")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	// The workspace (and its audit log) is gone; record the deletion in the log.
	s.log.Info("workspace deleted", slog.String("workspace", workspaceID), slog.String("by", sess.Email))
	render.Text(w, r, http.StatusOK, "OK deleted workspace/"+workspaceID+" and all its data")
}
