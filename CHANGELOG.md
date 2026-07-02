# Changelog

All notable changes to crmkit, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/).

## [0.9.1] - 2026-07-02

### Changed

- MCP `tools/list` now annotates the generic `request` tool with a `title` and the full standard hint set — `readOnlyHint`, `destructiveHint`, and `openWorldHint` — all present and explicit. A single tool spans every HTTP method, so it is annotated for its worst case: not read-only and destructive (it can `PATCH`/`DELETE`); `openWorldHint` is `false` because crmkit only ever touches the caller's own workspace, never public/internet state. This satisfies app-directory scanners (e.g. the OpenAI apps store) that require every tool to set all three hints. Purpose-split, per-method-annotated connectors remain the recommended surface for directory submissions.

## [0.9.0] - 2026-06-23

### Features

- **On-behalf-of attribution.** Activities can record who an agent acted **on behalf of**: an optional `on_behalf_of` on activity creation (`POST /{contacts,companies,tickets}/{id}/activities`) names the principal an interaction was performed for — an email by convention (like `owner`/`assignee`), client-supplied, so it can name a teammate who has no crmkit account. It is a distinct attribution axis from `created_by`/`by=`, which stays the actor that actually wrote the row (the agent's own token): `by=` is who logged it, `on_behalf_of=` is who it was done for. It shows on every activity line and is filterable on the feed — `GET /activities?on_behalf_of=alice@acme.com` (case-insensitive).
- That per-activity principal **rolls up** to the records the activity belongs to. Each contact, company, deal, and ticket surfaces `on_behalf_of=<the set of people who have worked it>` on its line and detail — derived from its activities, so when several people work the same deal they **all** appear (it is a set, never a single overwritten value). Every such list is filterable by principal: `GET /deals?on_behalf_of=alice@acme.com`, `GET /contacts?on_behalf_of=alice@acme.com`, etc. The roll-up is computed on read (never stored), so it can't drift from the activity log it derives from. Tasks and campaigns have no activities and carry no `on_behalf_of`.
- Deals get their own activity endpoints, `GET/POST /deals/{id}/activities`, matching contacts/companies/tickets — so you log work straight onto a deal instead of cross-linking it from another record's activity. (The cross-link route still works and now resolves handles; see Fixed.)
- Adds migration **v19** (a nullable `on_behalf_of` column and its index on `activities` — the only place it is stored; the record roll-ups need no schema). Existing activities are unaffected. Back up, then run `crmkitd migrate --execute` after upgrading.

### Fixed

- Logging an activity that cross-links another record by **handle** in the body (e.g. `POST /contacts/{id}/activities {"deal_id":"deal_x7k2", …}`) now resolves that handle to the internal id before storing, instead of saving the handle verbatim. Previously such an activity was invisible to anything keyed on the id — the linked deal's/company's/ticket's activity count, `last_activity`, the `?deal=`/`?company=`/`?ticket=` activity feed filter, and the new `on_behalf_of` roll-up all missed it. The path record was always resolved; this extends the same resolution to the body's cross-link ids.

## [0.8.0] - 2026-06-23

### Features

- Outreach signal on contacts and companies. Every list line now also carries `outreach=N` and `last_outreach` — the activity subset of the kinds that mean "we reached out" (`call`/`email`/`meeting`), distinct from the all-kinds `activities`/`last_activity` already shown (a logged `note` or `task` does not count). The same signal is filterable, so segmenting who you have or haven't contacted no longer needs a tag: `?last_outreach=is:null` (never contacted), `?last_outreach=lt:2026-05-01T00:00:00Z` (reached before, now cold), `?outreach_count=gte:1` (reached at least once) — on both `/contacts` and `/companies`. It is derived from the activity log on read (never persisted), so it can never drift from the activities it summarises, and the displayed figure and the filtered figure share one source of truth. The agent manual now spells out the convention this enables: record outreach as an activity (not a tag), keep funnel position in `stage`, and reserve tags for durable labels.

## [0.7.0] - 2026-06-22

### Features

- List endpoints (`/contacts`, `/companies`, `/deals`, `/tickets`) now carry an at-a-glance activity signal on every line: `activities=N` (how many activities reference the record) and `last_activity` (when the most recent was logged) — the same summary a single-record `GET` already shows, but for a whole page in one batched query. Quiet records add nothing (a zero count and empty timestamp are omitted), so a caller can tell which records are active without a fetch per record. The fields are computed on read only (never persisted).
- `GET /activities` filters (`contact`, `deal`, `company`, `ticket`) now accept several comma-separated handles, not just one — e.g. `?company=company_a,company_b` matches the activity for any of them. This pulls the activity text for a whole list of records in a single call (group the results by the `company=`/`contact=` handle each line carries) instead of one request per record. A single handle still works exactly as before.

## [0.6.0] - 2026-06-18

### Features

- List endpoints (`/contacts`, `/companies`, `/deals`, `/tickets`, `/tasks`, `/campaigns`) now report the total number of records matching the query across all pages, not just the current page. Keyset pagination still drives navigation; the total is an extra hint so a caller (or agent) can tell how much is there and decide whether to keep paging or narrow its filters. It honours the same filters and `search` as the list and is exposed as a `total` field in JSON responses and a `# total: N` trailer in the grepable text format, alongside the existing `# next:` cursor.

## [0.5.1] - 2026-06-13

### Fixed

- Plan limits: a configured plan that omitted a resource cap (e.g. `max_tasks`) silently capped it at 0, rejecting every create with `plan_limit_reached`. Each cap is now optional and distinct from an explicit `0`: an omitted limit inherits the built-in default, an explicit `0` means "none allowed", and `-1` stays unlimited.

## [0.5.0] - 2026-06-13

### Features

- Tasks: a first-class, completable unit of follow-up work with a title, optional due date, done flag, assignee, and explicit links to any of a contact/company/deal/ticket. Full CRUD at `/tasks`; complete with `PATCH /tasks/{id} {"done":true}`.

### Changed

- **Breaking:** `GET /reminders` now surfaces due, open **tasks** (with `about` and `assignee`) instead of per-record follow-ups. Follow-up tracking moves from a single slot per record to tasks, which allow several open items per record and can be checked off.

### Removed

- **Breaking:** the `follow_up_at` / `follow_up_note` fields on contacts, deals, and tickets. Existing follow-ups are migrated to linked tasks automatically (migration v18); the columns are then dropped.

## [0.4.1] - 2026-06-12

### Features

- Campaigns carry a free-form `custom` JSON object, so they are extensible without a schema change.

## [0.4.0] - 2026-06-12

### Features

- Campaign management: a campaign is a prospecting effort (a free-text brief plus the contacts and companies gathered under it). Full CRUD at `/campaigns`, with many-to-many membership that is deduped per campaign, and a `?campaign=` shortcut on contact/company create to attach in one call.
- `GET /whoami` returns the workspace name alongside identity, plan, and usage.

## [0.3.1] - 2026-06-11

### Features

- Resolve the client IP from proxy headers (`X-Forwarded-For` / `X-Real-IP`) for accurate rate limiting and audit behind a reverse proxy.

## [0.3.0] - 2026-06-08

### Features

- Ticket management: full CRUD, ticket activities with conversation summaries, and follow-ups
- Extend cross-entity search to include tickets
- Stricter email validation on contact and auth flows

## [0.2.0] - 2026-06-07

### Features

- Implement optimistic concurrency control with versioning for contacts, companies, and deals
- Implement detailed audit logging with filtering by target and record history
- Update confirmation messages to use stable internal IDs for deletions

## [0.1.11] - 2026-06-07

### Features

- Introduce short, workspace-scoped public handles for CRM records

## [0.1.10] - 2026-06-07

### Features

- Add tags support for companies and contacts, including filtering
- Implement custom-field filtering for companies and deals
- Harden query parsing against SQL injection (cursor validation + safety coverage)
- Enhance company management with notes and activity logging
- Implement activity deletion and activity-quota management
- Implement audit-log retention with pruning

## [0.1.9] - 2026-06-06

The initial public line of crmkit - an agent-first, headless CRM driven over a
plain-text HTTP API, with an MCP connector for chat clients.

### Features

- Core CRM: contacts, companies, deals, and activities, all workspace-scoped
- Cross-entity search across contacts, companies, and deals
- Per-workspace timezone with localized date display
- `created_by` attribution and activity summaries on records, with filtering by creator
- Audit entries attributed to the acting member (human or agent)
- Create a workspace during the OAuth authorization flow
- OTP auth with bearer tokens, MCP connector, and per-plan resource limits
