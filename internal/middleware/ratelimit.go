package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

// tokenBucketScript is an atomic Lua script executed on the Redis server.
// It reads bucket state, applies a token refill based on elapsed time, then
// either consumes one token (allowed) or rejects the request (denied) — all
// in a single round trip with no race conditions.
//
// KEYS[1]  = "ratelimit:{tenant_id}"
// ARGV[1]  = capacity   (max tokens, == RATE_LIMIT_RPM)
// ARGV[2]  = refill_rate (tokens per second == capacity / 60)
// ARGV[3]  = now        (current Unix time as float seconds)
// ARGV[4]  = cost       (tokens to consume, always 1)
//
// Returns: {1, 0}        — allowed
//
//	{0, wait_secs} — denied; wait_secs until next token is available
const tokenBucketScript = `
local key          = KEYS[1]
local capacity     = tonumber(ARGV[1])
local refill_rate  = tonumber(ARGV[2])
local now          = tonumber(ARGV[3])
local cost         = tonumber(ARGV[4])

local data       = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens     = tonumber(data[1])
local last_refill = tonumber(data[2])

-- First request for this tenant: start with a full bucket.
if tokens == nil then
    tokens     = capacity
    last_refill = now
end

-- Refill proportionally to elapsed time; cap at capacity.
-- math.max guards against negative elapsed caused by clock skew.
local elapsed = math.max(0, now - last_refill)
tokens = math.min(capacity, tokens + elapsed * refill_rate)

if tokens >= cost then
    tokens = tokens - cost
    redis.call('HSET', key, 'tokens', tokens, 'last_refill', now)
    redis.call('EXPIRE', key, 120)
    return {1, 0}
else
    -- Compute how many seconds until one token is available.
    local wait = math.ceil((cost - tokens) / refill_rate)
    redis.call('HSET', key, 'tokens', tokens, 'last_refill', now)
    redis.call('EXPIRE', key, 120)
    return {0, wait}
end
`

// RateLimiter holds a Redis client and the compiled Lua script.
type RateLimiter struct {
	client *redis.Client
	script *redis.Script
	rpm    int
}

// NewRateLimiter connects to Redis at redisURL, pings it, and returns a
// RateLimiter configured for rpm requests per minute per tenant.
func NewRateLimiter(redisURL string, rpm int) (*RateLimiter, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("rate limiter: parse redis URL: %w", err)
	}

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("rate limiter: ping redis: %w", err)
	}

	return &RateLimiter{
		client: client,
		script: redis.NewScript(tokenBucketScript),
		rpm:    rpm,
	}, nil
}

// Close releases the Redis connection.
func (rl *RateLimiter) Close() error { return rl.client.Close() }

// Middleware returns an HTTP middleware that enforces per-tenant rate limits.
// It must be applied after the auth middleware so that tenant_id is in context.
// On Redis errors it fails open rather than blocking all traffic.
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	capacity := float64(rl.rpm)
	refillRate := capacity / 60.0 // tokens per second

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID := GetTenantID(r.Context())
			if tenantID == "" {
				// Should not happen after auth middleware; pass through defensively.
				next.ServeHTTP(w, r)
				return
			}

			key := "ratelimit:" + tenantID
			now := float64(time.Now().UnixNano()) / 1e9

			res, err := rl.script.Run(r.Context(), rl.client,
				[]string{key},
				capacity, refillRate, now, 1,
			).Result()

			if err != nil {
				// Fail open: a Redis outage must not take down the gateway.
				log.Warn().Err(err).Str("tenant", tenantID).
					Msg("rate limit check failed — failing open")
				next.ServeHTTP(w, r)
				return
			}

			vals, ok := res.([]interface{})
			if !ok || len(vals) < 2 {
				log.Warn().Str("tenant", tenantID).
					Msg("unexpected rate limit response shape — failing open")
				next.ServeHTTP(w, r)
				return
			}

			allowed, _ := vals[0].(int64)
			retryAfter, _ := vals[1].(int64)

			if allowed != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": fmt.Sprintf("rate limit exceeded; retry after %d second(s)", retryAfter),
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
