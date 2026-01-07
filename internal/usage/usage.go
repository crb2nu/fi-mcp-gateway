// Package usage provides usage analytics tracking for the MCP gateway.
//
// Usage events are collected asynchronously and can be queried or exported
// for billing and analytics purposes.
package usage

import (
	"context"
	"os"
	"strings"
	"time"
)

// Event represents a single usage event.
type Event struct {
	ID        string            `json:"id"`
	Timestamp time.Time         `json:"timestamp"`
	TenantID  string            `json:"tenant_id"`
	UserID    string            `json:"user_id,omitempty"`
	ToolName  string            `json:"tool_name,omitempty"`
	ServerID  string            `json:"server_id,omitempty"`
	Duration  time.Duration     `json:"duration_ms"`
	TokensIn  int64             `json:"tokens_in,omitempty"`
	TokensOut int64             `json:"tokens_out,omitempty"`
	Success   bool              `json:"success"`
	ErrorCode string            `json:"error_code,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Summary aggregates usage statistics over a time period.
type Summary struct {
	TenantID       string           `json:"tenant_id"`
	UserID         string           `json:"user_id,omitempty"`
	PeriodStart    time.Time        `json:"period_start"`
	PeriodEnd      time.Time        `json:"period_end"`
	TotalEvents    int64            `json:"total_events"`
	SuccessCount   int64            `json:"success_count"`
	ErrorCount     int64            `json:"error_count"`
	TotalTokensIn  int64            `json:"total_tokens_in"`
	TotalTokensOut int64            `json:"total_tokens_out"`
	TotalDuration  time.Duration    `json:"total_duration_ms"`
	AvgDuration    time.Duration    `json:"avg_duration_ms"`
	ToolBreakdown  map[string]int64 `json:"tool_breakdown,omitempty"`
}

// QueryParams defines filters for querying usage data.
type QueryParams struct {
	TenantID  string
	UserID    string
	ToolName  string
	StartTime time.Time
	EndTime   time.Time
	Limit     int
	Offset    int
}

// Config holds usage tracking configuration.
type Config struct {
	// Enabled controls whether usage tracking is active
	Enabled bool
	// Store is the storage backend: "memory" or "postgres"
	Store string
	// PostgresURL is the connection string for Postgres store
	PostgresURL string
	// BufferSize is the event buffer size before flushing
	BufferSize int
	// FlushInterval is how often to flush buffered events
	FlushInterval time.Duration
	// RetentionDays is how long to keep usage data (0 = forever)
	RetentionDays int
	// WebhookURL is the billing webhook endpoint
	WebhookURL string
	// WebhookSecret is used for signing webhook payloads
	WebhookSecret string
}

// LoadConfigFromEnv loads usage configuration from environment variables.
func LoadConfigFromEnv() Config {
	return Config{
		Enabled:       envBoolDefault("FI_MCP_USAGE_ENABLED", false),
		Store:         envDefault("FI_MCP_USAGE_STORE", "memory"),
		PostgresURL:   os.Getenv("FI_MCP_USAGE_POSTGRES_URL"),
		BufferSize:    envIntDefault("FI_MCP_USAGE_BUFFER_SIZE", 100),
		FlushInterval: envDurationDefault("FI_MCP_USAGE_FLUSH_INTERVAL", 5*time.Second),
		RetentionDays: envIntDefault("FI_MCP_USAGE_RETENTION_DAYS", 90),
		WebhookURL:    os.Getenv("FI_MCP_BILLING_WEBHOOK_URL"),
		WebhookSecret: os.Getenv("FI_MCP_BILLING_WEBHOOK_SECRET"),
	}
}

// Tracker is the interface for usage tracking.
type Tracker interface {
	// Track records a usage event asynchronously.
	Track(ctx context.Context, event Event)
	// Query retrieves usage events matching the given params.
	Query(ctx context.Context, params QueryParams) ([]Event, error)
	// GetSummary returns aggregated usage statistics.
	GetSummary(ctx context.Context, tenantID, userID string, start, end time.Time) (Summary, error)
	// Close shuts down the tracker and flushes pending events.
	Close() error
}

// Store is the storage interface for usage events.
type Store interface {
	// Store saves events to the backend.
	Store(ctx context.Context, events []Event) error
	// Query retrieves events matching the params.
	Query(ctx context.Context, params QueryParams) ([]Event, error)
	// GetSummary returns aggregated statistics.
	GetSummary(ctx context.Context, tenantID, userID string, start, end time.Time) (Summary, error)
	// DeleteOlderThan removes events older than the given time.
	DeleteOlderThan(ctx context.Context, before time.Time) (int64, error)
	// Close releases resources.
	Close() error
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBoolDefault(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "true" || v == "1" || v == "yes"
}

func envIntDefault(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	var n int
	for _, c := range v {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			return fallback
		}
	}
	return n
}

func envDurationDefault(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return fallback
	}
	return d
}
