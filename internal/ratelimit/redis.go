package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// tokenBucketScript is an atomic token-bucket evaluated server-side in Redis so
// the limit is enforced consistently across all crmkitd replicas sharing the
// same Redis. It refills based on elapsed time, consumes one token if available,
// and sets a TTL so idle buckets expire (bounded memory).
//
//	KEYS[1] = bucket key
//	ARGV[1] = rate (tokens/sec)   ARGV[2] = burst (capacity)
//	ARGV[3] = now (unix millis)
//
// Returns 1 if allowed, 0 if throttled.
var tokenBucketScript = redis.NewScript(`
local key   = KEYS[1]
local rate  = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now   = tonumber(ARGV[3])

local data   = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])
if tokens == nil then
  tokens = burst
  ts = now
end

local delta = math.max(0, now - ts) / 1000.0
tokens = math.min(burst, tokens + delta * rate)

local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
local ttl = 60
if rate > 0 then
  ttl = math.ceil(burst / rate) + 1
end
redis.call('EXPIRE', key, ttl)
return allowed
`)

// openRedis connects to Redis and returns a Provider whose limiters share the
// client. dsn is a redis:// URL (redis.ParseURL format).
func openRedis(dsn string) (Provider, error) {
	opt, err := redis.ParseURL(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse redis dsn: %w", err)
	}
	client := redis.NewClient(opt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	return &redisProvider{client: client}, nil
}

type redisProvider struct {
	client *redis.Client
}

func (p *redisProvider) Limiter(name string, rate float64, burst int) Limiter {
	if burst < 1 {
		burst = 1
	}
	return &redisLimiter{
		client: p.client,
		prefix: "crmkit:rl:" + name + ":",
		rate:   rate,
		burst:  burst,
	}
}

func (p *redisProvider) Close() error { return p.client.Close() }

type redisLimiter struct {
	client *redis.Client
	prefix string
	rate   float64
	burst  int
}

// Allow runs the token-bucket script in Redis. A rate <= 0 disables limiting.
// On a Redis error it fails open (allows the request) and logs, so a Redis blip
// degrades to "no limiting" rather than rejecting all traffic.
func (l *redisLimiter) Allow(key string) bool {
	if l.rate <= 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	now := time.Now().UnixMilli()
	res, err := tokenBucketScript.Run(ctx, l.client,
		[]string{l.prefix + key}, l.rate, l.burst, now).Int()
	if err != nil {
		slog.Warn("rate limiter degraded: redis error, allowing request", slog.String("error", err.Error()))
		return true
	}
	return res == 1
}
