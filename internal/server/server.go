// Package server implements the crmkit HTTP API: a plain-text-first,
// agent-friendly CRM surface. Responses default to grepable text and switch to
// JSON on demand (see internal/render). Every mutating call is attributed to a
// bearer token and recorded in the audit log.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crmkit/crmkit/internal/auth"
	"github.com/crmkit/crmkit/internal/config"
	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/ratelimit"
	"github.com/crmkit/crmkit/internal/render"
	"github.com/crmkit/crmkit/internal/store"
)

// Server holds the running API dependencies.
type Server struct {
	cfg         config.Config
	store       store.Store
	mailer      auth.Mailer
	log         *slog.Logger
	ipLimiter   ratelimit.Limiter // general per-client-IP request limit
	authLimiter ratelimit.Limiter // stricter per-email limit on login attempts
	startedAt   time.Time

	// internalOnce/internalMux back the MCP tool dispatcher: an unmiddlewared
	// route table the /mcp handler replays synthesized HTTP requests against, so
	// MCP tools reuse the exact CRM handlers (auth, validation, plain-text output).
	internalOnce sync.Once
	internalMux  http.Handler

	// lastAuditPrune is the unix time of the last audit-retention sweep; the prune
	// piggybacks on audit writes, throttled to at most once per auditPruneInterval.
	lastAuditPrune atomic.Int64
}

// New constructs a Server from a loaded config, an open store backend, and a
// rate-limit provider (which determines whether limiting is in-process or
// shared across replicas).
func New(cfg config.Config, st store.Store, rl ratelimit.Provider) *Server {
	// The auth limiter caps login attempts per email at AuthPerHour, with a
	// small burst so a few quick retries are allowed.
	authBurst := 3
	if h := cfg.RateLimit.AuthPerHour; h > 0 && h < authBurst {
		authBurst = h
	}
	return &Server{
		cfg:         cfg,
		store:       st,
		mailer:      auth.NewMailer(cfg.Email),
		log:         slog.Default(),
		ipLimiter:   rl.Limiter("ip", cfg.RateLimit.RPS, cfg.RateLimit.Burst),
		authLimiter: rl.Limiter("auth", float64(cfg.RateLimit.AuthPerHour)/3600.0, authBurst),
		startedAt:   time.Now(),
	}
}

// Handler returns the root HTTP handler with all routes registered and
// middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.Routes(mux)
	return s.Middleware(mux)
}

