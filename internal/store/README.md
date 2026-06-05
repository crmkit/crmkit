# store - pluggable persistence

The rest of crmkit depends only on the `Store` **interface** (see `store.go`).
The concrete backend is selected at startup by `Open(backend, dsn)` and chosen
via config (`storage.backend` / `storage.dsn`, or the `--backend` / `--dsn`
flags). Both backends are pure-Go, so the binary stays static (`CGO_ENABLED=0`).

| Backend    | DSN                                                     |
| ---------- | ------------------------------------------------------- |
| `sqlite`   | file path or `:memory:` (defaults to `server.db_path`)  |
| `postgres` | `postgres://user:pass@host:5432/crmkit?sslmode=require` |

## How it stays DRY

Rather than duplicate ~40 queries per backend, there is a single
`database/sql` implementation (`sqlStore`) parameterized by a small `dialect`
(`dialect.go`). Queries are written once with `?` placeholders and
case-insensitive search; the dialect:

- **rebinds placeholders** - `?` stays `?` on SQLite, becomes `$1, $2, …` on
  Postgres (all statements go through `s.exec/query/queryRow/txExec`).
- **picks the search keyword** - `LIKE` on SQLite (case-insensitive by default),
  `ILIKE` on Postgres.

The schema (`baselineSchema`) uses types valid on both (`TEXT`, and `BIGINT`,
which has INTEGER affinity in SQLite) and runs one statement at a time (pgx
rejects multi-statement `Exec`). Upserts use `ON CONFLICT … DO NOTHING/UPDATE`,
which both backends support - avoid SQLite-only syntax like `INSERT OR IGNORE`.

`openSQLite` (`sqlite.go`) and `openPostgres` (`postgres.go`) just set the
driver + dialect and connect - **they never touch schema**.

## Schema migrations (the only DDL writer)

`Open` is read-only with respect to schema. All schema creation and evolution
lives in `migrate.go`: an ordered `migrations` slice (version 1 is the baseline
`baselineSchema`; later versions are deltas) plus two methods on the interface:

- `MigrationStatus()` - read-only; reports applied vs pending. It never creates
  the bookkeeping table, so it is safe for dry runs and the server's startup
  check (a fresh database reports zero applied).
- `ApplyMigrations()` - the **single** code path that writes DDL. Each pending
  migration runs in its own transaction and is recorded in `schema_migrations`.

This keeps the data-mutating surface a small, auditable unit. The server
(`crmkitd`) only ever calls `MigrationStatus` and **refuses to start** when
anything is pending; applying is the explicit, back-up-friendly
`crmkitd migrate --execute` (see `cmd/crmkitd/migrate.go`).

## Tests

The full behavior suite runs against SQLite by default (`store_test.go`). The
same behaviors are verified against a real Postgres by `postgres_test.go`, which
is **skipped unless** `CRMKIT_TEST_POSTGRES_DSN` is set:

```bash
docker run -d --name pg -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=crmkit -p 55432:5432 postgres:16-alpine
CRMKIT_TEST_POSTGRES_DSN='postgres://postgres:secret@localhost:55432/crmkit?sslmode=disable' \
  go test ./internal/store/ -run TestPostgresEndToEnd -v
```

## Adding another backend

Implement `openX` returning a `*sqlStore` with an `X` dialect (or, for a
non-SQL store, a fresh type satisfying the `Store` interface), then add a `case`
to `Open`. The compile-time `var _ Store = (*sqlStore)(nil)` check enforces the
contract.
