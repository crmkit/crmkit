<p align="center">
  <img src="https://crmkit.ai/icon-dark.svg" alt="crmkit" width="100" height="100" />
</p>

<h1 align="center">crmkit</h1>

**An agent-first CRM, built for AI agents to drive over plain HTTP.** Headless
by design - no UI, no SDK - just a plain-text, grepable API and a one-page
operating manual the agent loads. The agent (ChatGPT, Claude, Cursor, …) _is_
the interface.

## Status

crmkit is **0.x**: functional and in active use, with continuous improvements
landing release to release. Until 1.0 the API, storage schema, and behavior may
still change between versions - including breaking changes - so pin a version and
skim the [changelog](CHANGELOG.md) before upgrading. The hosted version at
[crmkit.ai](https://crmkit.ai) tracks the latest and stays up to date
automatically.

## Why crmkit exists

A CRM looks like something an agent could just keep in a few markdown files or a
scratch database table. That holds right up until it doesn't. Underneath, a CRM
is a pile of small problems that are each easy to get subtly wrong and painful to
fix once real data has piled up:

- stable IDs that survive renames and re-imports,
- relations between contacts, companies, and deals that stay consistent,
- search, filtering, and pagination that hold up as records grow,
- dedupe, validation, and safe concurrent writes,
- history, audit, and per-tenant limits,
- schema changes you can apply to a live database without losing data.

And a CRM is rarely driven by one agent. Several agents - and the teammates
alongside them - work the same records at the same time, so you need one shared
system of record: consistent reads, safe concurrent writes, and a clear trail of
which agent or member did what. Files and ad-hoc tables fall apart exactly here.

crmkit is that core - one well-maintained, battle-tested foundation that many
agents and deployments share, so you build on solved problems instead of
rediscovering them ad hoc in every project.

## Use cases

crmkit is useful anywhere an agent needs to remember people, organizations,
opportunities, and follow-up work in one shared system:

- **Sales CRM** - track leads, accounts, deals, activities, next steps, and
  pipeline movement without building a dashboard first.
- **Personal contact management** - keep notes, reminders, relationship context,
  and follow-ups for your own network.
- **Customer support** - record customers, companies, conversations, issues,
  escalations, and renewal or upsell opportunities.
- **Market monitoring** - track funded companies, competitors, target accounts,
  hiring signals, product launches, and other entities an agent watches over
  time.
- **Fundraising** - manage investors, intros, conversations, diligence status,
  commitments, and follow-up tasks across a raise.

**Example:** ChatBotKit's [AI Market Bot](https://chatbotkit.com/examples/ai-market-bot)
- a market-research and competitive-intelligence agent - is the market-monitoring
use case above, made real.

## Why it looks the way it does

- **Plain text by default, JSON on demand.** Responses are one labeled,
  grepable line per record. Add `Accept: application/json` (or `?format=json`)
  for JSON. Plain text is token-cheap and survives context truncation.
- **Short, stable handles.** Every record is addressed by a short workspace-
  scoped handle (e.g. `contact_k7m2q`) that the agent threads through follow-up
  calls - token-cheap and easy to reference.
- **Instructive errors.** Every 4xx returns a `hint` telling the agent what to
  do next - it self-corrects without a schema to lean on.
- **OTP auth, bearer tokens.** First login emails a 6-digit code; verifying it
  mints a long-lived token sent as `Authorization: Bearer <token>`.
- **Single static binary.** Pure-Go SQLite (`modernc.org/sqlite`), so
  `CGO_ENABLED=0` builds a static binary that deploys as one file.

## Quick start

```bash
make build                       # builds ./crmkitd
./crmkitd migrate --execute      # create/upgrade the schema (the only step that writes DDL)
./crmkitd --local --listen :8080 # local mode: single-user, echoes login codes (no email needed)
```

The server never creates or alters schema: it opens the database read-only and
refuses to start until the schema is current, so you always get a chance to back
up first. Run `crmkitd migrate` (a dry run that prints the pending SQL and writes
nothing) to preview, then `crmkitd migrate --execute` to apply.

Then drive it like an agent would:

```bash
B=http://localhost:8080
# 1. request a login code (local mode prints it in the response)
curl -s -X POST $B/auth/request -d '{"email":"you@example.com"}'
# 2. verify the code to get a token
curl -s -X POST $B/auth/verify -d '{"email":"you@example.com","code":"123456"}'
# 3. use the token
curl -s -X POST $B/contacts -H 'Authorization: Bearer ck_…' \
  -d '{"name":"Jane Doe","email":"jane@acme.com","stage":"lead"}'
curl -s $B/contacts -H 'Authorization: Bearer ck_…'
```

See the full agent manual any time:

```bash
curl -s $B/help          # or GET /.well-known/agent.md
```

## Binaries

| Command                     | Role                                                            |
| --------------------------- | --------------------------------------------------------------- |
| `crmkitd`                   | The HTTP API server (the backend).                              |
| `crmkitd migrate`           | Dry run: report pending migrations + SQL, write nothing.        |
| `crmkitd migrate --execute` | Apply pending migrations (the only command that writes schema). |

## Configuration

crmkitd runs with zero config. Configure it three ways, layered
(`defaults < file < env < flags`):

**Config file** - copy [`configs/crmkit.example.yaml`](configs/crmkit.example.yaml)
to `~/.config/crmkit/config.yaml` (or pass `--config`).

**Environment variables** - every field has a `CRMKIT_<PATH>` var (the YAML path
upper-cased, dots → underscores). crmkitd boots from env alone, no file needed -
ideal for containers/serverless:

```
CRMKIT_SERVER_LISTEN_ADDR=:8080
CRMKIT_SERVER_BASE_URL=https://api.example.com
CRMKIT_SERVER_SECRET_KEY=$(openssl rand -hex 32)
CRMKIT_STORAGE_DSN=postgres://…   # backend inferred from the DSN (postgres:// or postgresql:// → postgres)
CRMKIT_RATELIMIT_BACKEND=redis    CRMKIT_RATELIMIT_DSN=redis://…
CRMKIT_EMAIL_PROVIDER=cloudflare  CRMKIT_EMAIL_CLOUDFLARE_ACCOUNT_ID=…  CRMKIT_EMAIL_CLOUDFLARE_API_TOKEN=…
#   (or provider=resend / ses / smtp - each with its own CRMKIT_EMAIL_* fields)
```

**Flags** (highest precedence): `--listen`, `--db`, `--backend`, `--dsn`,
`--base-url`, `--log-format`, `--local`.

## Data model

Contacts · Companies · Deals · Tickets · Activities, all workspace-scoped
(multi-tenant), with an append-only audit log. Every entity carries a free-form
`custom` JSON object, so the schema is extensible without server changes.

## Plans & limits

Every user and workspace is assigned a **plan** (default `basic`) that caps how
many objects can be created - contacts/companies/deals and members per
workspace, workspaces per user. Creating past a cap returns `plan_limit_reached`;
`GET /whoami` reports the current plan and usage. Plan limits are defined in the
`plans:` config block (built-in defaults apply with no config); raising a row's
plan grants higher limits. See [`configs/crmkit.example.yaml`](configs/crmkit.example.yaml).

## API surface

```
POST /auth/request · POST /auth/verify · GET /whoami
GET/POST /contacts · GET/PATCH/DELETE /contacts/{id}
GET/POST /contacts/{id}/activities
GET/POST /companies · GET/PATCH/DELETE /companies/{id}
GET/POST /deals · GET/PATCH/DELETE /deals/{id}
GET/POST /tickets · GET/PATCH/DELETE /tickets/{id}
GET /activities · GET /audit · GET /help
```

`DELETE` is two-step: the first call returns a confirmation token the agent
echoes back as `?confirm=<token>`.

## MCP connector

crmkit also speaks the **Model Context Protocol** at `POST /mcp`, so it plugs
into chat clients (ChatGPT, Claude) as a one-click connector. Point the client at
`<base_url>/mcp` and it walks the standard OAuth 2.1 flow on its own - dynamic
client registration, then a crmkit sign-in page where the user enters their email
and pastes the emailed code, then a token. Over MCP, crmkit exposes a single
generic `request` tool (method + path + optional body) that calls this same HTTP
API - its description is a compact manual and `GET /help` returns the full one -
so the whole API is reachable without re-encoding each endpoint as a tool. Tool
results are the same plain text as the HTTP API.

```
POST /mcp                                  (JSON-RPC: initialize, tools/list, tools/call)
GET  /.well-known/oauth-protected-resource (RFC 9728)
GET  /.well-known/oauth-authorization-server (RFC 8414)
POST /oauth/register                       (RFC 7591 dynamic client registration)
GET/POST /oauth/authorize · POST /oauth/token · POST /oauth/revoke
```

The OAuth access token is an ordinary crmkit token - it shows up in `GET /tokens`
and is revocable there. `mcp.allowed_redirect_uris` (default `["*"]`, kept safe by
mandatory PKCE) restricts which client callbacks may register.

## Skills

Pre-built **agent skills** - digest, import, backup, inbox-sync - live in
[crmkit/skills](https://github.com/crmkit/skills), for coding agents with a
filesystem (on a chat app, use the MCP connector above instead). Install the set
with one command (works across Claude Code, Cursor, and 40+ agents via
[`npx skills`](https://github.com/vercel-labs/skills)):

```bash
npx skills add crmkit/skills
```

Or grab the release zip (offline / pinned) and unzip into your skills directory:

```bash
curl -fsSL https://github.com/crmkit/skills/releases/latest/download/crmkit-skills.zip -o /tmp/crmkit-skills.zip \
  && unzip -o /tmp/crmkit-skills.zip -d ~/.claude/skills/
```

Each recipe needs `CRMKIT_BASE_URL` and a crmkit token (`POST /auth/request` →
`/auth/verify`), and defers to this server's manual (`GET /help`) for syntax.

## Development

```bash
make test     # go test ./...
make vet      # go vet ./...
make build    # static binaries
```

Releasing (version bump → tagged multi-platform build) is documented in
[RELEASES.md](RELEASES.md).
