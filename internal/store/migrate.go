package store

import (
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// Migration is one ordered schema change: a set of statements applied
// atomically and recorded so it runs exactly once. Version must be unique and
// ascending. Statements run one at a time (some drivers reject multi-statement
// Exec), each through the dialect's placeholder rebinding.
type Migration struct {
	Version    int
	Name       string
	Statements []string
}

// migrations is the ordered, authoritative schema history. Version 1 is the
// baseline (the full current schema); later versions are deltas (ALTER TABLE,
// new tables/indexes) appended over time. This is the ONLY place that defines
// schema, and ApplyMigrations is the ONLY code that writes it - so the
// data-touching surface is a single, auditable unit. The server never writes
// schema; it only reads MigrationStatus and refuses to start if anything is
// pending (run `crmkit migrate --execute`).
var migrations = []Migration{
	{Version: 1, Name: "initial schema", Statements: baselineSchema},
	// v2: per-tenant plan assignment. 'basic' matches config.DefaultPlan; the
	// app sets the plan explicitly on insert, this default just backfills any
	// existing rows. ADD COLUMN with a constant default works on both backends.
	{Version: 2, Name: "plan columns", Statements: []string{
		`ALTER TABLE workspaces ADD COLUMN plan TEXT NOT NULL DEFAULT 'basic'`,
		`ALTER TABLE users ADD COLUMN plan TEXT NOT NULL DEFAULT 'basic'`,
	}},
	// v3: MCP OAuth 2.1 authorization server. oauth_clients holds dynamically
	// registered MCP clients (RFC 7591); oauth_codes holds single-use, short-TTL
	// authorization codes (the PKCE code_challenge is stored, never the verifier).
	// Both are minted/consumed by the /oauth/* handlers; the access token they
	// ultimately issue is a normal row in tokens, so /mcp reuses the same auth.
	{Version: 3, Name: "mcp oauth", Statements: []string{
		`CREATE TABLE IF NOT EXISTS oauth_clients (
	id            TEXT PRIMARY KEY,
	redirect_uris TEXT NOT NULL,
	client_name   TEXT,
	created_at    BIGINT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS oauth_codes (
	code_hash      TEXT PRIMARY KEY,
	client_id      TEXT NOT NULL,
	user_id        TEXT NOT NULL,
	workspace_id   TEXT NOT NULL,
	redirect_uri   TEXT NOT NULL,
	code_challenge TEXT NOT NULL,
	scope          TEXT,
	expires_at     BIGINT NOT NULL,
	created_at     BIGINT NOT NULL
)`,
		// oauth_refresh_tokens backs the refresh_token grant. They are rotated on
		// use (consumed + reissued), so at most one is live per client connection.
		// The access token they mint is, as always, a row in tokens.
		`CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
	token_hash        TEXT PRIMARY KEY,
	client_id         TEXT NOT NULL,
	user_id           TEXT NOT NULL,
	workspace_id      TEXT NOT NULL,
	scope             TEXT,
	access_token_hash TEXT,
	expires_at        BIGINT NOT NULL,
	created_at        BIGINT NOT NULL
)`,
	}},
}

// MigrationState reports how the database's schema compares to the code.
type MigrationState struct {
	Applied []int       // versions recorded as applied, ascending
	Pending []Migration // registered migrations not yet applied, ascending
	Current int         // highest applied version (0 = none / empty database)
	Latest  int         // highest available version
}

// orderedMigrations returns the registered migrations sorted by version.
func orderedMigrations() []Migration {
	out := make([]Migration, len(migrations))
	copy(out, migrations)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

// MigrationStatus reads which migrations have been applied and computes what is
// pending. It is strictly read-only - it never creates the bookkeeping table -
// so it is safe for dry runs and the server's startup check. An absent
// schema_migrations table (a fresh database) is reported as zero applied.
func (s *sqlStore) MigrationStatus() (MigrationState, error) {
	applied := map[int]bool{}
	var st MigrationState

	rows, err := s.query(`SELECT version FROM schema_migrations ORDER BY version`)
	// Always release the rows (and its connection) if one came back, even
	// alongside an error - SQLite runs on a single connection, so a stray open
	// Rows would wedge every later statement.
	if rows != nil {
		defer rows.Close()
	}
	if err == nil {
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				return MigrationState{}, err
			}
			applied[v] = true
			st.Applied = append(st.Applied, v)
			if v > st.Current {
				st.Current = v
			}
		}
		if err := rows.Err(); err != nil {
			return MigrationState{}, err
		}
	}
	// An error here means the table is absent (fresh DB); treat as zero applied.

	for _, m := range orderedMigrations() {
		if m.Version > st.Latest {
			st.Latest = m.Version
		}
		if !applied[m.Version] {
			st.Pending = append(st.Pending, m)
		}
	}
	return st, nil
}

// ApplyMigrations applies every pending migration in version order, each in its
// own transaction, recording it in schema_migrations. This is the single
// schema-writing entry point in the codebase; only `crmkit migrate --execute`
// invokes it. It is a no-op when the schema is already current.
func (s *sqlStore) ApplyMigrations() ([]Migration, error) {
	if _, err := s.exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
	version    BIGINT PRIMARY KEY,
	name       TEXT NOT NULL,
	applied_at BIGINT NOT NULL
)`); err != nil {
		return nil, fmt.Errorf("ensure schema_migrations: %w", err)
	}

	state, err := s.MigrationStatus()
	if err != nil {
		return nil, err
	}

	var done []Migration
	for _, m := range state.Pending {
		tx, err := s.db.Begin()
		if err != nil {
			return done, err
		}
		for _, stmt := range m.Statements {
			if _, err := s.txExec(tx, stmt); err != nil {
				_ = tx.Rollback()
				return done, fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
			}
		}
		if _, err := s.txExec(tx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.Version, m.Name, time.Now().Unix()); err != nil {
			_ = tx.Rollback()
			return done, err
		}
		if err := tx.Commit(); err != nil {
			return done, err
		}
		slog.Info("applied migration", "version", m.Version, "name", m.Name)
		done = append(done, m)
	}
	return done, nil
}
