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
