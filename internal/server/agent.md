---
name: crmkit
description: An agent-first CRM, built for an AI agent to operate over a plain-text HTTP API. Headless by design (no UI). Responses are grepable plain text by default, or JSON when the Accept header requests it.
content_types: [text/plain, application/json]
capabilities: [contacts, companies, deals, tickets, tasks, campaigns, activities, workspaces, members]
authentication:
  type: bearer
  scheme: email-otp
  description: Email one-time-password - request a code, verify it, then send the returned token as a bearer credential on every request.
---

# crmkit - Agent Operating Manual

You are talking to crmkit, an agent-first CRM built for you to operate. It is
headless - there is no UI; you (the agent) are the interface. Drive it with
plain HTTP requests using your fetch/HTTP tool.

BASE_URL: <base_url>
AUTH: send the header Authorization: Bearer <token> on every request.
FORMAT: responses are plain text by default (one labeled line per record).
Add the header Accept: application/json (or ?format=json) for JSON.

Records are addressed by a short, stable handle like contact_k7m2q - reuse that
handle in later calls (in a path, use it as the {id}, e.g. GET /contacts/k7m2q
or GET /contacts/contact_k7m2q; both work). Relations are shown the same way
(company=company_x7k2). You can grep responses: each line stands alone.

CONCURRENT EDITS: a fetched contact/company/deal shows a "version". To avoid
overwriting another agent's change, send that version back on a PATCH - either as
"version": N in the body or an If-Match: N header. If the record changed since you
read it the update is rejected with 412; GET it again, re-apply onto the current
version, and retry. Omit the version to force the write (last edit wins).

## CONNECTING (pick your client)

How to reach crmkit depends on the client:

- **Standard chat apps - ChatGPT and Claude.ai:** crmkit runs an MCP connector at
  `<base_url>/mcp` - add it as a custom connector.
  - **ChatGPT:** the MCP connector is the only way in - add it and you're set.
  - **Claude.ai:** use the MCP connector, or call this HTTP API directly if you
    whitelist the domain `<base_url>` for the conversation.
- **Coding agents - Claude Code, Codex, Cursor:** just follow this manual and
  drive the HTTP API directly. Packaged recipe skills are also available:
  `npx skills add crmkit/skills`.
- **OpenClaw (and similar):** follow this manual the same way; its skills install
  with `npx clawhub@latest install crmkit`.

All paths drive the same plain-text HTTP API described below.

## MCP CONNECTOR

Besides this plain HTTP API, crmkit is also a Model Context Protocol server at
POST <base_url>/mcp, so you can add it as a connector in clients like ChatGPT
and Claude. Point the client at <base_url>/mcp and it will walk the standard
OAuth flow automatically: it registers itself (dynamic client registration),
opens a crmkit sign-in page where the user enters their email and pastes the
emailed code, then receives a token. Over MCP, crmkit exposes a single generic
tool, `request`, that calls this same HTTP API (method + path + optional body)
and returns the same plain text shown below - so everything in this manual is
reachable from the connector. The token a connector receives is an ordinary
crmkit token - it appears in GET /tokens and can be revoked there like any
other session.

## SKILLS (pre-built agent recipes)

Reusable skills that package common crmkit workflows live at
github.com/crmkit/skills. If your runtime supports agent skills (coding agents
like Claude Code, Codex, and Cursor - chat apps use the MCP connector above),
install them with one command: `npx skills add crmkit/skills`. They cover daily digests, CSV
imports, full backups, and turning emails into logged activities - conveniences,
since everything they do is just the plain HTTP calls below, so you never need
them to operate crmkit.

## FIRST-TIME AUTH

1. POST /auth/request {"email":"you@example.com"}
   -> a 6-digit login code is emailed to that address.
2. Ask the user for the code.
3. POST /auth/verify {"email":"you@example.com","code":"123456","token_name":"<who you are>"}
   -> returns a token. SAVE IT. Send it as Authorization: Bearer <token> from
   now on. If you can persist it in memory, do so; otherwise ask the user
   to keep it for next session.
   Set "token_name" to label this session - e.g. your client ("ChatGPT",
   "Claude", "Cursor") or its purpose. It appears in GET /tokens so the user can
   recognize and revoke sessions. Optional; defaults to "default".
   On any 401 (auth_required / invalid_token / token_expired), repeat this flow.
   Tokens are long-lived but expire after a period of inactivity (each use renews
   them), so a token in regular use keeps working; an idle one eventually dies.

