# ratelimit - pluggable rate limiting

The server depends only on the `Limiter` / `Provider` interfaces (`ratelimit.go`).
The backend is chosen at startup by `Open(backend, dsn)` and configured via the
`ratelimit` block (`ratelimit.backend` / `ratelimit.dsn`).

| Backend  | DSN                   | Notes                                              |
| -------- | --------------------- | -------------------------------------------------- |
| `memory` | -                     | In-process token bucket. Default. Single-instance. |
| `redis`  | `redis://host:6379/0` | Shared across replicas (atomic Lua token bucket).  |

The Redis backend (`redis.go`) runs the token bucket as an atomic server-side
Lua script keyed by `crmkit:rl:<name>:<key>`, so all replicas enforce one global
limit. Idle buckets expire via TTL. If Redis is unreachable at request time the
limiter **fails open** (allows the request) and logs a warning, so a Redis blip
degrades to "no limiting" rather than rejecting all traffic. Integration test:

```bash
docker run -d --name redis -p 56379:6379 redis:7-alpine
CRMKIT_TEST_REDIS_URL='redis://localhost:56379/0' \
  go test ./internal/ratelimit/ -run TestRedisDistributedLimit -v
```

## Why pluggable

The in-memory limiter counts per process. The moment crmkitd runs as **multiple
replicas behind a load balancer**, each instance only sees its share of traffic,
so a global cap (e.g. "20 req/s per IP") is no longer enforced - a client can
round-robin across instances. That is the trigger to switch to a shared backend
(Redis), and the only reason to introduce Redis at all for crmkit today.

## Adding another backend

Implement a provider satisfying `Provider`:

```go
type Provider interface {
    Limiter(name string, rate float64, burst int) Limiter
    Close() error
}
type Limiter interface { Allow(key string) bool }
```

- `Limiter(name, rate, burst)` returns a limiter whose keys are namespaced by
  `name` (for example, `name + ":" + key`).
- Add a backend constant and wire the provider into `Open`.
- Add configuration validation and an integration test covering shared limits,
  failures, and cleanup.

The Redis implementation is the reference for a shared backend. Nothing in the
server changes because it already talks to the interfaces.
