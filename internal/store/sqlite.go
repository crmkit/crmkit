// Package store is the crmkit persistence layer. The rest of crmkit depends on
// the Store interface; backends are selected at startup (see store.go). Two
// pure-Go backends ship: SQLite (modernc.org/sqlite - a single file, static
// CGO_ENABLED=0 build) and Postgres (pgx). The query code is shared and made
// portable by the dialect (see dialect.go). Every CRM query is scoped to a
// workspace for tenant isolation.
package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crmkit/crmkit/internal/protocol"
)

// ErrNotFound is returned when a lookup matches no row in the workspace.
var ErrNotFound = errors.New("not found")

// ErrTokenExpired is returned when a token is valid but has exceeded its sliding
// inactivity window.
var ErrTokenExpired = errors.New("token expired")

// defaultTokenIdleTTL is the fallback sliding inactivity window when none is set.
const defaultTokenIdleTTL = 30 * 24 * time.Hour

// sqlStore is the shared database/sql implementation of Store. The same query
// code serves both SQLite and Postgres; the dialect handles the differences.
type sqlStore struct {
	db           *sql.DB
	d            dialect
	tokenIdleTTL time.Duration
	defaultPlan  string
}

// SetTokenIdleTTL sets the sliding inactivity window after which an access token
// expires. A value <= 0 disables expiry (tokens live until revoked).
func (s *sqlStore) SetTokenIdleTTL(d time.Duration) {
	s.tokenIdleTTL = d
}

// SetDefaultPlan sets the plan name assigned to newly created users and
// workspaces (from config.Plans.Default). Empty falls back to "basic".
func (s *sqlStore) SetDefaultPlan(name string) {
	s.defaultPlan = name
}

// planOrDefault is the plan to stamp on new rows.
func (s *sqlStore) planOrDefault() string {
	if s.defaultPlan == "" {
		return "basic"
	}
	return s.defaultPlan
}

// openSQLite opens (creating the file if needed) the SQLite database at path.
// The special path ":memory:" yields an ephemeral database. It does NOT touch
// the schema - schema creation/migration happens only via ApplyMigrations
// (i.e. `crmkitd migrate --execute`); the server opens read-only and refuses to
// start if the schema is not current.
func openSQLite(path string) (*sqlStore, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("create db directory: %w", err)
			}
		}
	}

	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	// SQLite handles one writer at a time; keep a single connection to avoid
	// "database is locked" under concurrent requests.
	db.SetMaxOpenConns(1)

	return &sqlStore{db: db, d: sqliteDialect, tokenIdleTTL: defaultTokenIdleTTL}, nil
}