// Routes registers the built-in API routes onto mux. It is exported so an
// embedding program can add its own routes to the same mux and then wrap it with
// Middleware (see the root crmkit package). Extra routes with more specific
// patterns take precedence over the built-ins.
func (s *Server) Routes(mux *http.ServeMux) {
	// Health probes - unauthenticated and exempt from rate limiting.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.HandleFunc("GET /help", s.handleHelp)
	mux.HandleFunc("GET /.well-known/agent.md", s.handleManualFile)
	mux.HandleFunc("GET /.well-known/agent.json", s.handleAgentCard)

	mux.HandleFunc("POST /auth/request", s.handleAuthRequest)
	mux.HandleFunc("POST /auth/verify", s.handleAuthVerify)
	mux.HandleFunc("GET /whoami", s.authed(s.handleWhoami))

	// Self-service token management.
	mux.HandleFunc("GET /tokens", s.authed(s.handleListTokens))
	mux.HandleFunc("DELETE /tokens/{id}", s.authed(s.handleRevokeToken))

	// Workspaces & membership (admin - scoped by the path workspace, authorized
	// by the caller's identity, not the token's workspace).
	mux.HandleFunc("GET /workspaces", s.authed(s.handleListWorkspaces))
	mux.HandleFunc("POST /workspaces", s.authed(s.handleCreateWorkspace))
	mux.HandleFunc("PATCH /workspaces/{id}", s.authed(s.handleUpdateWorkspace))
	mux.HandleFunc("POST /workspaces/{id}/tokens", s.authed(s.handleMintWorkspaceToken))
	mux.HandleFunc("GET /workspaces/{id}/members", s.authed(s.handleListMembers))
	mux.HandleFunc("POST /workspaces/{id}/invites", s.authed(s.handleInvite))
	mux.HandleFunc("POST /workspaces/{id}/members/{userId}/role", s.authed(s.handleSetMemberRole))
	mux.HandleFunc("DELETE /workspaces/{id}/members/{userId}", s.authed(s.handleRemoveMember))
	mux.HandleFunc("DELETE /workspaces/{id}", s.authed(s.handleDeleteWorkspace))

	// Cross-entity search ("find anything" across contacts, companies, deals).
	mux.HandleFunc("GET /search", s.authed(s.handleSearch))

	// Contacts.
	mux.HandleFunc("GET /contacts", s.authed(s.handleListContacts))
	mux.HandleFunc("POST /contacts", s.authed(s.handleCreateContact))
	mux.HandleFunc("GET /contacts/{id}", s.authed(s.handleGetContact))
	mux.HandleFunc("PATCH /contacts/{id}", s.authed(s.handleUpdateContact))
	mux.HandleFunc("DELETE /contacts/{id}", s.authed(s.handleDeleteContact))
	mux.HandleFunc("GET /contacts/{id}/activities", s.authed(s.handleListContactActivities))
	mux.HandleFunc("POST /contacts/{id}/activities", s.authed(s.handleCreateContactActivity))

	// Companies.
	mux.HandleFunc("GET /companies", s.authed(s.handleListCompanies))
	mux.HandleFunc("POST /companies", s.authed(s.handleCreateCompany))
	mux.HandleFunc("GET /companies/{id}", s.authed(s.handleGetCompany))
	mux.HandleFunc("PATCH /companies/{id}", s.authed(s.handleUpdateCompany))
	mux.HandleFunc("DELETE /companies/{id}", s.authed(s.handleDeleteCompany))
	mux.HandleFunc("GET /companies/{id}/activities", s.authed(s.handleListCompanyActivities))
	mux.HandleFunc("POST /companies/{id}/activities", s.authed(s.handleCreateCompanyActivity))

	// Deals.
	mux.HandleFunc("GET /deals", s.authed(s.handleListDeals))
	mux.HandleFunc("POST /deals", s.authed(s.handleCreateDeal))
	mux.HandleFunc("GET /deals/{id}", s.authed(s.handleGetDeal))
	mux.HandleFunc("PATCH /deals/{id}", s.authed(s.handleUpdateDeal))
	mux.HandleFunc("DELETE /deals/{id}", s.authed(s.handleDeleteDeal))

	mux.HandleFunc("GET /tickets", s.authed(s.handleListTickets))
	mux.HandleFunc("POST /tickets", s.authed(s.handleCreateTicket))
	mux.HandleFunc("GET /tickets/{id}", s.authed(s.handleGetTicket))
	mux.HandleFunc("PATCH /tickets/{id}", s.authed(s.handleUpdateTicket))
	mux.HandleFunc("DELETE /tickets/{id}", s.authed(s.handleDeleteTicket))
	mux.HandleFunc("GET /tickets/{id}/activities", s.authed(s.handleListTicketActivities))
	mux.HandleFunc("POST /tickets/{id}/activities", s.authed(s.handleCreateTicketActivity))

	// Campaigns: a prospecting effort (brief + members). Members are contacts and
	// companies attached many-to-many, deduped per campaign.
	mux.HandleFunc("GET /campaigns", s.authed(s.handleListCampaigns))
	mux.HandleFunc("POST /campaigns", s.authed(s.handleCreateCampaign))
	mux.HandleFunc("GET /campaigns/{id}", s.authed(s.handleGetCampaign))
	mux.HandleFunc("PATCH /campaigns/{id}", s.authed(s.handleUpdateCampaign))
	mux.HandleFunc("DELETE /campaigns/{id}", s.authed(s.handleDeleteCampaign))
	mux.HandleFunc("GET /campaigns/{id}/members", s.authed(s.handleListCampaignMembers))
	mux.HandleFunc("POST /campaigns/{id}/members", s.authed(s.handleAttachCampaignMember))
	mux.HandleFunc("DELETE /campaigns/{id}/members/{kind}/{memberId}", s.authed(s.handleDetachCampaignMember))

	// Reminders, activities & audit.
	mux.HandleFunc("GET /reminders", s.authed(s.handleListReminders))
	mux.HandleFunc("GET /activities", s.authed(s.handleListActivities))
	mux.HandleFunc("DELETE /activities/{id}", s.authed(s.handleDeleteActivity))
	mux.HandleFunc("GET /audit", s.authed(s.handleListAudit))

	// MCP (Model Context Protocol) endpoint - JSON-RPC over Streamable HTTP. It
	// does its own bearer auth so it can answer an unauthenticated request with
	// the 401 + WWW-Authenticate that bootstraps the OAuth flow below.
	mux.HandleFunc("POST /mcp", s.handleMCP)
	mux.HandleFunc("GET /mcp", s.handleMCPGet)

	// OAuth 2.1 authorization server backing the MCP connector (see handlers_oauth.go).
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleOAuthProtectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.handleOAuthProtectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleOAuthAuthorizationServer)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/mcp", s.handleOAuthAuthorizationServer)
	mux.HandleFunc("POST /oauth/register", s.handleOAuthRegister)
	mux.HandleFunc("GET /oauth/authorize", s.handleOAuthAuthorizeGet)
	mux.HandleFunc("POST /oauth/authorize", s.handleOAuthAuthorizePost)
	mux.HandleFunc("POST /oauth/token", s.handleOAuthToken)
	mux.HandleFunc("POST /oauth/revoke", s.handleOAuthRevoke)
}

