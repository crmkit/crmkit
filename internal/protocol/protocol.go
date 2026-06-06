// Package protocol defines the core CRM domain types shared between the
// storage layer, the HTTP server, and the rendering layer. Every entity is
// addressed by a stable, prefixed handle (e.g. "contact/c_3f9a...") that
// agents thread through follow-up calls.
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

// Handle returns the "kind/id" reference token for an entity.
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
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	Phone     string `json:"phone,omitempty"`
	CompanyID string `json:"company_id,omitempty"`
	// CompanyName is the resolved name of CompanyID, populated on read for
	// display (never persisted). Empty if the contact has no company.
	CompanyName string         `json:"company_name,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Stage       string         `json:"stage,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Notes       string         `json:"notes,omitempty"`
	Custom      map[string]any `json:"custom,omitempty"`
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
	Name      string         `json:"name"`
	Domain    string         `json:"domain,omitempty"`
	Custom    map[string]any `json:"custom,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	// CreatedBy is the member (human or agent) who created this record.
	CreatedBy string `json:"created_by,omitempty"`
}

// Deal is an opportunity moving through a pipeline.
type Deal struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ContactID string `json:"contact_id,omitempty"`
	CompanyID string `json:"company_id,omitempty"`
	// ContactName / CompanyName are the resolved names of ContactID / CompanyID,
	// populated on read for display (never persisted).
	ContactName string         `json:"contact_name,omitempty"`
	CompanyName string         `json:"company_name,omitempty"`
	AmountCents int64          `json:"amount_cents,omitempty"`
	Currency    string         `json:"currency,omitempty"`
	Stage       string         `json:"stage,omitempty"`
	Status      string         `json:"status,omitempty"` // open | won | lost
	Custom      map[string]any `json:"custom,omitempty"`
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

// Reminder is a due/overdue follow-up surfaced by GET /reminders - a contact or
// deal whose follow_up_at has arrived. Agents pull these instead of the server
// pushing notifications.
type Reminder struct {
	Handle     string    `json:"handle"` // contact/c_… or deal/d_…
	Kind       string    `json:"kind"`   // contact | deal
	Title      string    `json:"title"`  // contact name or deal title
	Email      string    `json:"email,omitempty"`
	FollowUpAt time.Time `json:"follow_up_at"`
	Note       string    `json:"note,omitempty"`
	Overdue    bool      `json:"overdue"`
}

// Activity is a logged interaction (note, call, email, meeting) attached to a
// contact and optionally a deal.
type Activity struct {
	ID        string    `json:"id"`
	ContactID string    `json:"contact_id,omitempty"`
	DealID    string    `json:"deal_id,omitempty"`
	Kind      string    `json:"kind"` // note | call | email | meeting | task
	Body      string    `json:"body"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
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
