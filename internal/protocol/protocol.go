// Package protocol defines the core CRM domain types shared between the
// storage layer, the HTTP server, and the rendering layer. Every entity carries
// an opaque global id (the durable PK/FK key) and a short, workspace-scoped
// handle (e.g. "contact_k7m2q") that agents thread through follow-up calls. See
// NewHandle / FormatRef / ParseRef.
package protocol

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
	"time"
)

// Entity kinds and their handle prefixes.
const (
	KindContact  = "contact"
	KindCompany  = "company"
	KindDeal     = "deal"
	KindActivity = "activity"
	KindTicket   = "ticket"
	KindCampaign = "campaign"
)

// idAlphabet is a lowercase, unambiguous base32 alphabet used for IDs.
var idEncoding = base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding)

// NewID returns a new random identifier with the given short prefix, e.g.
// NewID("c") -> "c_w7x4k2...". Prefixes keep IDs self-describing in logs.
func NewID(prefix string) string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand should never fail; fall back to a time-seeded value.
		t := time.Now().UnixNano()
		for i := range buf {
			buf[i] = byte(t >> (uint(i) * 8))
		}
	}
	return prefix + "_" + idEncoding.EncodeToString(buf)
}

// Handle returns the "kind/id" reference token for an entity. It is used for
// internal, durable references (e.g. audit-log targets) that point at the
// opaque global id. Agent-facing references use FormatRef over the short handle.
func Handle(kind, id string) string {
	return kind + "/" + id
}

// SplitHandle splits a "kind/id" handle. If the value has no slash it is
// treated as a bare id with an empty kind.
func SplitHandle(handle string) (kind, id string) {
	if i := strings.IndexByte(handle, '/'); i >= 0 {
		return handle[:i], handle[i+1:]
	}
	return "", handle
}

// handleLen is the length of a generated handle body. Handles are scoped to a
// (workspace, kind), so this small space is ample - collisions are rare and
// resolved by retry on insert. Shorter = fewer tokens for the agent.
const handleLen = 5

// NewHandle returns a short, random reference body (no prefix, e.g. "k7m2q").
// This is the public id an agent sees and threads through calls; it is stored
// bare. Uniqueness is per (workspace, kind), enforced by a unique index plus
// retry-on-collision at insert. The wire representation (any prefix/separator)
// is applied at the edge by FormatRef and is never stored.
func NewHandle() string {
	// 256 is a multiple of 32, so indexing the alphabet by a random byte is bias-free.
	buf := make([]byte, handleLen)
	if _, err := rand.Read(buf); err != nil {
		t := time.Now().UnixNano()
		for i := range buf {
			buf[i] = byte(t >> (uint(i) * 8))
		}
	}
	const alpha = "abcdefghijkmnpqrstuvwxyz23456789"
	for i := range buf {
		buf[i] = alpha[int(buf[i])%len(alpha)]
	}
	return string(buf)
}

// FormatRef renders a stored bare handle as the agent-facing reference, e.g.
// FormatRef("contact", "k7m2q") -> "contact_k7m2q". This is the single place the
// wire representation is decided; change it (to "contact/", bare, etc.) without
// touching storage. Empty handle yields "" so the field is omitted.
func FormatRef(kind, handle string) string {
	if handle == "" {
		return ""
	}
	return kind + "_" + handle
}

// ParseRef normalizes any reference an agent sends back to the bare handle. It
// accepts every representation (contact_k7m2q, contact/k7m2q, bare k7m2q): the
// handle body contains no separator, so it is the suffix after the last '/' or
// '_'. An opaque internal id (e.g. "c_3f9a…") also contains '_', so callers that
// must accept either match the bare result against handle AND the raw input
// against id (see store.ResolveHandle).
func ParseRef(input string) string {
	if i := strings.LastIndexAny(input, "/_"); i >= 0 {
		return input[i+1:]
	}
	return input
}

// Workspace is a tenant. The first successful login for an email address
// provisions a workspace and its owning user.
type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Timezone is the IANA name (e.g. "America/Los_Angeles") used to format times
	// in this workspace's reads. Instants are always stored in UTC; this is a
	// display setting only. Defaults to "UTC".
	Timezone string `json:"timezone,omitempty"`
	// Role is the requesting user's role in this workspace. Populated only when
	// a workspace is returned in the context of a specific user (e.g. listing).
	Role string `json:"role,omitempty"`
}

// Roles a user can hold within a workspace.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// User is an identity, addressed by email. A user belongs to one or more
// workspaces via memberships; DefaultWorkspaceID is the workspace a fresh login
// mints a token for.
type User struct {
	ID                 string    `json:"id"`
	Email              string    `json:"email"`
	DefaultWorkspaceID string    `json:"default_workspace_id"`
	CreatedAt          time.Time `json:"created_at"`
}