// Close releases the database handle.
func (s *sqlStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// baselineSchema is migration 1: the full schema as individual statements (so
// it works on drivers that reject multi-statement Exec, like Postgres/pgx).
// Column types are valid on both backends: TEXT works everywhere and BIGINT has
// INTEGER affinity in SQLite while giving Postgres a 64-bit integer for
// epochs/amounts. See migrate.go - this is applied only by ApplyMigrations.
var baselineSchema = []string{
	`CREATE TABLE IF NOT EXISTS workspaces (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	created_by TEXT,
	created_at BIGINT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS users (
	id                   TEXT PRIMARY KEY,
	email                TEXT NOT NULL UNIQUE,
	default_workspace_id TEXT,
	created_at           BIGINT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS memberships (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	user_id      TEXT NOT NULL REFERENCES users(id),
	role         TEXT NOT NULL DEFAULT 'member',
	created_at   BIGINT NOT NULL,
	PRIMARY KEY (workspace_id, user_id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_memberships_user ON memberships(user_id)`,
	`CREATE TABLE IF NOT EXISTS invites (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	email        TEXT NOT NULL,
	role         TEXT NOT NULL DEFAULT 'member',
	invited_by   TEXT,
	created_at   BIGINT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_invites_email ON invites(email)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_invites_ws_email ON invites(workspace_id, email)`,
	`CREATE TABLE IF NOT EXISTS otps (
	email      TEXT PRIMARY KEY,
	code_hash  TEXT NOT NULL,
	expires_at BIGINT NOT NULL,
	attempts   BIGINT NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS escalations (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL,
	action     TEXT NOT NULL,
	target     TEXT NOT NULL,
	code_hash  TEXT NOT NULL,
	expires_at BIGINT NOT NULL,
	attempts   BIGINT NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL
)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_escalations_scope ON escalations(user_id, action, target)`,
	`CREATE TABLE IF NOT EXISTS tokens (
	id           TEXT PRIMARY KEY,
	token_hash   TEXT NOT NULL UNIQUE,
	user_id      TEXT NOT NULL REFERENCES users(id),
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	created_at   BIGINT NOT NULL,
	last_used_at BIGINT,
	revoked_at   BIGINT
)`,
	`CREATE TABLE IF NOT EXISTS contacts (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	email        TEXT,
	phone        TEXT,
	company_id   TEXT,
	owner        TEXT,
	stage        TEXT,
	tags         TEXT,
	notes          TEXT,
	custom         TEXT,
	follow_up_at   BIGINT,
	follow_up_note TEXT,
	created_at     BIGINT NOT NULL,
	updated_at     BIGINT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_contacts_ws ON contacts(workspace_id)`,
	`CREATE INDEX IF NOT EXISTS idx_contacts_followup ON contacts(workspace_id, follow_up_at)`,
	`CREATE TABLE IF NOT EXISTS companies (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	domain       TEXT,
	custom       TEXT,
	created_at   BIGINT NOT NULL,
	updated_at   BIGINT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_companies_ws ON companies(workspace_id)`,
	`CREATE TABLE IF NOT EXISTS deals (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	title        TEXT NOT NULL,
	contact_id   TEXT,
	company_id   TEXT,
	amount_cents BIGINT,
	currency     TEXT,
	stage          TEXT,
	status         TEXT,
	custom         TEXT,
	follow_up_at   BIGINT,
	follow_up_note TEXT,
	created_at     BIGINT NOT NULL,
	updated_at     BIGINT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_deals_ws ON deals(workspace_id)`,
	`CREATE INDEX IF NOT EXISTS idx_deals_followup ON deals(workspace_id, follow_up_at)`,
	`CREATE TABLE IF NOT EXISTS activities (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	contact_id   TEXT,
	deal_id      TEXT,
	kind         TEXT NOT NULL,
	body         TEXT NOT NULL,
	created_by   TEXT,
	created_at   BIGINT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_activities_ws ON activities(workspace_id)`,
	`CREATE INDEX IF NOT EXISTS idx_activities_contact ON activities(contact_id)`,
	`CREATE TABLE IF NOT EXISTS audit_log (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	token_id     TEXT,
	action       TEXT NOT NULL,
	target       TEXT,
	detail       TEXT,
	created_at   BIGINT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_ws ON audit_log(workspace_id)`,
}

// ---- helpers -------------------------------------------------------------

func unix(t time.Time) int64 { return t.Unix() }

func fromUnix(v int64) time.Time { return time.Unix(v, 0).UTC() }

// nullableUnix converts an optional time to a value suitable for a nullable
// BIGINT column (nil when unset).
func nullableUnix(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.Unix()
}

// fromNullableUnix converts a nullable BIGINT back to an optional time.
func fromNullableUnix(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := fromUnix(n.Int64)
	return &t
}

func marshalJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func marshalTags(tags []string) (string, error) {
	if len(tags) == 0 {
		return "", nil
	}
	return marshalJSON(tags)
}

func unmarshalTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func unmarshalCustom(s string) map[string]any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// ---- identity / auth -----------------------------------------------------

// PutOTP stores (replacing any existing) the pending login code for an email.
func (s *sqlStore) PutOTP(email, codeHash string, expiresAt time.Time) error {
	now := time.Now()
	_, err := s.exec(`
INSERT INTO otps (email, code_hash, expires_at, attempts, created_at)
VALUES (?, ?, ?, 0, ?)
ON CONFLICT(email) DO UPDATE SET code_hash=excluded.code_hash, expires_at=excluded.expires_at, attempts=0, created_at=excluded.created_at`,
		email, codeHash, unix(expiresAt), unix(now))
	return err
}

// VerifyOTP checks the supplied code hash for email. It returns ok=true and
// deletes the code on success. Each failed attempt is counted; after 5 the
// code is invalidated.
func (s *sqlStore) VerifyOTP(email, codeHash string, now time.Time) (bool, error) {
	var (
		storedHash string
		expiresAt  int64
		attempts   int
	)
	err := s.queryRow(`SELECT code_hash, expires_at, attempts FROM otps WHERE email = ?`, email).
		Scan(&storedHash, &expiresAt, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if now.Unix() > expiresAt || attempts >= 5 {
		_, _ = s.exec(`DELETE FROM otps WHERE email = ?`, email)
		return false, nil
	}

	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(codeHash)) != 1 {
		_, _ = s.exec(`UPDATE otps SET attempts = attempts + 1 WHERE email = ?`, email)
		return false, nil
	}

	_, _ = s.exec(`DELETE FROM otps WHERE email = ?`, email)
	return true, nil
}

// GetOrCreateIdentity returns the user for an email, provisioning the user, a
// default workspace, and an admin membership on first sight. On every call it
// also consumes any pending invites for the email into memberships.
func (s *sqlStore) GetOrCreateIdentity(email string) (protocol.User, error) {
	var (
		u         protocol.User
		createdAt int64
		defWs     sql.NullString
	)
	err := s.queryRow(`SELECT id, email, default_workspace_id, created_at FROM users WHERE email = ?`, email).
		Scan(&u.ID, &u.Email, &defWs, &createdAt)
	if err == nil {
		u.CreatedAt = fromUnix(createdAt)
		u.DefaultWorkspaceID = defWs.String
		if err := s.acceptInvites(u.ID, email); err != nil {
			return protocol.User{}, err
		}
		// Self-heal: if the user's default workspace was deleted, give them a
		// fresh personal one so login can always mint a usable token.
		if u.DefaultWorkspaceID == "" {
			ws, err := s.CreateWorkspace(u.ID, defaultWorkspaceName(email))
			if err != nil {
				return protocol.User{}, err
			}
			if _, err := s.exec(`UPDATE users SET default_workspace_id = ? WHERE id = ?`, ws.ID, u.ID); err != nil {
				return protocol.User{}, err
			}
			u.DefaultWorkspaceID = ws.ID
		}
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.User{}, err
	}

	now := time.Now()
	ws := protocol.Workspace{ID: protocol.NewID("ws"), Name: defaultWorkspaceName(email), CreatedAt: now}
	u = protocol.User{ID: protocol.NewID("u"), Email: email, DefaultWorkspaceID: ws.ID, CreatedAt: now}

	tx, err := s.db.Begin()
	if err != nil {
		return protocol.User{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := s.txExec(tx, `INSERT INTO workspaces (id, name, created_by, created_at, plan) VALUES (?, ?, ?, ?, ?)`,
		ws.ID, ws.Name, u.ID, unix(now), s.planOrDefault()); err != nil {
		return protocol.User{}, err
	}
	if _, err := s.txExec(tx, `INSERT INTO users (id, email, default_workspace_id, created_at, plan) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.DefaultWorkspaceID, unix(now), s.planOrDefault()); err != nil {
		return protocol.User{}, err
	}
	if _, err := s.txExec(tx, `INSERT INTO memberships (workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
		ws.ID, u.ID, protocol.RoleAdmin, unix(now)); err != nil {
		return protocol.User{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.User{}, err
	}

	if err := s.acceptInvites(u.ID, email); err != nil {
		return protocol.User{}, err
	}
	return u, nil
}

// acceptInvites converts any pending invites for email into memberships, then
// clears them. Rows are read fully before any write because the store runs on a
// single connection.
func (s *sqlStore) acceptInvites(userID, email string) error {
	rows, err := s.query(`SELECT workspace_id, role FROM invites WHERE email = ?`, email)
	if err != nil {
		return err
	}
	type pending struct{ workspaceID, role string }
	var invites []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.workspaceID, &p.role); err != nil {
			rows.Close()
			return err
		}
		invites = append(invites, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(invites) == 0 {
		return nil
	}

	now := unix(time.Now())
	for _, p := range invites {
		if _, err := s.exec(`INSERT INTO memberships (workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?) ON CONFLICT (workspace_id, user_id) DO NOTHING`,
			p.workspaceID, userID, p.role, now); err != nil {
			return err
		}
	}
	_, err = s.exec(`DELETE FROM invites WHERE email = ?`, email)
	return err
}

func defaultWorkspaceName(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[i+1:]
	}
	return email
}

// CreateToken mints a token row. The caller supplies the SHA-256 hash of the
// opaque token string; the plaintext is never stored.
func (s *sqlStore) CreateToken(userID, workspaceID, name, tokenHash string) (string, error) {
	id := protocol.NewID("tok")
	_, err := s.exec(`
INSERT INTO tokens (id, token_hash, user_id, workspace_id, name, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, id, tokenHash, userID, workspaceID, name, unix(time.Now()))
	if err != nil {
		return "", err
	}
	return id, nil
}

// ResolveToken returns the session behind a token hash, updating last_used_at.
// Revoked or unknown tokens yield ErrNotFound.
func (s *sqlStore) ResolveToken(tokenHash string) (protocol.Session, error) {
	var (
		sess      protocol.Session
		revoked   sql.NullInt64
		createdAt int64
		lastUsed  sql.NullInt64
	)
	// The membership join means a token stops resolving the moment its user is
	// removed from the workspace - revocation without touching the token rows.
	err := s.queryRow(`
SELECT t.id, t.name, t.user_id, t.workspace_id, u.email, t.revoked_at, t.created_at, t.last_used_at
FROM tokens t
JOIN users u ON u.id = t.user_id
JOIN memberships m ON m.user_id = t.user_id AND m.workspace_id = t.workspace_id
WHERE t.token_hash = ?`, tokenHash).
		Scan(&sess.TokenID, &sess.TokenName, &sess.UserID, &sess.WorkspaceID, &sess.Email, &revoked, &createdAt, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Session{}, ErrNotFound
	}
	if err != nil {
		return protocol.Session{}, err
	}
	if revoked.Valid {
		return protocol.Session{}, ErrNotFound
	}

	// Sliding-window expiry: a token dies if it has gone unused longer than the
	// idle TTL. Each successful resolve below slides the window forward.
	if s.tokenIdleTTL > 0 {
		lastActive := createdAt
		if lastUsed.Valid {
			lastActive = lastUsed.Int64
		}
		if time.Since(fromUnix(lastActive)) > s.tokenIdleTTL {
			return protocol.Session{}, ErrTokenExpired
		}
	}

	_, _ = s.exec(`UPDATE tokens SET last_used_at = ? WHERE id = ?`, unix(time.Now()), sess.TokenID)
	return sess, nil
}

// TokenInfo summarizes a token for listing (no secret material).
type TokenInfo struct {
	ID          string
	WorkspaceID string
	Name        string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
}

func scanTokens(rows *sql.Rows) ([]TokenInfo, error) {
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var (
			ti        TokenInfo
			createdAt int64
			lastUsed  sql.NullInt64
		)
		if err := rows.Scan(&ti.ID, &ti.WorkspaceID, &ti.Name, &createdAt, &lastUsed); err != nil {
			return nil, err
		}
		ti.CreatedAt = fromUnix(createdAt)
		if lastUsed.Valid {
			t := fromUnix(lastUsed.Int64)
			ti.LastUsedAt = &t
		}
		out = append(out, ti)
	}
	return out, rows.Err()
}

// ListTokens returns the active (non-revoked) tokens for a workspace.
func (s *sqlStore) ListTokens(workspaceID string) ([]TokenInfo, error) {
	rows, err := s.query(`
SELECT id, workspace_id, name, created_at, last_used_at FROM tokens
WHERE workspace_id = ? AND revoked_at IS NULL ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	return scanTokens(rows)
}

// ListUserTokens returns a user's active tokens across all their workspaces, so
// they can review and revoke their own sessions.
func (s *sqlStore) ListUserTokens(userID string) ([]TokenInfo, error) {
	rows, err := s.query(`
SELECT id, workspace_id, name, created_at, last_used_at FROM tokens
WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return scanTokens(rows)
}

// RevokeToken marks a token revoked within a workspace.
func (s *sqlStore) RevokeToken(workspaceID, tokenID string) error {
	res, err := s.exec(`UPDATE tokens SET revoked_at = ? WHERE id = ? AND workspace_id = ? AND revoked_at IS NULL`,
		unix(time.Now()), tokenID, workspaceID)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

// RevokeUserToken revokes a token only if it belongs to the given user - the
// self-service "log out this session" operation.
func (s *sqlStore) RevokeUserToken(userID, tokenID string) error {
	res, err := s.exec(`UPDATE tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		unix(time.Now()), tokenID, userID)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

// Ping checks backend connectivity (used by the readiness probe).
func (s *sqlStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}