// internalHandler returns an unmiddlewared route table used by the MCP tool
// dispatcher to replay synthesized HTTP requests against the CRM handlers. It
// excludes the request-logging and rate-limit middleware (a tool call is not a
// public request) but keeps per-route bearer auth, so a tool runs with exactly
// the permissions of the token that called /mcp.
func (s *Server) internalHandler() http.Handler {
	s.internalOnce.Do(func() {
		mux := http.NewServeMux()
		s.Routes(mux)
		s.internalMux = mux
	})
	return s.internalMux
}

// Middleware applies crmkit's request logging and per-client-IP rate limiting
// around a handler (the built-in routes and any added by an embedder).
func (s *Server) Middleware(next http.Handler) http.Handler {
	return s.requestLog(s.rateLimit(next))
}

// Authed wraps a handler so it requires a valid bearer token; the resolved
// session is retrievable with SessionFrom. Exposed for embedders adding
// authenticated routes that reuse crmkit's token auth.
func (s *Server) Authed(next http.HandlerFunc) http.HandlerFunc { return s.authed(next) }

// SessionFrom returns the authenticated session (workspace + user) on a request
// handled behind Authed.
func SessionFrom(r *http.Request) protocol.Session { return sessionFrom(r) }

// ---- middleware ----------------------------------------------------------

type ctxKey int

const sessionKey ctxKey = iota

// statusRecorder captures the response status code for request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// requestLog emits one structured log line per request (method, path, status,
// duration, client IP).
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sr, r)

		level := slog.LevelInfo
		if sr.status >= 500 {
			level = slog.LevelError
		} else if sr.status == http.StatusTooManyRequests {
			level = slog.LevelWarn
		}
		s.log.LogAttrs(r.Context(), level, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", sr.status),
			slog.Int64("dur_ms", time.Since(start).Milliseconds()),
			slog.String("ip", s.clientIP(r)),
		)
	})
}

// rateLimit applies the per-client-IP limiter to every request except health
// probes. On rejection it returns 429 with a Retry-After hint.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.ipLimiter.Allow(s.clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			render.Error(w, r, http.StatusTooManyRequests, "rate_limited",
				"Too many requests from your address. Slow down and retry shortly.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP resolves the caller's IP, honoring proxy headers only when the