## CONVENTIONS

- All request bodies are JSON. Only send fields you want to set/change.
- PATCH performs a partial update: omitted fields are preserved.
- DELETE is gated: the first call returns confirmation_required with a token;
  confirm with the user, then repeat with ?confirm=<token>.
- Errors are instructive: they include a hint telling you what to do next.
- Money is integer cents (amount_cents) plus a currency code.
- Times in reads are compact RFC3339 with the workspace's offset, e.g.
  2026-06-10T09:00-07:00 (or ...Z for UTC). Instants are stored in UTC; the
  workspace timezone (default UTC) only controls formatting. Set it with
  PATCH /workspaces/{id} {"timezone":"America/Los_Angeles"} (an IANA name). When
  you write a time (a task's due_at), include the offset so it lands on the
  instant the user means.
- POST /contacts and /companies UPSERT: if you include an email (contacts) or
  domain (companies) that already exists, the existing record is updated (merge
  of the fields you send) instead of creating a duplicate. The response says
  "# created" or "# updated". To update a specific record you already hold a
  handle for, use PATCH /contacts/{id}.

## WORKSPACES & TEAMS

Your token operates inside ONE workspace (the one it was minted for). CRM data
endpoints below always act on that workspace. To work in a different workspace
you belong to, mint a token for it and send that token instead:

GET /workspaces list workspaces you belong to (with your role + timezone)
POST /workspaces create one {"name":"My Team"}
PATCH /workspaces/{id} set the display timezone {"timezone":"America/Los_Angeles"} (admin only)
POST /workspaces/{id}/tokens mint a token scoped to that workspace ("switch")
GET /workspaces/{id}/members members + pending invites
POST /workspaces/{id}/invites add someone {"email":"x@acme.com","role":"member"} (admin only)
POST /workspaces/{id}/members/{userId}/role change role {"role":"admin"|"member"} (admin only)
DELETE /workspaces/{id}/members/{userId} remove a member (admin only)
DELETE /workspaces/{id} delete the workspace + all its data (admin only)

Roles are "admin" and "member". Admins manage membership; members operate the
CRM. Invites are by email: the invited person is emailed sign-in instructions
and joins automatically the next time they authenticate. Switching workspace
means switching which token you send - the CRM URLs below never change.

## PLANS & LIMITS

Each workspace/user has a plan with caps on how many objects you can create
(contacts, companies, deals, members, workspaces). Creating past a cap returns
"plan_limit_reached" (HTTP 403) - don't retry; tell the user the limit is hit.
GET /whoami shows the current plan and usage (e.g. "contacts: 12 / 1000"), so
check there to know the ceilings before bulk-creating.

## STEP-UP (sensitive actions)

Promoting a member to admin and deleting a workspace require an email
confirmation. Call the endpoint once: it returns "escalation_required" and emails
a code to your address. Ask the user for the code, then repeat the SAME request
with ?code=<code> (or header X-Escalation-Code: <code>).

## QUERY (the list endpoints: /contacts, /companies, /deals)

Filter by any allowed field as field=[op:]value ; repeated params are AND-ed:
?stage=lead (eq is the default operator)
?amount_cents=gte:100000 ops: eq ne gt gte lt lte like in is not
?stage=in:lead,qualified in: takes a comma-separated list
?due_at=is:null is:null / not:null check empty / non-empty
\*\_at fields accept RFC3339, e.g. created_at=gte:2026-01-01T00:00:00Z
Fuzzy search across key fields: ?search=acme
Filter by creator: ?created_by=agent@x.com - every record is stamped with the
member (human or agent) that created it; filter to organise records by who made
them.
Group by tags: contacts and companies carry a tags array (e.g. "competitor",
"watchlist", "fintech"). Set them on create/update; filter with ?tags=competitor
(repeat or comma-separate to require several, e.g. ?tags=competitor,fintech).
Filter by a custom field: ?custom.<key>=value matches a key inside the record's
custom JSON (contacts, companies, deals), e.g. ?custom.region=emea. Use
?custom.<key>=like:term for a contains match. Compared as text - best for string
custom fields.
Sort: ?sort=field or ?sort=-field (the - means descending).
Paginate: ?limit=N (default 50, max 200). When more rows remain, the response
ends with a line # next: <cursor> (JSON: "next_cursor"); fetch the next page
with ?cursor=<cursor> and keep the other params unchanged.
Unknown field/operator/value -> 400 listing what is allowed.
Activity at a glance: every list line carries activities=N (count) and
last_activity (most recent), so you can tell active from dormant records without a
fetch per row. To read what actually happened across a page, make one
GET /activities?<kind>=<comma-separated handles> call (e.g.
?company=company_a,company_b) and group the result by the company=/contact= handle
each line carries - no per-record calls.
Outreach: each contact/company line also carries outreach=N and last_outreach -
the activity subset of kinds call/email/meeting, i.e. "we reached out" (vs
note/task, which don't count). The same signal is filterable, so you segment who
you have / haven't reached without a tag: ?last_outreach=is:null (never contacted),
?last_outreach=lt:2026-05-01T00:00:00Z (reached before, now cold),
?outreach_count=gte:1 (reached at least once).

## RECORDING CONTACT - one field per job

"We contacted them" is an EVENT, not a label. Keep three jobs in three fields so a
page stays queryable:

- EVENT (you called, emailed, or met someone) -> log an activity:
  POST /contacts/{id}/activities {"kind":"email","body":"..."}. The activity log is
  the system of record for outreach - it keeps when, what, and who. Do NOT mark
  "contacted" with a tag; a tag can't say when or how often.
- LIFECYCLE (where the contact sits in your funnel) -> stage, e.g.
  new -> contacted -> engaged -> qualified -> customer. PATCH it as things move.
- LABEL (durable segments like "fintech", "watchlist", "competitor") -> tags.
  Tags group records; they are not events or state.

To find who you have or haven't reached, use the outreach filters under QUERY above
- no tag required.

## SEARCH (find anything in one call)

GET /search?q=acme runs the fuzzy search across contacts, companies, deals AND
tickets at once and returns grouped results (sections "# contacts / # companies /
# deals / # tickets"). Use it when you do not yet know which type a name belongs
to. Scope it with ?types=contacts,tickets (default: all). It returns up to a handful per type;
when a type is truncated the response says so - switch to that type's endpoint
(e.g. GET /contacts?search=acme) for the full, paginated list.

## ENDPOINTS

GET /help this manual
GET /healthz liveness probe (no auth)
GET /readyz readiness probe - checks the database (no auth)
GET /whoami identity + current workspace behind the token
GET /search?q=&types= find anything across contacts, companies, deals & tickets (grouped; see SEARCH)
GET /tokens list your active tokens (sessions)
DELETE /tokens/{id} revoke one of your tokens (log out a session)

GET /contacts?<filters>&search=&sort=&limit=&cursor= list/query contacts (see QUERY); each line carries activities=N & last_activity, so you see which records are active without a per-record fetch (same on the companies/deals/tickets lists)
POST /contacts create OR update by email (upsert) {"name":...,"email":...,"company_id":...,"stage":...,"tags":[...],"custom":{...}}
GET /contacts/{id} fetch one contact (includes created_by + an activity summary: activities=N, last_activity)
PATCH /contacts/{id} update fields
DELETE /contacts/{id}?confirm= delete (two-step)
GET /contacts/{id}/activities activity log for a contact
POST /contacts/{id}/activities log {"kind":"call|email|meeting|note|task","body":...}

GET /companies?<filters>&search=&sort=&limit=&cursor= list/query companies (see QUERY; search covers name, domain, notes)
POST /companies create OR update by domain (upsert) {"name":...,"domain":...,"tags":[...],"notes":"...","custom":{...}}
GET /companies/{id} fetch one company (includes created_by + an activity summary)
PATCH /companies/{id} update fields
DELETE /companies/{id}?confirm= delete (two-step)
GET /companies/{id}/activities activity log for a company
POST /companies/{id}/activities log {"kind":"call|email|meeting|note|task","body":...}

GET /deals?<filters>&search=&sort=&limit=&cursor= list/query deals (see QUERY)
POST /deals create {"title":...,"amount_cents":...,"currency":"USD","stage":...,"contact_id":...,"company_id":...}
GET /deals/{id} fetch one deal (includes created_by + an activity summary: activities=N, last_activity)
PATCH /deals/{id} update (e.g. {"stage":"won","status":"won"})
DELETE /deals/{id}?confirm= delete (two-step)

GET /tickets?<filters>&search=&sort=&limit=&cursor= list/query support tickets (see QUERY; search covers subject, content; filter status=open|pending|solved, assignee=, requester_id=)
POST /tickets create {"subject":...,"content":...,"requester_id":"contact_...","assignee":"agent@x.com","status":"open"}
GET /tickets/{id} fetch one ticket (includes a conversation summary: activities=N, last_activity)
PATCH /tickets/{id} update (e.g. {"status":"solved"}); supports If-Match / "version" for concurrency
DELETE /tickets/{id}?confirm= delete (two-step)
GET /tickets/{id}/activities the ticket's conversation (notes/replies, newest first)
POST /tickets/{id}/activities log {"kind":"note|call|email|meeting|task","body":...} onto the ticket

GET /tasks?<filters>&search=&sort=&limit=&cursor= list/query tasks (see QUERY; search covers title; filter assignee=, contact_id=, deal_id=, company_id=, ticket_id=, due_at=, done_at=; open tasks = done_at=is:null)
POST /tasks create {"title":...,"due_at":"2026-06-20T09:00:00Z","assignee":"agent@x.com","contact_id":"contact_..","deal_id":"deal_.."} - any of contact/company/deal/ticket may be linked, all optional
GET /tasks/{id} fetch one task (shows its links: contact_ref, company_ref, deal_ref, ticket_ref)
PATCH /tasks/{id} update (e.g. {"done":true} to complete, {"done":false} to reopen, {"due_at":...} to reschedule); supports If-Match / "version" for concurrency
DELETE /tasks/{id}?confirm= delete (two-step)

GET /campaigns?<filters>&search=&sort=&limit=&cursor= list/query campaigns (see QUERY; search covers name, description; filter status=active|paused|done)
POST /campaigns create {"name":...,"description":"what you're collecting & why"} (the description is the brief)
GET /campaigns/{id} fetch one campaign (includes member counts: contacts=N, companies=N)
PATCH /campaigns/{id} update (e.g. {"status":"done"}); supports If-Match / "version" for concurrency
DELETE /campaigns/{id}?confirm= delete (two-step; removes the memberships, not the contacts/companies)
GET /campaigns/{id}/members?kind=&limit= the contacts & companies in the campaign (newest first; kind=contact|company)
POST /campaigns/{id}/members attach one {"kind":"contact","id":"contact_k7m2q","reason":"matches the brief"} - idempotent, so re-attaching the same entity is a free no-op
DELETE /campaigns/{id}/members/{kind}/{id} detach one (e.g. .../members/contact/contact_k7m2q)
Shortcut: POST /contacts?campaign=campaign_..&reason=.. (and /companies) attaches the created/upserted record to the campaign in one call - so the usual loop is just POST /contacts?campaign=..

GET /reminders?days=&limit= due/overdue tasks (open tasks whose due_at has arrived; each shows about=<linked record> and assignee; ?days=N looks ahead)
GET /activities?contact=&deal=&company=&ticket=&limit= recent activities (each shows by= who logged it). Each filter takes one OR several comma-separated handles - e.g. ?company=company_a,company_b - so you pull the activity text for a whole list of records in one call (matches any of them), then group by the company=/contact= handle each line carries
DELETE /activities/{id} delete one activity (one-shot, no confirm; e.g. a mistake or to free quota)
GET /audit?by=&target=&limit= audit log = record history: who did what, and what changed (an update's detail shows the field diff, e.g. "stage: lead -> customer"). by=email filters to one member; target=<handle> (e.g. contact_k7m2q) scopes it to one record's history

## REMINDERS (pull, not push)

There is no background notifier. To track follow-up work, create a task with a
due_at, e.g. POST /tasks {"title":"Send the renewal quote","due_at":"2026-06-10T09:00:00Z",
"contact_id":"contact_k7m2q"}. A record can have several open tasks, and a task
can link any of a contact/company/deal/ticket (or none, to stand alone). Then
read what is due with GET /reminders (overdue + due now) at the start of a
session, or GET /reminders?days=7 to look a week ahead. Complete a task with
PATCH /tasks/{id} {"done":true} (it then drops out of reminders).

## EXAMPLES (curl)

# begin login

curl -s -X POST <base_url>/auth/request -d '{"email":"you@example.com"}'

# create a contact (note the bearer token)

curl -s -X POST <base*url>/contacts \
 -H 'Authorization: Bearer ck*...' \
 -d '{"name":"Jane Doe","email":"jane@acme.com","stage":"lead"}'

Custom fields: any keys you put under "custom" are stored as-is and returned on
the record, so the schema is extensible without server changes.
