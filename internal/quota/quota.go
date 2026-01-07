// Package quota provides usage quota management for the MCP gateway.
//
// Quotas enforce daily/monthly usage limits per tenant and user,
// supporting both soft limits (warnings) and hard limits (blocks).
package quota

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/billing"
)

// QuotaType represents the type of resource being limited.
type QuotaType string

const (
	// QuotaTypeRequests limits the total number of API requests
	QuotaTypeRequests QuotaType = "requests"
	// QuotaTypeTokensIn limits input tokens (for AI workloads)
	QuotaTypeTokensIn QuotaType = "tokens_in"
	// QuotaTypeTokensOut limits output tokens
	QuotaTypeTokensOut QuotaType = "tokens_out"
	// QuotaTypeToolCalls limits tool invocations
	QuotaTypeToolCalls QuotaType = "tool_calls"
)

// Period represents the quota reset period.
type Period string

const (
	PeriodDaily   Period = "daily"
	PeriodWeekly  Period = "weekly"
	PeriodMonthly Period = "monthly"
)

// Quota defines a usage limit for a tenant/user.
type Quota struct {
	TenantID  string
	UserID    string // Empty for tenant-level quotas
	Type      QuotaType
	Limit     int64
	SoftLimit int64 // Optional warning threshold
	Period    Period
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsUserQuota returns true if this is a user-specific quota.
func (q Quota) IsUserQuota() bool {
	return q.UserID != ""
}

// HasSoftLimit returns true if a soft limit is configured.
func (q Quota) HasSoftLimit() bool {
	return q.SoftLimit > 0
}

// Usage represents current usage against a quota.
type Usage struct {
	TenantID    string
	UserID      string
	Type        QuotaType
	PeriodStart time.Time
	PeriodEnd   time.Time
	Current     int64
	LastUpdated time.Time
}

// Remaining returns the remaining quota.
func (u Usage) Remaining(limit int64) int64 {
	remaining := limit - u.Current
	if remaining < 0 {
		return 0
	}
	return remaining
}

// PercentUsed returns the percentage of quota used.
func (u Usage) PercentUsed(limit int64) float64 {
	if limit == 0 {
		return 0
	}
	return float64(u.Current) / float64(limit) * 100
}

// CheckResult represents the outcome of a quota check.
type CheckResult struct {
	// Allowed indicates if the request should proceed
	Allowed bool
	// Current is the current usage count
	Current int64
	// Limit is the hard limit
	Limit int64
	// SoftLimit is the warning threshold (0 if not set)
	SoftLimit int64
	// Remaining is how much quota is left
	Remaining int64
	// PercentUsed is the usage percentage
	PercentUsed float64
	// ResetAt is when the quota period resets
	ResetAt time.Time
	// Warning indicates soft limit exceeded but not hard limit
	Warning bool
	// QuotaType is the type of quota checked
	QuotaType QuotaType
}

// Manager is the interface for quota management.
type Manager interface {
	// Check verifies if an operation is allowed and returns current status.
	Check(ctx context.Context, tenantID, userID string, quotaType QuotaType, cost int64) (CheckResult, error)
	// Increment adds to the usage counter.
	Increment(ctx context.Context, tenantID, userID string, quotaType QuotaType, amount int64) error
	// GetUsage returns current usage for a tenant/user.
	GetUsage(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Usage, error)
	// SetQuota creates or updates a quota.
	SetQuota(ctx context.Context, quota Quota) error
	// GetQuota retrieves a quota definition.
	GetQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Quota, error)
	// SetWebhookSender sets the billing webhook sender for quota events.
	SetWebhookSender(sender billing.WebhookSender)
	// Close releases resources.
	Close() error
}

// Store is the storage interface for quotas and usage.
type Store interface {
	// GetQuota retrieves a quota definition.
	GetQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Quota, error)
	// SetQuota creates or updates a quota.
	SetQuota(ctx context.Context, quota Quota) error
	// DeleteQuota removes a quota.
	DeleteQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) error
	// GetUsage retrieves usage for a period.
	GetUsage(ctx context.Context, tenantID, userID string, quotaType QuotaType, periodStart time.Time) (Usage, error)
	// IncrementUsage atomically increments usage.
	IncrementUsage(ctx context.Context, tenantID, userID string, quotaType QuotaType, amount int64, periodStart, periodEnd time.Time) (int64, error)
	// Close releases resources.
	Close() error
}

// Config holds quota configuration.
type Config struct {
	// Enabled controls whether quota enforcement is active
	Enabled bool

	// Store is the storage backend: "postgres" or "memory"
	Store string

	// PostgresURL is the connection string for Postgres store
	PostgresURL string

	// Default limits (used when no quota is defined)
	DefaultDailyRequests   int64
	DefaultMonthlyRequests int64

	// EnforceHard blocks requests when hard limit exceeded
	EnforceHard bool
	// EnforceSoft logs warnings when soft limit exceeded
	EnforceSoft bool
}

// LoadConfigFromEnv loads quota configuration from environment variables.
func LoadConfigFromEnv() Config {
	return Config{
		Enabled:                envBoolDefault("FI_MCP_QUOTA_ENABLED", false),
		Store:                  envDefault("FI_MCP_QUOTA_STORE", "memory"),
		PostgresURL:            os.Getenv("FI_MCP_QUOTA_POSTGRES_URL"),
		DefaultDailyRequests:   envInt64Default("FI_MCP_QUOTA_DAILY_REQUESTS", 10000),
		DefaultMonthlyRequests: envInt64Default("FI_MCP_QUOTA_MONTHLY_REQUESTS", 100000),
		EnforceHard:            envBoolDefault("FI_MCP_QUOTA_ENFORCE_HARD", true),
		EnforceSoft:            envBoolDefault("FI_MCP_QUOTA_ENFORCE_SOFT", true),
	}
}

// Common errors
var (
	ErrQuotaExceeded    = errors.New("quota exceeded")
	ErrQuotaNotFound    = errors.New("quota not found")
	ErrInvalidQuotaType = errors.New("invalid quota type")
)

// PeriodBounds returns the start and end times for a period.
func PeriodBounds(p Period, now time.Time) (start, end time.Time) {
	// Use UTC for consistent period boundaries
	now = now.UTC()

	switch p {
	case PeriodDaily:
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 0, 1)
	case PeriodWeekly:
		// Week starts on Monday
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday becomes 7
		}
		start = time.Date(now.Year(), now.Month(), now.Day()-weekday+1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 0, 7)
	case PeriodMonthly:
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)
	default:
		// Default to daily
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 0, 1)
	}

	return start, end
}

// TimeUntilReset returns the duration until the next period reset.
func TimeUntilReset(p Period, now time.Time) time.Duration {
	_, end := PeriodBounds(p, now)
	return end.Sub(now.UTC())
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

func envInt64Default(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// FormatUsage returns a human-readable usage string.
func FormatUsage(current, limit int64) string {
	percent := float64(current) / float64(limit) * 100
	return fmt.Sprintf("%d/%d (%.1f%%)", current, limit, percent)
}