// Member is a user's presence in a workspace.
type Member struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// Invite is a pending grant of workspace access to an email address. It is
// consumed into a Membership the next time that email authenticates.
type Invite struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	InvitedBy   string    `json:"invited_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Session is the resolved identity behind a bearer token.
type Session struct {
	TokenID     string `json:"token_id"`
	TokenName   string `json:"token_name"`
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	WorkspaceID string `json:"workspace_id"`
	// WorkspaceTimezone is the workspace's display timezone (IANA name), resolved
	// when the token is, so reads can format times in it without another lookup.
	WorkspaceTimezone string `json:"workspace_timezone,omitempty"`
}

// Contact is a person in the CRM.
type Contact struct {
	ID string `json:"id"`
	// Handle is the short, workspace-scoped public reference (bare, e.g. "k7m2q").
	// Agents address the record by this; ID is the durable opaque global key.
	Handle string `json:"handle,omitempty"`
	// Version increments on every update. Read it, then send it back as If-Match
	// (header) or "version" (body) on a PATCH to make the write conditional - the
	// update is rejected (412) if the record changed in the meantime.
	Version   int64  `json:"version,omitempty"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	Phone     string `json:"phone,omitempty"`
	CompanyID string `json:"company_id,omitempty"`
	// CompanyName / CompanyHandle are the resolved name and public handle of
	// CompanyID, populated on read for display (never persisted).
	CompanyName   string         `json:"company_name,omitempty"`
	CompanyHandle string         `json:"company_handle,omitempty"`
	Owner         string         `json:"owner,omitempty"`
	Stage         string         `json:"stage,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	Notes         string         `json:"notes,omitempty"`
	Custom        map[string]any `json:"custom,omitempty"`
	// FollowUpAt is when this contact should next be followed up (RFC3339).
	// Send null to clear it. Agents read due/overdue items via GET /reminders.
	FollowUpAt   *time.Time `json:"follow_up_at,omitempty"`
	FollowUpNote string     `json:"follow_up_note,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// CreatedBy is the member (human or agent) who created this record, stamped
	// once at creation. Persisted; never changes.
	CreatedBy string `json:"created_by,omitempty"`
	// ActivityCount / LastActivityAt summarise the contact's activity log. They
	// are populated on a single-record fetch for display (never persisted), so an
	// agent sees there's history worth pulling without a blind second call.
	ActivityCount  int        `json:"activity_count,omitempty"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
}

// Company is an organization that contacts and deals belong to.
type Company struct {
	ID        string         `json:"id"`
	Handle    string         `json:"handle,omitempty"`  // short public reference; see Contact.Handle
	Version   int64          `json:"version,omitempty"` // optimistic-concurrency token; see Contact.Version
	Name      string         `json:"name"`
	Domain    string         `json:"domain,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	Notes     string         `json:"notes,omitempty"`
	Custom    map[string]any `json:"custom,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	// CreatedBy is the member (human or agent) who created this record.
	CreatedBy string `json:"created_by,omitempty"`
	// ActivityCount / LastActivityAt summarise the company's activity log,
	// populated on a single-record fetch for display (never persisted).
	ActivityCount  int        `json:"activity_count,omitempty"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
}

// Deal is an opportunity moving through a pipeline.
type Deal struct {
	ID        string `json:"id"`
	Handle    string `json:"handle,omitempty"`  // short public reference; see Contact.Handle
	Version   int64  `json:"version,omitempty"` // optimistic-concurrency token; see Contact.Version
	Title     string `json:"title"`
	ContactID string `json:"contact_id,omitempty"`
	CompanyID string `json:"company_id,omitempty"`
	// ContactName/CompanyName and ContactHandle/CompanyHandle are the resolved
	// names and public handles of ContactID/CompanyID, populated on read (never
	// persisted).
	ContactName   string         `json:"contact_name,omitempty"`
	CompanyName   string         `json:"company_name,omitempty"`
	ContactHandle string         `json:"contact_handle,omitempty"`
	CompanyHandle string         `json:"company_handle,omitempty"`
	AmountCents   int64          `json:"amount_cents,omitempty"`
	Currency      string         `json:"currency,omitempty"`
	Stage         string         `json:"stage,omitempty"`
	Status        string         `json:"status,omitempty"` // open | won | lost
	Custom        map[string]any `json:"custom,omitempty"`
	// FollowUpAt is when this deal should next be advanced (RFC3339). Send null
	// to clear it. Agents read due/overdue items via GET /reminders.
	FollowUpAt   *time.Time `json:"follow_up_at,omitempty"`
	FollowUpNote string     `json:"follow_up_note,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// CreatedBy is the member (human or agent) who created this record, stamped
	// once at creation. Persisted; never changes.
	CreatedBy string `json:"created_by,omitempty"`
	// ActivityCount / LastActivityAt summarise the deal's activity log, populated
	// on a single-record fetch for display (never persisted).
	ActivityCount  int        `json:"activity_count,omitempty"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
}

