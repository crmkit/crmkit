package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/crmkit/crmkit/internal/render"
	"github.com/crmkit/crmkit/internal/store"
)

// handleListTokens lists the caller's own active tokens (across their
// workspaces) so they can review and revoke sessions.
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	list, err := s.store.ListUserTokens(sess.UserID)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	b := strings.Builder{}
	for _, ti := range list {
		lastUsed := "never"
		if ti.LastUsedAt != nil {
			lastUsed = ti.LastUsedAt.UTC().Format("2006-01-02")
		}
		current := ""
		if ti.ID == sess.TokenID {
			current = "yes"
		}
		b.WriteString(render.Line("token/"+ti.ID,
			render.F("name", ti.Name),
			render.F("workspace", ti.WorkspaceID),
			render.F("created", ti.CreatedAt.UTC().Format("2006-01-02")),
			render.F("last_used", lastUsed),
			render.F("current", current),
		))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %d token(s)", len(list))
	render.Respond(w, r, http.StatusOK, list, b.String())
}

// handleRevokeToken revokes one of the caller's own tokens by id.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id := r.PathValue("id")
	if err := s.store.RevokeUserToken(sess.UserID, id); errors.Is(err, store.ErrNotFound) {
		render.Error(w, r, http.StatusNotFound, "not_found", "No such token of yours. List them with GET /tokens.")
		return
	} else if err != nil {
		s.serverErr(w, r)
		return
	}
	s.audit(sess, "token.revoke", "token/"+id, "")
	render.Text(w, r, http.StatusOK, "OK revoked token/"+id)
}
