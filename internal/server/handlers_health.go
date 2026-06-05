package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/crmkit/crmkit/internal/render"
)

// handleHealthz is a liveness probe: it returns 200 as long as the process is
// serving, with no dependency checks.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	render.Text(w, r, http.StatusOK, "ok")
}

// handleReadyz is a readiness probe: it returns 200 only if the storage backend
// is reachable, else 503.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.log.Warn("readiness check failed", slog.String("error", err.Error()))
		render.Error(w, r, http.StatusServiceUnavailable, "not_ready", "Storage backend is not reachable.")
		return
	}
	render.Text(w, r, http.StatusOK, "ready")
}
