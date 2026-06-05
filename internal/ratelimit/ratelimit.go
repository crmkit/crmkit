// Package ratelimit provides a pluggable rate-limiting seam. The server depends
// on the Limiter/Provider interfaces; the backend is chosen at startup by Open.
// Two backends ship: an in-process token bucket (default, great for a single
// instance) and a Redis-backed distributed limiter (needed once crmkitd runs as
// multiple replicas, where per-instance counters no longer enforce a global
// limit). See redis.go.
package ratelimit

import (
	"fmt"
	"sync"
	"time"
)

// Backend identifiers accepted by Open.
const (
	BackendMemory = "memory"
	BackendRedis  = "redis"
)

// Limiter decides whether one event for a key is permitted now.
type Limiter interface {
	Allow(key string) bool
}

// Provider mints named limiters that share a backend (e.g. one Redis client).
// name namespaces keys across limiters; rate is tokens/second and burst is the
// bucket capacity.
type Provider interface {
	Limiter(name string, rate float64, burst int) Limiter
	Close() error
}

// Open constructs a Provider for the given backend. dsn is backend-specific:
// ignored for "memory"; a redis:// URL for "redis".
func Open(backend, dsn string) (Provider, error) {
	switch backend {
	case "", BackendMemory:
		return &memoryProvider{}, nil
	case BackendRedis:
		return openRedis(dsn)
	default:
		return nil, fmt.Errorf("unknown rate-limit backend %q (use %q or %q)", backend, BackendMemory, BackendRedis)
	}
}

// ---- in-memory token-bucket backend --------------------------------------

type memoryProvider struct{}

func (p *memoryProvider) Limiter(name string, rate float64, burst int) Limiter {
	return newMemoryLimiter(rate, burst)
}

func (p *memoryProvider) Close() error { return nil }

// memoryLimiter is an in-process token-bucket limiter keyed by an arbitrary
// string (client IP, email, …). Buckets are created lazily and swept
// periodically so memory stays bounded. Safe for concurrent use.
type memoryLimiter struct {
	rate  float64
	burst float64

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newMemoryLimiter(rate float64, burst int) *memoryLimiter {
	if burst < 1 {
		burst = 1
	}
	return &memoryLimiter{
		rate:      rate,
		burst:     float64(burst),
		buckets:   make(map[string]*bucket),
		lastSweep: time.Now(),
	}
}

// Allow reports whether one event for key is permitted now, consuming a token
// if so. A rate <= 0 disables limiting.
func (l *memoryLimiter) Allow(key string) bool {
	if l.rate <= 0 {
		return true
	}
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops idle buckets at most once a minute. A bucket idle long enough to
// have fully refilled is at capacity and can be dropped without changing
// behavior.
func (l *memoryLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now

	maxIdle := time.Minute
	if l.rate > 0 {
		if full := time.Duration(l.burst / l.rate * float64(time.Second)); full > maxIdle {
			maxIdle = full
		}
	}
	for k, b := range l.buckets {
		if now.Sub(b.last) > maxIdle {
			delete(l.buckets, k)
		}
	}
}
