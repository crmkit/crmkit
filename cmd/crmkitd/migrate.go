package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/crmkit/crmkit/internal/config"
	"github.com/crmkit/crmkit/internal/store"
)

// migrateUsage documents the migrate subcommand.
var migrateUsage = `crmkitd migrate - inspect and apply database schema migrations

Usage:
  crmkitd migrate [flags]             dry run: report applied vs pending, write nothing
  crmkitd migrate --execute [flags]   apply pending migrations (the only command that writes schema)

The server (crmkitd) never creates or alters schema. On a fresh database, or
after upgrading to a build with new migrations, run "crmkitd migrate --execute"
once (back up first) before starting the server.

Flags:
  --execute            apply pending migrations (default: dry run)
  --config PATH        config file (default: ` + config.DefaultConfigPath() + `, optional)
  --backend NAME       storage backend override: sqlite | postgres
  --dsn DSN            storage DSN override (sqlite path or postgres:// URL)
  --db PATH            sqlite database path override
`

// runMigrate implements `crmkitd migrate`. It returns a process exit code. The
// bare command is a dry run that writes nothing; --execute is the single code
// path in crmkitd that mutates schema (it delegates to store.ApplyMigrations).
func runMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, migrateUsage) }
	execute := fs.Bool("execute", false, "apply pending migrations (default: dry run)")
	configPath := fs.String("config", "", "config file path")
	backend := fs.String("backend", "", "storage backend override: sqlite | postgres")
	dsn := fs.String("dsn", "", "storage DSN override")
	databasePath := fs.String("db", "", "sqlite database path override")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		return 1
	}
	if *backend != "" {
		cfg.Storage.Backend = *backend
	}
	if *dsn != "" {
		cfg.Storage.DSN = *dsn
	}
	if *databasePath != "" {
		cfg.Server.DBPath = *databasePath
	}

	st, err := store.Open(cfg.EffectiveBackend(), cfg.EffectiveDSN(), store.Options{
		MaxOpenConns:    cfg.Storage.MaxOpenConns,
		MaxIdleConns:    cfg.Storage.MaxIdleConns,
		ConnMaxLifetime: time.Duration(cfg.Storage.ConnMaxLifetimeSeconds) * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		return 1
	}
	defer st.Close()

	state, err := st.MigrationStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "read migration status: %v\n", err)
		return 1
	}

	fmt.Printf("backend:   %s\n", cfg.EffectiveBackend())
	fmt.Printf("current:   %s\n", versionLabel(state.Current))
	fmt.Printf("latest:    %d\n", state.Latest)

	if len(state.Pending) == 0 {
		fmt.Println("status:    up to date")
		return 0
	}

	fmt.Printf("status:    %d migration(s) pending\n", len(state.Pending))
	for _, m := range state.Pending {
		fmt.Printf("\npending migration %d (%s):\n", m.Version, m.Name)
		for _, stmt := range m.Statements {
			fmt.Printf("  %s;\n", stmt)
		}
	}

	if !*execute {
		fmt.Println("\ndry run - nothing was written. Re-run with --execute to apply (back up first).")
		return 0
	}

	fmt.Println("\napplying...")
	applied, err := st.ApplyMigrations()
	if err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		return 1
	}
	for _, m := range applied {
		fmt.Printf("applied migration %d (%s)\n", m.Version, m.Name)
	}
	fmt.Printf("done - schema is now at version %d\n", state.Latest)
	return 0
}

func versionLabel(v int) string {
	if v == 0 {
		return "0 (empty database)"
	}
	return fmt.Sprintf("%d", v)
}
