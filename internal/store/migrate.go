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
	// v1 (2026-06-05): baseline - the full schema as of the first release.
	{Version: 1, Name: "initial schema", Statements: baselineSchema},
	// v2 (2026-06-05): per-tenant plan assignment. 'basic' matches config.DefaultPlan; the
	// app sets the plan explicitly on insert, this default just backfills any
	// existing rows. ADD COLUMN with a constant default works on both backends.
	{Version: 2, Name: "plan columns", Statements: []string{
		`ALTER TABLE workspaces ADD COLUMN plan TEXT NOT NULL DEFAULT 'basic'`,
		`ALTER TABLE users ADD COLUMN plan TEXT NOT NULL DEFAULT 'basic'`,
	}},
	// v3 (2026-06-05): MCP OAuth 2.1 authorization server. oauth_clients holds dynamically
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
	// v4 (2026-06-06): attribute audit entries to the acting member. token_id alone is opaque
	// and dangles when a token is revoked; actor_email is the member's identity at
	// the time of the action (point-in-time, denormalized, durable). Existing rows
	// keep a NULL actor_email and render with no `by=`.
	{Version: 4, Name: "audit actor", Statements: []string{
		`ALTER TABLE audit_log ADD COLUMN actor_email TEXT`,
	}},
	// v5 (2026-06-06): durable per-record provenance. created_by is the member
	// (human or agent) who created the row, stamped once at insert and never
	// changed - the audit feed is bounded/recent, so the record itself is the
	// permanent home for "who made this". Filterable, to organise records by the
	// agent that created them. Existing rows keep a NULL created_by.
	{Version: 5, Name: "record creator", Statements: []string{
		`ALTER TABLE contacts ADD COLUMN created_by TEXT`,
		`ALTER TABLE companies ADD COLUMN created_by TEXT`,
		`ALTER TABLE deals ADD COLUMN created_by TEXT`,
	}},
	// v6 (2026-06-06): per-workspace display timezone. Instants are still stored
	// in UTC (unix seconds); this only controls how reads are formatted for
	// humans. An IANA name like 'America/Los_Angeles'; defaults to (and backfills
	// as) 'UTC'.
	{Version: 6, Name: "workspace timezone", Statements: []string{
		`ALTER TABLE workspaces ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC'`,
	}},
	// v7 (2026-06-07): tags on companies (contacts already have them), so records
	// can be grouped - e.g. "competitor", "watchlist", "portfolio". Stored as a
	// JSON array string, mirroring contact tags; the query layer filters by
	// membership. Existing rows keep a NULL (empty) tag set.
	{Version: 7, Name: "company tags", Statements: []string{
		`ALTER TABLE companies ADD COLUMN tags TEXT`,
	}},
	// v8 (2026-06-07): richer company records. notes is a first-class, searchable
	// long-text field (companies previously only had name/domain). activities gain
	// company_id so interactions/observations can be logged against a company - a
	// monitoring timeline, not just contacts and deals. Existing rows keep NULLs.
	{Version: 8, Name: "company notes and activities", Statements: []string{
		`ALTER TABLE companies ADD COLUMN notes TEXT`,
		`ALTER TABLE activities ADD COLUMN company_id TEXT`,
	}},
	// v9 (2026-06-07): index the audit log by time so the retention prune
	// (DELETE WHERE created_at < cutoff) is cheap. The audit log is a security
	// log, bounded by age rather than count.
	{Version: 9, Name: "audit created_at index", Statements: []string{
		`CREATE INDEX IF NOT EXISTS idx_audit_log_created_at ON audit_log(created_at)`,
	}},
	// v10 (2026-06-07): short, workspace-scoped public handles. Each record keeps
	// its opaque global id (PK, FK target) and gains a short `handle` that agents
	// address it by - fewer tokens, easier to reference. The handle is stored
	// bare; presentation (any prefix) is applied at the API edge. Uniqueness is
	// per (workspace, kind) via the unique index, with retry-on-collision at
	// insert. Existing rows are backfilled handle=id (valid, just long) before the
	// index is built; new rows get a generated short handle.
	{Version: 10, Name: "record handles", Statements: []string{
		`ALTER TABLE contacts ADD COLUMN handle TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE companies ADD COLUMN handle TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE deals ADD COLUMN handle TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE activities ADD COLUMN handle TEXT NOT NULL DEFAULT ''`,
		`UPDATE contacts SET handle = id WHERE handle = ''`,
		`UPDATE companies SET handle = id WHERE handle = ''`,
		`UPDATE deals SET handle = id WHERE handle = ''`,
		`UPDATE activities SET handle = id WHERE handle = ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_ws_handle ON contacts(workspace_id, handle)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_companies_ws_handle ON companies(workspace_id, handle)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_deals_ws_handle ON deals(workspace_id, handle)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_activities_ws_handle ON activities(workspace_id, handle)`,
	}},
	// v11 (2026-06-07): optimistic concurrency. Each updatable record carries a
	// monotonic version that increments on every write; a conditional update
	// (If-Match) only succeeds when the caller's expected version still matches,
	// so two agents editing the same record can't silently clobber each other.
	// Activities are append-only (no update), so they need no version. Existing
	// rows start at 1.
	{Version: 11, Name: "record version", Statements: []string{
		`ALTER TABLE contacts ADD COLUMN version BIGINT NOT NULL DEFAULT 1`,
		`ALTER TABLE companies ADD COLUMN version BIGINT NOT NULL DEFAULT 1`,
		`ALTER TABLE deals ADD COLUMN version BIGINT NOT NULL DEFAULT 1`,
	}},
	// v12 (2026-06-08): support tickets. A ticket is a first-class record (its own
	// entity, not a repurposed deal): a customer request with a status and an
	// assignee. requester_id references a contact (the customer); assignee is a
	// member email, mirroring `owner`. The opening message is `content`; the
	// conversation/activity layer and the rest of the lifecycle come later.
	{Version: 12, Name: "tickets", Statements: []string{
		`CREATE TABLE IF NOT EXISTS tickets (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	handle       TEXT NOT NULL DEFAULT '',
	version      BIGINT NOT NULL DEFAULT 1,
	subject      TEXT NOT NULL,
	content      TEXT,
	status       TEXT NOT NULL DEFAULT 'open',
	requester_id TEXT,
	assignee     TEXT,
	tags         TEXT,
	custom       TEXT,
	created_at   BIGINT NOT NULL,
	updated_at   BIGINT NOT NULL,
	created_by   TEXT
)`,
		`CREATE INDEX IF NOT EXISTS idx_tickets_ws ON tickets(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tickets_status ON tickets(workspace_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_tickets_requester ON tickets(workspace_id, requester_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_tickets_ws_handle ON tickets(workspace_id, handle)`,
	}},
	// v13 (2026-06-08): tickets get a conversation. activities can attach to a
	// ticket (mirroring contact_id/deal_id/company_id), so a ticket has a timeline
	// of notes/replies. Existing rows keep a NULL ticket_id.
	{Version: 13, Name: "ticket activities", Statements: []string{
		`ALTER TABLE activities ADD COLUMN ticket_id TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_activities_ticket ON activities(workspace_id, ticket_id)`,
	}},
	// v14 (2026-06-08): tickets get a follow-up timer, mirroring contacts/deals, so
	// a ticket (e.g. one waiting on the customer) resurfaces via GET /reminders
	// when it's due. follow_up_at is the generic "next action due"; SLA timers
	// come later.
	{Version: 14, Name: "ticket follow-up", Statements: []string{
		`ALTER TABLE tickets ADD COLUMN follow_up_at BIGINT`,
		`ALTER TABLE tickets ADD COLUMN follow_up_note TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_tickets_followup ON tickets(workspace_id, follow_up_at)`,
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