// Ticket is a support case: something a customer needs help with, with a status
// and an assignee. The opening message is `content`; replies/notes (the
// conversation) are a later addition.
type Ticket struct {
	ID      string `json:"id"`
	Handle  string `json:"handle,omitempty"`  // short public reference; see Contact.Handle
	Version int64  `json:"version,omitempty"` // optimistic-concurrency token; see Contact.Version
	Subject string `json:"subject"`
	Content string `json:"content,omitempty"` // the opening message / body
	// Status is the workflow state: open | pending | solved. (on-hold, closed and
	// the full lifecycle come later.)
	Status string `json:"status,omitempty"`
	// RequesterID is the contact this ticket is for (the customer). RequesterName /
	// RequesterHandle are resolved on read for display (never persisted).
	RequesterID     string `json:"requester_id,omitempty"`
	RequesterName   string `json:"requester_name,omitempty"`
	RequesterHandle string `json:"requester_handle,omitempty"`
	// Assignee is the member (human or agent) handling the ticket - an email,
	// mirroring `owner` on other entities.
	Assignee string         `json:"assignee,omitempty"`
	Tags     []string       `json:"tags,omitempty"`
	Custom   map[string]any `json:"custom,omitempty"`
	// FollowUpAt is when this ticket should next be acted on (e.g. nudge the
	// customer). Send null to clear it. Due/overdue tickets surface via GET
	// /reminders.
	FollowUpAt   *time.Time `json:"follow_up_at,omitempty"`
	FollowUpNote string     `json:"follow_up_note,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// CreatedBy is the member who opened the ticket, stamped once. Persisted.
	CreatedBy string `json:"created_by,omitempty"`
	// ActivityCount / LastActivityAt summarise the ticket's conversation,
	// populated on a single-record fetch for display (never persisted).
	ActivityCount  int        `json:"activity_count,omitempty"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
}

// Campaign is a prospecting effort: a free-text brief plus the contacts and
// companies gathered under it. Membership is many-to-many (an entity can belong
// to several campaigns) and deduped per campaign, so an agent re-finding the
// same contact never double-counts. It is an anchor for agent work, not an
// outreach channel - an email/outreach object attaches to a campaign later.
type Campaign struct {
	ID          string `json:"id"`
	Handle      string `json:"handle,omitempty"`  // short public reference; see Contact.Handle
	Version     int64  `json:"version,omitempty"` // optimistic-concurrency token; see Contact.Version
	Name        string `json:"name"`
	Description string `json:"description,omitempty"` // the free-text brief: what this campaign is for
	// Status is the lifecycle state: active | paused | done.
	Status    string         `json:"status,omitempty"`
	Custom    map[string]any `json:"custom,omitempty"` // free-form extension fields, like every other entity
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// CreatedBy is the member (human or agent) who created this campaign, stamped
	// once at creation. Persisted; never changes.
	CreatedBy string `json:"created_by,omitempty"`
	// MemberCounts summarises membership by kind (e.g. {"contact":47,"company":12}),
	// populated on a single-record fetch for display (never persisted). The contact
	// count is what a "fill N contacts" objective measures.
	MemberCounts map[string]int `json:"member_counts,omitempty"`
}

