// Command crmkitd runs the crmkit HTTP API server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/crmkit/crmkit/internal/auth"
	"github.com/crmkit/crmkit/internal/config"
	"github.com/crmkit/crmkit/internal/ratelimit"
	"github.com/crmkit/crmkit/internal/server"
	"github.com/crmkit/crmkit/internal/store"
	"github.com/crmkit/crmkit/internal/version"
)

func main() {
	// Subcommands are dispatched before the server flags are parsed. "migrate"
	// is the only code path that writes schema; the server itself never does.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		os.Exit(runMigrate(os.Args[2:]))
	}

	configPath := flag.String("config", "", "path to crmkit config (default: "+config.DefaultConfigPath()+", optional)")
	listenAddr := flag.String("listen", "", "override listen address (e.g. :8080)")
	databasePath := flag.String("db", "", "override sqlite database path")
	backend := flag.String("backend", "", "override storage backend: sqlite | postgres")
	dsn := flag.String("dsn", "", "override storage DSN (sqlite path or postgres:// URL)")
	baseURL := flag.String("base-url", "", "override the public base URL advertised to agents")
	logFormat := flag.String("log-format", "", "override log format: text | json")
	local := flag.Bool("local", false, "local mode: single-user, no mail provider needed; login codes are logged/echoed so an agent on this machine can self-authenticate")
	debug := flag.Bool("debug", false, "enable debug-level logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("crmkitd %s\n", version.Version)
		os.Exit(0)
	}

	cfg := loadConfig(*configPath)

	// Apply CLI overrides.
	if *listenAddr != "" {
		cfg.Server.ListenAddr = *listenAddr
	}
	if *databasePath != "" {
		cfg.Server.DBPath = *databasePath
	}
	if *backend != "" {
		cfg.Storage.Backend = *backend
	}
	if *dsn != "" {
		cfg.Storage.DSN = *dsn
	}
	if *baseURL != "" {
		cfg.Server.BaseURL = *baseURL
	}
	if *logFormat != "" {
		cfg.Server.LogFormat = *logFormat
	}
	if *local {
		cfg.Server.Local = true
	}

	setupLogging(cfg.Server.LogFormat, *debug)
	slog.Info("crmkitd starting", "version", version.Version)

	// Validate the fully-merged config (file + env + flags + defaults).
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	// Fail closed: refuse to run in a config where login/step-up codes would be
	// written to logs (log email mode outside dev).
	if err := cfg.CheckMailDelivery(); err != nil {
		slog.Error("insecure configuration", "error", err)
		os.Exit(1)
	}
	// Key the code hashers. An unset secret is generated per-process: fine for a
	// single dev instance, but it won't survive restarts or work across replicas.
	if cfg.Server.SecretKey == "" {
		cfg.Server.SecretKey = auth.GenerateSecret()
		slog.Warn("no server.secret_key set; generated an ephemeral key (login/step-up codes will not survive a restart or work across multiple instances - set server.secret_key in production)")
	}

	if err := run(cfg); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

// setupLogging installs the default structured logger.
func setupLogging(format string, debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

// loadConfig layers defaults < file < env. A missing default config file is
// fine (env vars alone can configure the server); a bad explicit file is fatal.
func loadConfig(path string) config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

func run(cfg config.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slog.Info("opening storage backend", "backend", cfg.EffectiveBackend())
	st, err := store.Open(cfg.EffectiveBackend(), cfg.EffectiveDSN(), store.Options{
		MaxOpenConns:    cfg.Storage.MaxOpenConns,
		MaxIdleConns:    cfg.Storage.MaxIdleConns,
		ConnMaxLifetime: time.Duration(cfg.Storage.ConnMaxLifetimeSeconds) * time.Second,
	})
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Fail closed: the server never creates or alters schema. If the database is
	// empty or behind on migrations, refuse to start and tell the operator to run
	// the explicit (back-up-friendly) migrate step first.
	state, err := st.MigrationStatus()
	if err != nil {
		return fmt.Errorf("check schema version: %w", err)
	}
	if len(state.Pending) > 0 {
		pending := make([]int, len(state.Pending))
		for i, m := range state.Pending {
			pending[i] = m.Version
		}
		slog.Error("database schema is not up to date; run `crmkitd migrate --execute` (after backing up) before starting the server",
			"current_version", state.Current, "latest_version", state.Latest, "pending", pending)
		return fmt.Errorf("%d schema migration(s) pending", len(state.Pending))
	}

	st.SetTokenIdleTTL(time.Duration(cfg.Server.TokenIdleTTLSeconds) * time.Second)
	st.SetDefaultPlan(cfg.Plans.Default)

	rl, err := ratelimit.Open(cfg.RateLimit.Backend, cfg.RateLimit.DSN)
	if err != nil {
		return fmt.Errorf("open rate limiter: %w", err)
	}
	defer rl.Close()

	srv := server.New(cfg, st, rl)

	httpServer := &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Server.ListenAddr, "base_url", cfg.Server.BaseURL)
		if cfg.Server.Local {
			slog.Warn("local mode ON: login codes are logged and echoed in responses - single-user only, do not expose to the internet")
		}
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