// deployment is configured to trust them.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.Server.TrustProxyHeaders {
		if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
			return cf
		}
		if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return xff
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authed wraps a handler requiring a valid bearer token, attaching the resolved
// session to the request context.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Internal MCP tool dispatch resolves the token once and injects the
		// session into the context; reuse it rather than resolving again. The
		// context key is unexported, so only trusted in-process callers can set it.
		if sess := sessionFrom(r); sess.TokenID != "" {
			next(w, r)
			return
		}
		token := bearerToken(r)
		if token == "" {
			render.Error(w, r, http.StatusUnauthorized, "auth_required",
				"No token. POST /auth/request {\"email\":\"you@example.com\"} to get a login code, then POST /auth/verify.")
			return
		}
		sess, err := s.store.ResolveToken(auth.HashToken(token))
		if errors.Is(err, store.ErrNotFound) {
			render.Error(w, r, http.StatusUnauthorized, "invalid_token",
				"Token is unknown or revoked. Re-authenticate via POST /auth/request then /auth/verify.")
			return
		}
		if errors.Is(err, store.ErrTokenExpired) {
			render.Error(w, r, http.StatusUnauthorized, "token_expired",
				"Token expired after inactivity. Re-authenticate via POST /auth/request then /auth/verify.")
			return
		}
		if err != nil {
			render.Error(w, r, http.StatusInternalServerError, "server_error", "Try again shortly.")
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey, sess)
		next(w, r.WithContext(ctx))
	}
}

func sessionFrom(r *http.Request) protocol.Session {
	sess, _ := r.Context().Value(sessionKey).(protocol.Session)
	return sess
}

// sessionForUser builds a minimal session for audit writes that happen before a
// request context exists (e.g. during token minting).
func sessionForUser(userID, workspaceID, tokenID string) protocol.Session {
	return protocol.Session{UserID: userID, WorkspaceID: workspaceID, TokenID: tokenID}
}

func bearerToken(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[len("bearer "):])
	}
	return h
}

// ---- helpers -------------------------------------------------------------

// readBody reads the (size-limited) request body.
func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}

// decodeBytes decodes JSON bytes into dst, rejecting unknown fields. Empty input
// is not an error (yields the zero value), so callers may apply an empty patch.
func decodeBytes(b []byte, dst any) error {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// decodeJSON reads a JSON request body into dst. A missing/empty body is not an
// error (yielding the zero value) so agents may PATCH with an empty object.
func decodeJSON(r *http.Request, dst any) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	return decodeBytes(body, dst)
}

// audit records an action, logging (but not failing) on error.
func (s *Server) audit(sess protocol.Session, action, target, detail string) {
	if err := s.store.WriteAudit(sess.WorkspaceID, sess.TokenID, sess.Email, action, target, detail); err != nil {
		s.log.Warn("audit write failed", slog.String("error", err.Error()))
	}
	s.maybePruneAudit()
}

// auditPruneInterval throttles how often the retention sweep runs - it
// piggybacks on audit writes (which is exactly when the table grows), so no
// always-on goroutine is needed (a good fit for scale-to-zero deployments).
const auditPruneInterval = time.Hour

// maybePruneAudit fires the retention sweep at most once per auditPruneInterval,
// in the background so it never adds latency to the request. Retention is a
// per-plan window, so the sweep prunes each plan against its own cutoff; a plan
// with AuditRetentionDays <= 0 keeps its audit forever.
func (s *Server) maybePruneAudit() {
	now := time.Now().Unix()
	last := s.lastAuditPrune.Load()
	if now-last < int64(auditPruneInterval.Seconds()) {
		return
	}
	// Only one caller wins the slot; others skip until the next interval.
	if !s.lastAuditPrune.CompareAndSwap(last, now) {
		return
	}
	go func() {
		for plan, limits := range s.cfg.Plans.Catalogue {
			days := limits.AuditRetentionDays
			if days <= 0 {
				continue // this plan keeps audit forever
			}
			cutoff := time.Now().AddDate(0, 0, -days).Unix()
			n, err := s.store.PruneAuditForPlan(plan, cutoff)
			if err != nil {
				s.log.Warn("audit prune failed", slog.String("plan", plan), slog.String("error", err.Error()))
				continue
			}
			if n > 0 {
				s.log.Info("pruned audit log", slog.String("plan", plan), slog.Int64("deleted", n), slog.Int("retention_days", days))
			}
		}
	}()
}