// CampaignMember is one entity (a contact or company) gathered under a campaign,
// carrying the provenance of why/when/by-whom it was attached. Name and Handle
// are resolved on read for display (never persisted).
type CampaignMember struct {
	Kind    string    `json:"kind"` // contact | company
	ID      string    `json:"id"`   // internal id of the attached entity
	Handle  string    `json:"handle,omitempty"`
	Name    string    `json:"name,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	AddedBy string    `json:"added_by,omitempty"`
	AddedAt time.Time `json:"added_at"`
}

// Reminder is a due/overdue follow-up surfaced by GET /reminders - a contact,
// deal, or ticket whose follow_up_at has arrived. Agents pull these instead of
// the server pushing notifications.
type Reminder struct {
	Handle     string    `json:"handle"` // e.g. contact_k7m2q / deal_… / ticket_…
	Kind       string    `json:"kind"`   // contact | deal | ticket
	Title      string    `json:"title"`  // contact name, deal title, or ticket subject
	Email      string    `json:"email,omitempty"`
	FollowUpAt time.Time `json:"follow_up_at"`
	Note       string    `json:"note,omitempty"`
	Overdue    bool      `json:"overdue"`
}

// Activity is a logged interaction (note, call, email, meeting) attached to a
// contact and optionally a deal.
type Activity struct {
	ID        string `json:"id"`
	Handle    string `json:"handle,omitempty"` // short public reference; see Contact.Handle
	ContactID string `json:"contact_id,omitempty"`
	DealID    string `json:"deal_id,omitempty"`
	CompanyID string `json:"company_id,omitempty"`
	TicketID  string `json:"ticket_id,omitempty"`
	// ContactHandle/DealHandle/CompanyHandle/TicketHandle are the resolved public
	// handles of the relation ids, populated on read for display (never persisted).
	ContactHandle string    `json:"contact_handle,omitempty"`
	DealHandle    string    `json:"deal_handle,omitempty"`
	CompanyHandle string    `json:"company_handle,omitempty"`
	TicketHandle  string    `json:"ticket_handle,omitempty"`
	Kind          string    `json:"kind"` // note | call | email | meeting | task
	Body          string    `json:"body"`
	CreatedBy     string    `json:"created_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// ---- timezone-aware display -------------------------------------------------
//
// Instants are always stored and compared in UTC. These helpers return copies
// with the instants expressed in a display location, so reads format in the
// workspace timezone without changing what is stored.

func inLoc(t time.Time, loc *time.Location) time.Time {
	if t.IsZero() || loc == nil {
		return t
	}
	return t.In(loc)
}

func inLocPtr(t *time.Time, loc *time.Location) *time.Time {
	if t == nil || loc == nil {
		return t
	}
	u := t.In(loc)
	return &u
}

// Localized returns a copy of the contact with its instants expressed in loc.
func (c Contact) Localized(loc *time.Location) Contact {
	c.CreatedAt = inLoc(c.CreatedAt, loc)
	c.UpdatedAt = inLoc(c.UpdatedAt, loc)
	c.FollowUpAt = inLocPtr(c.FollowUpAt, loc)
	c.LastActivityAt = inLocPtr(c.LastActivityAt, loc)
	return c
}

// Localized returns a copy of the company with its instants expressed in loc.
func (c Company) Localized(loc *time.Location) Company {
	c.CreatedAt = inLoc(c.CreatedAt, loc)
	c.UpdatedAt = inLoc(c.UpdatedAt, loc)
	c.LastActivityAt = inLocPtr(c.LastActivityAt, loc)
	return c
}

// Localized returns a copy of the deal with its instants expressed in loc.
func (d Deal) Localized(loc *time.Location) Deal {
	d.CreatedAt = inLoc(d.CreatedAt, loc)
	d.UpdatedAt = inLoc(d.UpdatedAt, loc)
	d.FollowUpAt = inLocPtr(d.FollowUpAt, loc)
	d.LastActivityAt = inLocPtr(d.LastActivityAt, loc)
	return d
}

// Localized returns a copy of the activity with its instant expressed in loc.
func (a Activity) Localized(loc *time.Location) Activity {
	a.CreatedAt = inLoc(a.CreatedAt, loc)
	return a
}

// Localized returns a copy of the ticket with its instants expressed in loc.
func (t Ticket) Localized(loc *time.Location) Ticket {
	t.CreatedAt = inLoc(t.CreatedAt, loc)
	t.UpdatedAt = inLoc(t.UpdatedAt, loc)
	t.FollowUpAt = inLocPtr(t.FollowUpAt, loc)
	t.LastActivityAt = inLocPtr(t.LastActivityAt, loc)
	return t
}

// Localized returns a copy of the campaign with its instants expressed in loc.
func (c Campaign) Localized(loc *time.Location) Campaign {
	c.CreatedAt = inLoc(c.CreatedAt, loc)
	c.UpdatedAt = inLoc(c.UpdatedAt, loc)
	return c
}

// Localized returns a copy of the campaign member with its instant in loc.
func (m CampaignMember) Localized(loc *time.Location) CampaignMember {
	m.AddedAt = inLoc(m.AddedAt, loc)
	return m
}

// Localized returns a copy of the reminder with its instant expressed in loc.
func (r Reminder) Localized(loc *time.Location) Reminder {
	r.FollowUpAt = inLoc(r.FollowUpAt, loc)
	return r
}

// Localized returns a copy of the member with its instant expressed in loc.
func (m Member) Localized(loc *time.Location) Member {
	m.CreatedAt = inLoc(m.CreatedAt, loc)
	return m
}

// Localized returns a copy of the workspace with its instant expressed in loc.
func (w Workspace) Localized(loc *time.Location) Workspace {
	w.CreatedAt = inLoc(w.CreatedAt, loc)
	return w
}
