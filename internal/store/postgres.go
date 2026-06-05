package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
)

// Default connection-pool sizing for Postgres when Options leaves a field unset.
const (
	defaultMaxOpenConns    = 10
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = time.Hour
)

// openPostgres connects to Postgres via the pure-Go pgx driver. dsn is a
// connection URL, e.g. "postgres://user:pass@host:5432/crmkit?sslmode=require".
// The pool is sized from opts, falling back to defaults for any zero field. It
// does NOT touch the schema - that happens only via ApplyMigrations (see
// migrate.go).
func openPostgres(dsn string, opts Options) (*sqlStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	maxOpen := opts.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = defaultMaxOpenConns
	}
	maxIdle := opts.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = defaultMaxIdleConns
	}
	lifetime := opts.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = defaultConnMaxLifetime
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(lifetime)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	return &sqlStore{db: db, d: postgresDialect, tokenIdleTTL: defaultTokenIdleTTL}, nil
}
