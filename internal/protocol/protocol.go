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
}

// Contact is a person in the CRM.
type Contact struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Email     string         `json:"email,omitempty"`
	Phone     string         `json:"phone,omitempty"`
	CompanyID string         `json:"company_id,omitempty"`
	Owner     string         `json:"owner,omitempty"`
	Stage     string         `json:"stage,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	Notes     string         `json:"notes,omitempty"`
	Custom    map[string]any `json:"custom,omitempty"`
	// FollowUpAt is when this contact should next be followed up (RFC3339).
	// Send null to clear it. Agents read due/overdue items via GET /reminders.
	FollowUpAt   *time.Time `json:"follow_up_at,omitempty"`
	FollowUpNote string     `json:"follow_up_note,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Company is an organization that contacts and deals belong to.
type Company struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Domain    string         `json:"domain,omitempty"`
	Custom    map[string]any `json:"custom,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Deal is an opportunity moving through a pipeline.
type Deal struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	ContactID   string         `json:"contact_id,omitempty"`
	CompanyID   string         `json:"company_id,omitempty"`
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
