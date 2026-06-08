package ratelimit

import (
	"testing"
	"time"
)

func memLimiter(t *testing.T, rate float64, burst int) Limiter {
	t.Helper()
	p, err := Open(BackendMemory, "")
	if err != nil {
		t.Fatalf("open memory provider: %v", err)
	}
	return p.Limiter("test", rate, burst)
}

func TestLimiterBurstThenBlocks(t *testing.T) {
	l := memLimiter(t, 1, 3) // 1 token/sec, burst 3
	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatalf("burst token %d should be allowed", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("4th request should be blocked")
	}
	if !l.Allow("other") {
		t.Fatal("a different key should be independent")
	}
}

func TestLimiterDisabled(t *testing.T) {
	l := memLimiter(t, 0, 1) // rate 0 disables limiting
	for i := 0; i < 100; i++ {
		if !l.Allow("k") {
			t.Fatal("rate 0 should allow everything")
		}
	}
}

func TestLimiterRefills(t *testing.T) {
	l := memLimiter(t, 100, 1) // 100/sec
	if !l.Allow("k") {
		t.Fatal("first request should be allowed")
	}
	if l.Allow("k") {
		t.Fatal("second immediate request should be blocked")
	}
	time.Sleep(25 * time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("should be allowed again after refill")
	}
}

func TestOpenUnknownBackend(t *testing.T) {
	if _, err := Open("memcached", ""); err == nil {
		t.Fatal("expected error for unknown backend")
	}
	// An invalid redis DSN fails fast at parse time (no network).
	if _, err := Open(BackendRedis, "http://not-redis"); err == nil {
		t.Fatal("expected error for invalid redis dsn")
	}
}

func TestMemoryLimiterBurstClamp(t *testing.T) {
	if l := newMemoryLimiter(1, 0); l.burst != 1 {
		t.Errorf("burst < 1 must clamp to 1, got %v", l.burst)
	}
	if l := newMemoryLimiter(1, -5); l.burst != 1 {
		t.Errorf("negative burst must clamp to 1, got %v", l.burst)
	}
}

// TestMemoryLimiterSweep covers the idle-bucket eviction, which Allow-based tests
// never reach (it runs at most once a minute). With rate 1/burst 5 the refill
// window is 5s, so maxIdle stays at the 1-minute floor.
func TestMemoryLimiterSweep(t *testing.T) {
	l := newMemoryLimiter(1, 5)
	now := time.Now()
	l.buckets["fresh"] = &bucket{tokens: 0, last: now.Add(-10 * time.Second)} // within maxIdle -> kept
	l.buckets["stale"] = &bucket{tokens: 5, last: now.Add(-2 * time.Minute)}  // beyond maxIdle -> dropped
	l.lastSweep = now.Add(-2 * time.Minute)                                   // open the once-a-minute gate

	l.sweep(now)

	if _, ok := l.buckets["stale"]; ok {
		t.Error("a bucket idle beyond maxIdle must be swept")
	}
	if _, ok := l.buckets["fresh"]; !ok {
		t.Error("a recently-used bucket must be retained")
	}
}

// TestMemoryLimiterSweepThrottled verifies the sweep is a no-op when the last one
// was under a minute ago, even with a clearly-stale bucket present.
func TestMemoryLimiterSweepThrottled(t *testing.T) {
	l := newMemoryLimiter(1, 1)
	now := time.Now()
	l.buckets["stale"] = &bucket{tokens: 1, last: now.Add(-2 * time.Minute)}
	l.lastSweep = now.Add(-30 * time.Second) // < 1 min since last sweep

	l.sweep(now)

	if _, ok := l.buckets["stale"]; !ok {
		t.Error("sweep must not evict more than once a minute")
	}
}

// TestMemoryLimiterSweepRespectsRefillWindow exercises the branch where
// burst/rate exceeds the 1-minute floor, so maxIdle follows the refill window
// (here 120s) rather than 1 minute.
func TestMemoryLimiterSweepRespectsRefillWindow(t *testing.T) {
	l := newMemoryLimiter(1, 120) // full refill = 120s > 1m floor
	now := time.Now()
	l.buckets["recent"] = &bucket{tokens: 120, last: now.Add(-90 * time.Second)} // < 120s -> kept
	l.buckets["old"] = &bucket{tokens: 120, last: now.Add(-150 * time.Second)}   // > 120s -> dropped
	l.lastSweep = now.Add(-2 * time.Minute)

	l.sweep(now)

	if _, ok := l.buckets["recent"]; !ok {
		t.Error("a bucket within the refill window must be kept (maxIdle follows burst/rate)")
	}
	if _, ok := l.buckets["old"]; ok {
		t.Error("a bucket beyond the refill window must be swept")
	}
}
