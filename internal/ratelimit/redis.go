package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// tokenBucketScript is a Lua script that implements an atomic token bucket.
// It returns: [allowed (0/1), remaining tokens, reset_at_unix_ms]
const tokenBucketScript = `
local key = KEYS[1]
local burst = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])
local window_ms = tonumber(ARGV[5])

-- Get current bucket state
local bucket = redis.call('HMGET', key, 'tokens', 'last_update')
local tokens = tonumber(bucket[1])
local last_update = tonumber(bucket[2])

-- Initialize bucket if it doesn't exist
if tokens == nil then
    tokens = burst
    last_update = now
end

-- Calculate token refill
local elapsed_ms = now - last_update
local refill = (elapsed_ms / 1000.0) * rate
tokens = math.min(burst, tokens + refill)

-- Try to consume tokens
local allowed = 0
if tokens >= requested then
    tokens = tokens - requested
    allowed = 1
end

-- Update bucket state
redis.call('HMSET', key, 'tokens', tokens, 'last_update', now)
redis.call('PEXPIRE', key, window_ms * 2)

-- Calculate reset time (when bucket would be full)
local reset_at = now + window_ms

return {allowed, math.floor(tokens), reset_at}
`

// peekScript checks the current state without consuming tokens.
const peekScript = `
local key = KEYS[1]
local burst = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local window_ms = tonumber(ARGV[4])

-- Get current bucket state
local bucket = redis.call('HMGET', key, 'tokens', 'last_update')
local tokens = tonumber(bucket[1])
local last_update = tonumber(bucket[2])

-- Initialize bucket if it doesn't exist
if tokens == nil then
    tokens = burst
    last_update = now
end

-- Calculate token refill (without updating)
local elapsed_ms = now - last_update
local refill = (elapsed_ms / 1000.0) * rate
tokens = math.min(burst, tokens + refill)

local allowed = 0
if tokens >= 1 then
    allowed = 1
end

local reset_at = now + window_ms

return {allowed, math.floor(tokens), reset_at}
`

// RedisStore implements Store using Redis for distributed rate limiting.
type RedisStore struct {
	client *redis.Client

	// Script SHAs for faster execution
	takeScriptSHA string
	peekScriptSHA string

	// Key prefix for namespacing
	keyPrefix string
}

// RedisStoreOption configures a RedisStore.
type RedisStoreOption func(*RedisStore)

// WithKeyPrefix sets a prefix for all Redis keys.
func WithKeyPrefix(prefix string) RedisStoreOption {
	return func(s *RedisStore) {
		s.keyPrefix = prefix
	}
}

// NewRedisStore creates a new Redis-backed rate limit store.
func NewRedisStore(client *redis.Client, opts ...RedisStoreOption) (*RedisStore, error) {
	s := &RedisStore{
		client:    client,
		keyPrefix: "ratelimit:",
	}

	for _, opt := range opts {
		opt(s)
	}

	// Preload scripts
	ctx := context.Background()
	var err error

	s.takeScriptSHA, err = client.ScriptLoad(ctx, tokenBucketScript).Result()
	if err != nil {
		return nil, fmt.Errorf("load take script: %w", err)
	}

	s.peekScriptSHA, err = client.ScriptLoad(ctx, peekScript).Result()
	if err != nil {
		return nil, fmt.Errorf("load peek script: %w", err)
	}

	return s, nil
}

// Take consumes n tokens from the bucket.
func (s *RedisStore) Take(ctx context.Context, key string, limit Limit, n int) (Result, error) {
	fullKey := s.keyPrefix + key

	nowMs := time.Now().UnixMilli()
	burst := limit.EffectiveBurst()
	rate := limit.RequestsPerSecond()
	windowMs := limit.Window.Milliseconds()

	// Try EvalSha first (faster), fall back to Eval if script not cached
	result, err := s.client.EvalSha(ctx, s.takeScriptSHA, []string{fullKey},
		burst, rate, nowMs, n, windowMs,
	).Slice()

	if err != nil {
		// Script might not be cached on this shard, try full eval
		if isNoScriptErr(err) {
			result, err = s.client.Eval(ctx, tokenBucketScript, []string{fullKey},
				burst, rate, nowMs, n, windowMs,
			).Slice()
		}
		if err != nil {
			return Result{}, fmt.Errorf("redis eval: %w", err)
		}
	}

	return s.parseResult(result, limit)
}

// Peek returns the current state without consuming tokens.
func (s *RedisStore) Peek(ctx context.Context, key string, limit Limit) (Result, error) {
	fullKey := s.keyPrefix + key

	nowMs := time.Now().UnixMilli()
	burst := limit.EffectiveBurst()
	rate := limit.RequestsPerSecond()
	windowMs := limit.Window.Milliseconds()

	result, err := s.client.EvalSha(ctx, s.peekScriptSHA, []string{fullKey},
		burst, rate, nowMs, windowMs,
	).Slice()

	if err != nil {
		if isNoScriptErr(err) {
			result, err = s.client.Eval(ctx, peekScript, []string{fullKey},
				burst, rate, nowMs, windowMs,
			).Slice()
		}
		if err != nil {
			return Result{}, fmt.Errorf("redis eval: %w", err)
		}
	}

	return s.parseResult(result, limit)
}

// Reset clears the rate limit state for a key.
func (s *RedisStore) Reset(ctx context.Context, key string) error {
	fullKey := s.keyPrefix + key
	return s.client.Del(ctx, fullKey).Err()
}

// Close releases resources. The underlying Redis client is not closed.
func (s *RedisStore) Close() error {
	return nil
}

func (s *RedisStore) parseResult(result []any, limit Limit) (Result, error) {
	if len(result) < 3 {
		return Result{}, fmt.Errorf("unexpected result length: %d", len(result))
	}

	allowed, err := toInt64(result[0])
	if err != nil {
		return Result{}, fmt.Errorf("parse allowed: %w", err)
	}

	remaining, err := toInt64(result[1])
	if err != nil {
		return Result{}, fmt.Errorf("parse remaining: %w", err)
	}

	resetAtMs, err := toInt64(result[2])
	if err != nil {
		return Result{}, fmt.Errorf("parse reset_at: %w", err)
	}

	r := Result{
		Allowed:   allowed == 1,
		Remaining: int(remaining),
		ResetAt:   time.UnixMilli(resetAtMs),
		Limit:     limit,
	}

	if !r.Allowed && remaining < int64(limit.EffectiveBurst()) {
		// Calculate retry-after based on tokens needed
		tokensNeeded := 1 - float64(remaining)
		if tokensNeeded > 0 {
			rate := limit.RequestsPerSecond()
			if rate > 0 {
				r.RetryAfter = time.Duration(tokensNeeded/rate*1000) * time.Millisecond
			}
		}
	}

	return r, nil
}

func toInt64(v any) (int64, error) {
	switch val := v.(type) {
	case int64:
		return val, nil
	case int:
		return int64(val), nil
	case float64:
		return int64(val), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
}

func isNoScriptErr(err error) bool {
	return err != nil && err.Error() == "NOSCRIPT No matching script. Please use EVAL."
}
