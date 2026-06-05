package ratelimit

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestRedisDistributedLimit verifies that the Redis backend enforces a single
// shared limit across what would be separate crmkitd replicas. Skipped unless
// CRMKIT_TEST_REDIS_URL is set.
func TestRedisDistributedLimit(t *testing.T) {
	url := os.Getenv("CRMKIT_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set CRMKIT_TEST_REDIS_URL to run Redis integration tests")
	}

	// Two providers simulate two replicas sharing one Redis.
	p1, err := Open(BackendRedis, url)
	if err != nil {
		t.Fatalf("open redis 1: %v", err)
	}
	defer p1.Close()
	p2, err := Open(BackendRedis, url)
	if err != nil {
		t.Fatalf("open redis 2: %v", err)
	}
	defer p2.Close()

	key := fmt.Sprintf("itest-%d", time.Now().UnixNano())
	l1 := p1.Limiter("test", 1, 2) // 1 token/sec, burst 2
	l2 := p2.Limiter("test", 1, 2)

	// The burst of 2 is shared across the two instances, not 2-per-instance.
	if !l1.Allow(key) {
		t.Fatal("1st request (instance 1) should pass")
	}
	if !l2.Allow(key) {
		t.Fatal("2nd request (instance 2) should pass - shared burst of 2")
	}
	if l1.Allow(key) {
		t.Fatal("3rd request should be throttled - shared bucket is exhausted")
	}
	if l2.Allow(key) {
		t.Fatal("4th request (other instance) should also be throttled")
	}

	// A disabled limiter (rate 0) allows everything without touching Redis.
	ld := p1.Limiter("test", 0, 1)
	for i := 0; i < 5; i++ {
		if !ld.Allow(key) {
			t.Fatal("rate 0 should allow everything")
		}
	}
}
