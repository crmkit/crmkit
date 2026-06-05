package server

import (
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Manual returns the agent operating manual served at GET /help and
// /.well-known/agent.md. It begins with YAML frontmatter (the single source of
// truth for the agent card's static metadata) followed by the prose manual a
// model reads. The frontmatter has no base_url-dependent fields; the prose body
// interpolates base_url.
func Manual(baseURL string) string {
	b := strings.Builder{}
	fmt.Fprintf(&b, manualTemplate, baseURL, baseURL, baseURL)
	return strings.TrimSpace(b.String())
}

// ManualMeta is the structured metadata parsed from the manual's frontmatter.
type ManualMeta struct {
	Name           string   `yaml:"name"`
	Description    string   `yaml:"description"`
	ContentTypes   []string `yaml:"content_types"`
	Capabilities   []string `yaml:"capabilities"`
	Authentication struct {
		Type        string `yaml:"type"`
		Scheme      string `yaml:"scheme"`
		Description string `yaml:"description"`
	} `yaml:"authentication"`
}

var (
	metaOnce sync.Once
	meta     ManualMeta
)

// Frontmatter parses (once) and returns the manual's frontmatter metadata. It is
// the single source of truth shared by the manual and the JSON agent card.
func Frontmatter() ManualMeta {
	metaOnce.Do(func() {
		front, _ := splitFrontmatter(manualTemplate)
		_ = yaml.Unmarshal([]byte(front), &meta)
	})
	return meta
}

// splitFrontmatter separates a leading "---\n...\n---" YAML block from the body.
// If there is no frontmatter it returns an empty front and the whole input.
func splitFrontmatter(doc string) (front, body string) {
	doc = strings.TrimLeft(doc, "\n")
	if !strings.HasPrefix(doc, "---\n") {
		return "", doc
	}
	rest := doc[len("---\n"):]
	if i := strings.Index(rest, "\n---"); i >= 0 {
		return rest[:i], strings.TrimLeft(rest[i+len("\n---"):], "\n")
	}
	return "", doc
}

const manualTemplate = `
---
name: crmkit
description: An agent-first CRM, built for an AI agent to operate over a plain-text HTTP API. Headless by design (no UI). Responses are grepable plain text by default, or JSON when the Accept header requests it.
content_types: [text/plain, application/json]
capabilities: [contacts, companies, deals, activities, workspaces, members]
authentication:
  type: bearer
  scheme: email-otp
  description: Email one-time-password - request a code, verify it, then send the returned token as a bearer credential on every request.
---

crmkit - Agent Operating Manual
===============================

You are talking to crmkit, an agent-first CRM built for you to operate. It is
headless - there is no UI; you (the agent) are the interface. Drive it with
plain HTTP requests using your fetch/HTTP tool.

BASE_URL: %s
AUTH:     send the header  Authorization: Bearer <token>  on every request.
FORMAT:   responses are plain text by default (one labeled line per record).
          Add the header  Accept: application/json  (or ?format=json) for JSON.

Records are addressed by a stable handle like  contact/c_ab12...  - reuse that
handle (or the bare id after the slash) in later calls. You can grep responses:
each line stands alone.

FIRST-TIME AUTH
---------------
1. POST /auth/request   {"email":"you@example.com"}
     -> a 6-digit login code is emailed to that address.
2. Ask the user for the code.
3. POST /auth/verify    {"email":"you@example.com","code":"123456"}
     -> returns a token. SAVE IT. Send it as Authorization: Bearer <token> from
        now on. If you can persist it in memory, do so; otherwise ask the user
        to keep it for next session.
On any 401 (auth_required / invalid_token / token_expired), repeat this flow.
Tokens are long-lived but expire after a period of inactivity (each use renews
them), so a token in regular use keeps working; an idle one eventually dies.

CONVENTIONS
-----------
- All request bodies are JSON. Only send fields you want to set/change.
- PATCH performs a partial update: omitted fields are preserved.
- DELETE is gated: the first call returns confirmation_required with a token;
  confirm with the user, then repeat with ?confirm=<token>.
- Errors are instructive: they include a hint telling you what to do next.
- Money is integer cents (amount_cents) plus a currency code.
- POST /contacts and /companies UPSERT: if you include an email (contacts) or
  domain (companies) that already exists, the existing record is updated (merge
  of the fields you send) instead of creating a duplicate. The response says
  "# created" or "# updated". To update a specific record you already hold a
  handle for, use PATCH /contacts/{id}.

WORKSPACES & TEAMS
------------------
Your token operates inside ONE workspace (the one it was minted for). CRM data
endpoints below always act on that workspace. To work in a different workspace
you belong to, mint a token for it and send that token instead:

GET    /workspaces                  list workspaces you belong to (with your role)
POST   /workspaces                  create one  {"name":"My Team"}
POST   /workspaces/{id}/tokens      mint a token scoped to that workspace ("switch")
GET    /workspaces/{id}/members     members + pending invites
POST   /workspaces/{id}/invites     add someone  {"email":"x@acme.com","role":"member"} (admin only)
POST   /workspaces/{id}/members/{userId}/role   change role {"role":"admin"|"member"} (admin only)
DELETE /workspaces/{id}/members/{userId}   remove a member (admin only)
DELETE /workspaces/{id}               delete the workspace + all its data (admin only)

Roles are "admin" and "member". Admins manage membership; members operate the
CRM. Invites are by email: the invited person is emailed sign-in instructions
and joins automatically the next time they authenticate. Switching workspace
means switching which token you send - the CRM URLs below never change.

PLANS & LIMITS
--------------
Each workspace/user has a plan with caps on how many objects you can create
(contacts, companies, deals, members, workspaces). Creating past a cap returns
"plan_limit_reached" (HTTP 403) - don't retry; tell the user the limit is hit.
GET /whoami shows the current plan and usage (e.g. "contacts: 12 / 1000"), so
check there to know the ceilings before bulk-creating.

STEP-UP (sensitive actions)
---------------------------
Promoting a member to admin and deleting a workspace require an email
confirmation. Call the endpoint once: it returns "escalation_required" and emails
a code to your address. Ask the user for the code, then repeat the SAME request
with ?code=<code> (or header X-Escalation-Code: <code>).

QUERY (the list endpoints: /contacts, /companies, /deals)
---------------------------------------------------------
Filter by any allowed field as  field=[op:]value ; repeated params are AND-ed:
  ?stage=lead                  (eq is the default operator)
  ?amount_cents=gte:100000     ops: eq ne gt gte lt lte like in is not
  ?stage=in:lead,qualified     in: takes a comma-separated list
  ?follow_up_at=is:null        is:null / not:null check empty / non-empty
  *_at fields accept RFC3339, e.g. created_at=gte:2026-01-01T00:00:00Z
Fuzzy search across key fields:  ?search=acme
Sort:  ?sort=field  or  ?sort=-field  (the - means descending).
Paginate:  ?limit=N  (default 50, max 200). When more rows remain, the response
ends with a line  # next: <cursor>  (JSON: "next_cursor"); fetch the next page
with ?cursor=<cursor> and keep the other params unchanged.
Unknown field/operator/value -> 400 listing what is allowed.

ENDPOINTS
---------
GET    /help                        this manual
GET    /healthz                     liveness probe (no auth)
GET    /readyz                      readiness probe - checks the database (no auth)
GET    /whoami                      identity + current workspace behind the token
GET    /tokens                      list your active tokens (sessions)
DELETE /tokens/{id}                 revoke one of your tokens (log out a session)

GET    /contacts?<filters>&search=&sort=&limit=&cursor=   list/query contacts (see QUERY)
POST   /contacts                    create OR update by email (upsert)  {"name":...,"email":...,"company_id":...,"stage":...,"tags":[...],"custom":{...}}
GET    /contacts/{id}               fetch one contact
PATCH  /contacts/{id}               update fields
DELETE /contacts/{id}?confirm=      delete (two-step)
GET    /contacts/{id}/activities    activity log for a contact
POST   /contacts/{id}/activities    log  {"kind":"call|email|meeting|note|task","body":...}

GET    /companies?<filters>&search=&sort=&limit=&cursor=  list/query companies (see QUERY)
POST   /companies                   create OR update by domain (upsert)  {"name":...,"domain":...,"custom":{...}}
GET    /companies/{id}              fetch one company
PATCH  /companies/{id}              update fields
DELETE /companies/{id}?confirm=     delete (two-step)

GET    /deals?<filters>&search=&sort=&limit=&cursor=      list/query deals (see QUERY)
POST   /deals                       create  {"title":...,"amount_cents":...,"currency":"USD","stage":...,"contact_id":...,"company_id":...}
GET    /deals/{id}                  fetch one deal
PATCH  /deals/{id}                  update (e.g. {"stage":"won","status":"won"})
DELETE /deals/{id}?confirm=         delete (two-step)

GET    /reminders?days=&limit=      due/overdue follow-ups (contacts + deals due now; ?days=N looks ahead)
GET    /activities?contact=&deal=&limit=          recent activities
GET    /audit?limit=                workspace audit log

REMINDERS (pull, not push)
--------------------------
There is no background notifier. To track a follow-up, set follow_up_at (RFC3339)
on a contact or deal, e.g. PATCH /contacts/{id} {"follow_up_at":"2026-06-10T09:00:00Z",
"follow_up_note":"Send the renewal quote"}. Then read what is due with
GET /reminders (overdue + due now) at the start of a session, or GET /reminders?days=7
to look a week ahead. Clear a follow-up by PATCHing follow_up_at to null.

EXAMPLES (curl)
---------------
# begin login
curl -s -X POST %s/auth/request -d '{"email":"you@example.com"}'

# create a contact (note the bearer token)
curl -s -X POST %s/contacts \
  -H 'Authorization: Bearer ck_...' \
  -d '{"name":"Jane Doe","email":"jane@acme.com","stage":"lead"}'

Custom fields: any keys you put under "custom" are stored as-is and returned on
the record, so the schema is extensible without server changes.
`
