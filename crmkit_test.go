package crmkit

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crmkit/crmkit/internal/config"
	"github.com/crmkit/crmkit/internal/store"
)

// TestFacadeExtension proves the embedding path: built-in routes serve through
// App.Handler, and an added route is registered and wrapped by crmkit's auth.
func TestFacadeExtension(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Local = true

	st, err := store.Open("sqlite", ":memory:", store.Options{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.ApplyMigrations(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	app, err := New(cfg, st)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { app.Close() })

	app.Handle("GET /x/ping", app.Authed(func(w http.ResponseWriter, r *http.Request) {
		// Reached only if authenticated; the test exercises the unauth path.
		_ = SessionFrom(r)
		w.WriteHeader(http.StatusTeapot)
	}))

	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)

	// A built-in route still serves through the facade handler.
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz via facade: err=%v status=%v", err, resp)
	}
	resp.Body.Close()

	// The extension route is registered AND protected by crmkit auth: an
	// unauthenticated call yields 401 (not 404), proving both wiring and auth.
	resp, err = http.Get(ts.URL + "/x/ping")
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("extension route should require auth (401), got %d", resp.StatusCode)
	}
}
