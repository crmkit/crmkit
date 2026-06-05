package store

import (
	"context"
	"fmt"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// Backend identifiers accepted by Open.
const (
	BackendSQLite   = "sqlite"
	BackendPostgres = "postgres"
)

// Options tunes a backend at open time. The pool settings apply to networked
// backends (Postgres); SQLite ignores them (it is single-connection by design).
// Zero values fall back to sensible defaults.
type Options struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Store is the persistence interface the rest of crmkit depends on. It is
// deliberately backend-agnostic: SQLite is the default (single-file, great for
// local and small deployments), and a more robust backend such as Postgres can
// be added by implementing this interface and wiring it into Open - no changes
// to the server are required. See internal/store/README.md.
type Store interface {
	// lifecycle / config / health
	Close() error
	SetTokenIdleTTL(d time.Duration)
	SetDefaultPlan(name string)
	Ping(ctx context.Context) error

	// plans & quotas
	WorkspacePlan(workspaceID string) (string, error)
	UserPlan(userID string) (string, error)
	CountWorkspacesForUser(userID string) (int, error)
	CountResource(workspaceID, kind string) (int, error)

	// schema migrations. Open never touches schema; MigrationStatus is the
	// read-only check the server uses to refuse startup when out of date, and
	// ApplyMigrations is the single schema-writing entry point (`crmkitd
	// migrate --execute`). See migrate.go.
	MigrationStatus() (MigrationState, error)
	ApplyMigrations() ([]Migration, error)

	// auth: one-time codes, identities, tokens, step-up
	PutOTP(email, codeHash string, expiresAt time.Time) error
	VerifyOTP(email, codeHash string, now time.Time) (bool, error)
	GetOrCreateIdentity(email string) (protocol.User, error)
	CreateToken(userID, workspaceID, name, tokenHash string) (string, error)
	ResolveToken(tokenHash string) (protocol.Session, error)
	ListTokens(workspaceID string) ([]TokenInfo, error)
	ListUserTokens(userID string) ([]TokenInfo, error)
	RevokeToken(workspaceID, tokenID string) error
	RevokeUserToken(userID, tokenID string) error
	PutEscalation(userID, action, target, codeHash string, expiresAt time.Time) error
	VerifyEscalation(userID, action, target, codeHash string, now time.Time) (bool, error)

	// workspaces & membership
	ListWorkspacesForUser(userID string) ([]protocol.Workspace, error)
	CreateWorkspace(userID, name string) (protocol.Workspace, error)
	DeleteWorkspace(workspaceID string) error
	MemberRole(workspaceID, userID string) (string, error)
	SetMemberRole(workspaceID, userID, role string) error
	RemoveMember(workspaceID, userID string) error
	CreateInvite(workspaceID, email, role, invitedBy string) (protocol.Invite, error)
	ListInvites(workspaceID string) ([]protocol.Invite, error)
	ListMembers(workspaceID string) ([]protocol.Member, error)

	// contacts
	CreateContact(ws string, c *protocol.Contact) error
	GetContact(ws, id string) (protocol.Contact, error)
	QueryContacts(ws string, q Query) ([]protocol.Contact, string, error)
	FindContactByEmail(ws, email string) ([]protocol.Contact, error)
	UpdateContact(ws string, c *protocol.Contact) error
	DeleteContact(ws, id string) error

	// companies
	CreateCompany(ws string, c *protocol.Company) error
	GetCompany(ws, id string) (protocol.Company, error)
	QueryCompanies(ws string, q Query) ([]protocol.Company, string, error)
	FindCompanyByDomain(ws, domain string) ([]protocol.Company, error)
	UpdateCompany(ws string, c *protocol.Company) error
	DeleteCompany(ws, id string) error

	// deals
	CreateDeal(ws string, d *protocol.Deal) error
	GetDeal(ws, id string) (protocol.Deal, error)
	QueryDeals(ws string, q Query) ([]protocol.Deal, string, error)
	UpdateDeal(ws string, d *protocol.Deal) error
	DeleteDeal(ws, id string) error

	// reminders (due/overdue follow-ups across contacts and deals)
	ListReminders(ws string, until time.Time, limit int) ([]protocol.Reminder, error)

	// activities & audit
	CreateActivity(ws string, a *protocol.Activity) error
	ListActivities(ws, contactID, dealID string, limit int) ([]protocol.Activity, error)
	WriteAudit(ws, tokenID, action, target, detail string) error
	ListAudit(ws string, limit int) ([]AuditEntry, error)
}

// Compile-time assurance that the SQLite backend satisfies the interface.
var _ Store = (*sqlStore)(nil)

// Open returns a Store for the given backend. dsn is backend-specific: for
// "sqlite" it is a file path (or ":memory:"); for "postgres" it is a connection
// URL (e.g. "postgres://user:pass@host:5432/crmkit?sslmode=require"). opts tunes
// the connection pool (Postgres only).
//
// Open only connects - it never creates or alters schema. Callers that need a
// ready database must run ApplyMigrations (the server instead checks
// MigrationStatus and refuses to start when migrations are pending).
func Open(backend, dsn string, opts Options) (Store, error) {
	switch backend {
	case "", BackendSQLite:
		return openSQLite(dsn)
	case BackendPostgres:
		return openPostgres(dsn, opts)
	default:
		return nil, fmt.Errorf("unknown storage backend %q (use %q or %q)", backend, BackendSQLite, BackendPostgres)
	}
}
