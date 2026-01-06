// Package ratelimit provides rate limiting for the MCP gateway.
//
// It implements a token bucket algorithm with support for hierarchical limits
// (global, tenant, user, tool) and multiple storage backends (memory, Redis).
package ratelimit

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Key identifies the scope of a rate limit.
// Multiple fields can be set to create hierarchical limits.
type Key struct {
	// Tenant is the tenant/organization identifier
	Tenant string
	// User is the user/subject identifier
	User string
	// Team is an optional team identifier within a tenant
	Team string
	// Tool is the MCP tool name (for tool-specific limits)
	Tool string
	// Scope is an arbitrary scope identifier (e.g., "global", "api", "ws")
	Scope string
}

// String returns a canonical string representation of the key for storage.
func (k Key) String() string {
	parts := make([]string, 0, 5)
	if k.Scope != "" {
		parts = append(parts, "s:"+k.Scope)
	}
	if k.Tenant != "" {
		parts = append(parts, "t:"+k.Tenant)
	}
	if k.Team != "" {
		parts = append(parts, "tm:"+k.Team)
	}
	if k.User != "" {
		parts = append(parts, "u:"+k.User)
	}
	if k.Tool != "" {
		parts = append(parts, "tl:"+k.Tool)
	}
	if len(parts) == 0 {
		return "global"
	}
	return strings.Join(parts, ":")
}

// Limit defines rate limiting parameters.
type Limit struct {
	// Requests is the maximum number of requests allowed in the window
	Requests int
	// Window is the time window for the limit
	Window time.Duration
	// Burst is the maximum burst size (defaults to Requests if 0)
	Burst int
}

// RequestsPerSecond returns the rate as requests per second.
func (l Limit) RequestsPerSecond() float64 {
	if l.Window == 0 {
		return 0
	}
	return float64(l.Requests) / l.Window.Seconds()
}

// EffectiveBurst returns the burst size, defaulting to Requests if not set.
func (l Limit) EffectiveBurst() int {
	if l.Burst > 0 {
		return l.Burst
	}
	return l.Requests
}

// Result represents the outcome of a rate limit check.
type Result struct {
	// Allowed indicates whether the request should proceed
	Allowed bool
	// Remaining is the number of requests remaining in the current window
	Remaining int
	// ResetAt is when the limit will reset
	ResetAt time.Time
	// RetryAfter is how long to wait before retrying (only set if not allowed)
	RetryAfter time.Duration
	// Limit is the limit that was applied
	Limit Limit
}

// Limiter is the main rate limiter interface.
type Limiter interface {
	// Allow checks if a request should be allowed and consumes one token.
	Allow(ctx context.Context, key Key) (Result, error)
	// AllowN checks if n requests should be allowed and consumes n tokens.
	AllowN(ctx context.Context, key Key, n int) (Result, error)
	// Peek checks the current state without consuming tokens.
	Peek(ctx context.Context, key Key) (Result, error)
	// Reset clears the rate limit state for a key.
	Reset(ctx context.Context, key Key) error
}

// Store is the storage backend interface for rate limit state.
type Store interface {
	// Take attempts to consume n tokens from the bucket for the given key.
	// Returns the result of the operation.
	Take(ctx context.Context, key string, limit Limit, n int) (Result, error)
	// Peek returns the current state without consuming tokens.
	Peek(ctx context.Context, key string, limit Limit) (Result, error)
	// Reset clears the state for the given key.
	Reset(ctx context.Context, key string) error
	// Close releases resources.
	Close() error
}

// Config holds rate limiter configuration.
type Config struct {
	// Store is the storage backend type: "memory" or "redis"
	Store string

	// Global limits (apply to all requests)
	GlobalRPS   int
	GlobalBurst int

	// Tenant limits (apply per tenant)
	TenantRPS   int
	TenantBurst int

	// User limits (apply per user within tenant)
	UserRPS   int
	UserBurst int

	// Tool limits (apply per tool call)
	ToolRPS   int
	ToolBurst int

	// Default window for sliding window limits
	Window time.Duration

	// Redis configuration (when Store = "redis")
	RedisURL string

	// Enabled controls whether rate limiting is active
	Enabled bool
}

// LoadConfigFromEnv loads rate limit configuration from environment variables.
func LoadConfigFromEnv() Config {
	cfg := Config{
		Store:       envDefault("FI_MCP_RATELIMIT_STORE", "memory"),
		GlobalRPS:   envIntDefault("FI_MCP_RATELIMIT_GLOBAL_RPS", 1000),
		GlobalBurst: envIntDefault("FI_MCP_RATELIMIT_GLOBAL_BURST", 0),
		TenantRPS:   envIntDefault("FI_MCP_RATELIMIT_TENANT_RPS", 500),
		TenantBurst: envIntDefault("FI_MCP_RATELIMIT_TENANT_BURST", 0),
		UserRPS:     envIntDefault("FI_MCP_RATELIMIT_USER_RPS", 100),
		UserBurst:   envIntDefault("FI_MCP_RATELIMIT_USER_BURST", 0),
		ToolRPS:     envIntDefault("FI_MCP_RATELIMIT_TOOL_RPS", 50),
		ToolBurst:   envIntDefault("FI_MCP_RATELIMIT_TOOL_BURST", 0),
		Window:      envDurationDefault("FI_MCP_RATELIMIT_WINDOW", time.Second),
		RedisURL:    os.Getenv("FI_MCP_RATELIMIT_REDIS_URL"),
		Enabled:     envBoolDefault("FI_MCP_RATELIMIT_ENABLED", false),
	}

	return cfg
}

// GlobalLimit returns the configured global limit.
func (c Config) GlobalLimit() Limit {
	return Limit{
		Requests: c.GlobalRPS,
		Window:   c.Window,
		Burst:    c.GlobalBurst,
	}
}

// TenantLimit returns the configured tenant limit.
func (c Config) TenantLimit() Limit {
	return Limit{
		Requests: c.TenantRPS,
		Window:   c.Window,
		Burst:    c.TenantBurst,
	}
}

// UserLimit returns the configured user limit.
func (c Config) UserLimit() Limit {
	return Limit{
		Requests: c.UserRPS,
		Window:   c.Window,
		Burst:    c.UserBurst,
	}
}

// ToolLimit returns the configured tool limit.
func (c Config) ToolLimit() Limit {
	return Limit{
		Requests: c.ToolRPS,
		Window:   c.Window,
		Burst:    c.ToolBurst,
	}
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntDefault(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func envBoolDefault(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "true" || v == "1" || v == "yes"
}

func envDurationDefault(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// ErrRateLimited is returned when a request is rate limited.
var ErrRateLimited = fmt.Errorf("rate limited")
