package ratelimit

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/logger"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/storage"
)

// HierarchicalLimiter applies multiple rate limits in order.
// It checks limits from most specific to least specific,
// failing fast if any limit is exceeded.
type HierarchicalLimiter struct {
	store   Store
	cfg     Config
	enabled bool
}

// New creates a new rate limiter from configuration.
func New(cfg Config) (*HierarchicalLimiter, error) {
	if !cfg.Enabled {
		return &HierarchicalLimiter{
			cfg:     cfg,
			enabled: false,
		}, nil
	}

	var store Store
	var err error

	switch cfg.Store {
	case "redis":
		store, err = newRedisStoreFromConfig(cfg)
	case "memory", "":
		store = NewMemoryStore()
	default:
		return nil, fmt.Errorf("unknown rate limit store: %s", cfg.Store)
	}

	if err != nil {
		return nil, err
	}

	return &HierarchicalLimiter{
		store:   store,
		cfg:     cfg,
		enabled: true,
	}, nil
}

func newRedisStoreFromConfig(cfg Config) (*RedisStore, error) {
	redisCfg := storage.RedisConfig{
		URL: cfg.RedisURL,
	}

	redis, err := storage.NewRedis(redisCfg)
	if err != nil {
		return nil, fmt.Errorf("create redis client: %w", err)
	}

	ctx := context.Background()
	if err := redis.Ping(ctx); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	return NewRedisStore(redis.Client())
}

// Allow checks if a request is allowed, consuming one token if so.
func (l *HierarchicalLimiter) Allow(ctx context.Context, key Key) (Result, error) {
	return l.AllowN(ctx, key, 1)
}

// AllowN checks if n requests are allowed, consuming n tokens if so.
func (l *HierarchicalLimiter) AllowN(ctx context.Context, key Key, n int) (Result, error) {
	if !l.enabled {
		return Result{Allowed: true}, nil
	}

	// Build the hierarchy of limits to check
	// Order: tool -> user -> tenant -> global (most specific first)
	checks := l.buildLimitChecks(key)

	// Check each limit in order
	for _, check := range checks {
		result, err := l.store.Take(ctx, check.key, check.limit, n)
		if err != nil {
			// Log error but don't fail the request
			logger.Error("rate limit store error", "error", err, "key", check.key)
			continue
		}

		if !result.Allowed {
			return result, ErrRateLimited
		}
	}

	// All limits passed
	return Result{Allowed: true}, nil
}

// Peek checks the current rate limit state without consuming tokens.
func (l *HierarchicalLimiter) Peek(ctx context.Context, key Key) (Result, error) {
	if !l.enabled {
		return Result{Allowed: true}, nil
	}

	checks := l.buildLimitChecks(key)

	// Return the most restrictive limit
	var worstResult Result
	worstResult.Allowed = true

	for _, check := range checks {
		result, err := l.store.Peek(ctx, check.key, check.limit)
		if err != nil {
			continue
		}

		if !result.Allowed {
			return result, nil
		}

		if result.Remaining < worstResult.Remaining || !worstResult.Allowed {
			worstResult = result
		}
	}

	return worstResult, nil
}

// Reset clears all rate limit state for a key.
func (l *HierarchicalLimiter) Reset(ctx context.Context, key Key) error {
	if !l.enabled || l.store == nil {
		return nil
	}
	return l.store.Reset(ctx, key.String())
}

// Close releases resources.
func (l *HierarchicalLimiter) Close() error {
	if l.store != nil {
		return l.store.Close()
	}
	return nil
}

// Enabled returns whether rate limiting is active.
func (l *HierarchicalLimiter) Enabled() bool {
	return l.enabled
}

type limitCheck struct {
	key   string
	limit Limit
}

func (l *HierarchicalLimiter) buildLimitChecks(key Key) []limitCheck {
	var checks []limitCheck

	// Tool-level limit (most specific)
	if key.Tool != "" && l.cfg.ToolRPS > 0 {
		toolKey := Key{
			Scope:  key.Scope,
			Tenant: key.Tenant,
			User:   key.User,
			Tool:   key.Tool,
		}
		checks = append(checks, limitCheck{
			key:   toolKey.String(),
			limit: l.cfg.ToolLimit(),
		})
	}

	// User-level limit
	if key.User != "" && l.cfg.UserRPS > 0 {
		userKey := Key{
			Scope:  key.Scope,
			Tenant: key.Tenant,
			User:   key.User,
		}
		checks = append(checks, limitCheck{
			key:   userKey.String(),
			limit: l.cfg.UserLimit(),
		})
	}

	// Tenant-level limit
	if key.Tenant != "" && l.cfg.TenantRPS > 0 {
		tenantKey := Key{
			Scope:  key.Scope,
			Tenant: key.Tenant,
		}
		checks = append(checks, limitCheck{
			key:   tenantKey.String(),
			limit: l.cfg.TenantLimit(),
		})
	}

	// Global limit (least specific)
	if l.cfg.GlobalRPS > 0 {
		globalKey := Key{Scope: "global"}
		checks = append(checks, limitCheck{
			key:   globalKey.String(),
			limit: l.cfg.GlobalLimit(),
		})
	}

	return checks
}

// NoopLimiter is a rate limiter that always allows requests.
type NoopLimiter struct{}

// NewNoopLimiter creates a limiter that does nothing.
func NewNoopLimiter() *NoopLimiter {
	return &NoopLimiter{}
}

// Allow always allows the request.
func (NoopLimiter) Allow(ctx context.Context, key Key) (Result, error) {
	return Result{Allowed: true}, nil
}

// AllowN always allows the request.
func (NoopLimiter) AllowN(ctx context.Context, key Key, n int) (Result, error) {
	return Result{Allowed: true}, nil
}

// Peek always returns full capacity.
func (NoopLimiter) Peek(ctx context.Context, key Key) (Result, error) {
	return Result{Allowed: true}, nil
}

// Reset does nothing.
func (NoopLimiter) Reset(ctx context.Context, key Key) error {
	return nil
}

// NewFromRedisClient creates a rate limiter using an existing Redis client.
func NewFromRedisClient(client *redis.Client, cfg Config) (*HierarchicalLimiter, error) {
	if !cfg.Enabled {
		return &HierarchicalLimiter{
			cfg:     cfg,
			enabled: false,
		}, nil
	}

	store, err := NewRedisStore(client)
	if err != nil {
		return nil, err
	}

	return &HierarchicalLimiter{
		store:   store,
		cfg:     cfg,
		enabled: true,
	}, nil
}
