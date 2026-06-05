# crmkit

**An agent-first CRM, built for AI agents to drive over plain HTTP.** Headless
by design - no UI, no SDK - just a plain-text, grepable API and a one-page
operating manual the agent loads. The agent (ChatGPT, Claude, Cursor, …) _is_
the interface.

```
┌──────────────┐   loads the manual    ┌──────────────────────────┐
│ landing page │ ───────────────────▶  │ chat client = the only UI │
└──────────────┘                       └────────────┬─────────────┘
                                                     │ HTTP + Bearer token
                                                     ▼
                                        ┌──────────────────────────┐
                                        │ crmkitd (Go, single file) │
                                        │ plain-text API · SQLite   │
                                        └──────────────────────────┘
```

## Why it looks the way it does

- **Plain text by default, JSON on demand.** Responses are one labeled,
  grepable line per record. Add `Accept: application/json` (or `?format=json`)
  for JSON. Plain text is token-cheap and survives context truncation.
- **Stable handles.** Every record is addressed by `kind/id`
  (e.g. `contact/c_ab12…`) that the agent threads through follow-up calls.
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

Contacts · Companies · Deals · Activities, all workspace-scoped (multi-tenant),
with an append-only audit log. Every entity carries a free-form `custom` JSON
object, so the schema is extensible without server changes.

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
GET /activities · GET /audit · GET /help
```

`DELETE` is two-step: the first call returns a confirmation token the agent
echoes back as `?confirm=<token>`.

## Development

```bash
make test     # go test ./...
make vet      # go vet ./...
make build    # static binaries
```

Releasing (version bump → tagged multi-platform build) is documented in
[RELEASES.md](RELEASES.md).
