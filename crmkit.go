// Package crmkit is the public, embeddable API for the crmkit server. It lets an
// external program build the full crmkit HTTP API and extend it with additional
// routes that reuse crmkit's storage and bearer-token auth - while crmkit's
// internals stay internal. The standalone daemon is cmd/crmkitd; an extended
// deployment imports this package (see the crmkit/api deployment repo).
package crmkit

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/crmkit/crmkit/internal/auth"
	"github.com/crmkit/crmkit/internal/config"
	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/ratelimit"
	"github.com/crmkit/crmkit/internal/render"
	"github.com/crmkit/crmkit/internal/server"
	"github.com/crmkit/crmkit/internal/store"
	"github.com/crmkit/crmkit/internal/version"
)

// Re-exported types so extenders can name them without reaching into internal/.
type (
	// Config is the fully-resolved crmkit configuration. Load it with Load; its
	// Validate and CheckMailDelivery methods enforce the same safety checks the
	// standalone server runs.
	Config = config.Config
	// Store is the persistence layer shared by built-in and extension routes.
	Store = store.Store
	// Session is the authenticated identity (workspace + user) behind a token.
	Session = protocol.Session
)

// CoreVersion reports the build version of the linked crmkit core.
func CoreVersion() string { return version.Version }

// Respond writes a response in the negotiated format, exactly like the built-in
// endpoints: jsonObj when the client asks for JSON (Accept: application/json or
// ?format=json), otherwise the plain-text body. Use it from extension routes so
// they format like first-class crmkit routes (plain text by default).
func Respond(w http.ResponseWriter, r *http.Request, status int, jsonObj any, text string) {
	render.Respond(w, r, status, jsonObj, text)
}

// Error writes an instructive error in the negotiated format (JSON {error,hint}
// or plain text), matching the built-in endpoints.
func Error(w http.ResponseWriter, r *http.Request, status int, code, hint string) {
	render.Error(w, r, status, code, hint)
}

// WantJSON reports whether the client negotiated a JSON response.
func WantJSON(r *http.Request) bool { return render.WantJSON(r) }

// Load reads configuration, layering defaults < file < env. A missing default
// file is fine (env vars alone can configure the server).
func Load(path string) (Config, error) { return config.Load(path) }

// DefaultConfigPath is the default config file location.
func DefaultConfigPath() string { return config.DefaultConfigPath() }

// GenerateSecret returns a random key suitable for Config.Server.SecretKey.
func GenerateSecret() string { return auth.GenerateSecret() }

// Open connects the storage backend described by cfg (backend inferred from the
// DSN). It does NOT create schema: check Store.MigrationStatus() and run
// migrations out of band before serving (crmkitd refuses to start otherwise).
func Open(cfg Config) (Store, error) {
	return store.Open(cfg.EffectiveBackend(), cfg.EffectiveDSN(), store.Options{
		MaxOpenConns:    cfg.Storage.MaxOpenConns,
		MaxIdleConns:    cfg.Storage.MaxIdleConns,
		ConnMaxLifetime: time.Duration(cfg.Storage.ConnMaxLifetimeSeconds) * time.Second,
	})
}

// App is a crmkit server you can extend with additional routes before serving.
type App struct {
	srv *server.Server
	mux *http.ServeMux
	st  Store
	cfg Config
	rl  ratelimit.Provider
}

// New builds the base crmkit app from a loaded config and an open store: all
// built-in routes are registered, token idle TTL and default plan are applied,
// and the rate-limit provider is opened. Add your own routes with Handle, then
// serve with Handler/ListenAndServe. Call Close when done.
func New(cfg Config, st Store) (*App, error) {
	rl, err := ratelimit.Open(cfg.RateLimit.Backend, cfg.RateLimit.DSN)
	if err != nil {
		return nil, err
	}
	st.SetTokenIdleTTL(time.Duration(cfg.Server.TokenIdleTTLSeconds) * time.Second)
	st.SetDefaultPlan(cfg.Plans.Default)

	srv := server.New(cfg, st, rl)
	mux := http.NewServeMux()
	srv.Routes(mux)
	return &App{srv: srv, mux: mux, st: st, cfg: cfg, rl: rl}, nil
}

// Handle registers an additional route, using Go 1.22 "METHOD /path" patterns.
// A more specific pattern takes precedence over a built-in, so you can add new
// paths or override existing ones. Wrap the handler with Authed to require a
// bearer token.
func (a *App) Handle(pattern string, h http.HandlerFunc) { a.mux.HandleFunc(pattern, h) }

// Authed wraps a handler so it requires a valid bearer token; the resolved
// session is available via SessionFrom. Extension routes reuse crmkit's auth.
func (a *App) Authed(h http.HandlerFunc) http.HandlerFunc { return a.srv.Authed(h) }

// SessionFrom returns the authenticated session on a request handled behind
// Authed - use it to scope extension queries to the caller's workspace.
func SessionFrom(r *http.Request) Session { return server.SessionFrom(r) }

// Store exposes the persistence layer so extension handlers read/write the same
// data (workspace-scoped) as the built-in endpoints.
func (a *App) Store() Store { return a.st }

// Config returns the loaded configuration.
func (a *App) Config() Config { return a.cfg }

// Handler returns the root HTTP handler (built-ins + extensions) with crmkit's
// request logging and rate limiting applied to all routes.
func (a *App) Handler() http.Handler { return a.srv.Middleware(a.mux) }

// RouteHandler returns the bare route mux WITHOUT the outer middleware (request
// logging, rate limiting). It is for in-process dispatch: an extension that
// wants to invoke crmkit's own endpoints internally (e.g. an MCP tool that
// replays a synthesized request) should target this, so a single external call
// is not rate-limited or logged twice. Per-route auth (Authed) still applies, so
// the synthesized request must carry its own Authorization header.
func (a *App) RouteHandler() http.Handler { return a.mux }

// Close releases App-owned resources (the rate-limit provider). The store is
// owned by the caller (close it separately).
func (a *App) Close() error { return a.rl.Close() }

// ListenAndServe serves the app on addr, shutting down gracefully when ctx is
// cancelled.
func (a *App) ListenAndServe(ctx context.Context, addr string) error {
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
