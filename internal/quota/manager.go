package quota

import (
	"context"
	"fmt"
	"time"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/billing"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/logger"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/metrics"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/storage"
)

// DefaultManager implements the Manager interface.
type DefaultManager struct {
	store         Store
	cfg           Config
	enabled       bool
	webhookSender billing.WebhookSender
}

// New creates a new quota manager from configuration.
func New(cfg Config) (*DefaultManager, error) {
	if !cfg.Enabled {
		return &DefaultManager{
			cfg:     cfg,
			enabled: false,
		}, nil
	}

	var store Store
	var err error

	switch cfg.Store {
	case "postgres":
		store, err = newPostgresStoreFromConfig(cfg)
	case "memory", "":
		store = NewMemoryStore()
	default:
		return nil, fmt.Errorf("unknown quota store: %s", cfg.Store)
	}

	if err != nil {
		return nil, err
	}

	return &DefaultManager{
		store:   store,
		cfg:     cfg,
		enabled: true,
	}, nil
}

func newPostgresStoreFromConfig(cfg Config) (*PostgresStore, error) {
	pgCfg := storage.PostgresConfig{
		URL: cfg.PostgresURL,
	}

	pg, err := storage.NewPostgres(pgCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres client: %w", err)
	}

	ctx := context.Background()
	if err := pg.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	// Run migrations
	if err := pg.MigrateQuotaSchema(ctx); err != nil {
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return NewPostgresStore(pg), nil
}

// Check verifies if an operation is allowed.
func (m *DefaultManager) Check(ctx context.Context, tenantID, userID string, quotaType QuotaType, cost int64) (CheckResult, error) {
	if !m.enabled {
		return CheckResult{Allowed: true}, nil
	}

	// Get quota definition - try user-specific first, then tenant-level
	quota, err := m.getEffectiveQuota(ctx, tenantID, userID, quotaType)
	if err != nil {
		// No quota defined - use defaults
		quota = m.defaultQuota(tenantID, quotaType)
	}

	// Get current period bounds
	periodStart, periodEnd := PeriodBounds(quota.Period, time.Now())

	// Get current usage
	usage, err := m.store.GetUsage(ctx, tenantID, userID, quotaType, periodStart)
	if err != nil {
		logger.Error("quota usage lookup failed",
			"error", err,
			"tenant", tenantID,
			"user", userID,
			"type", string(quotaType))
		// On error, allow but log
		return CheckResult{Allowed: true}, nil
	}

	// Calculate result
	newUsage := usage.Current + cost
	result := CheckResult{
		Allowed:     true,
		Current:     usage.Current,
		Limit:       quota.Limit,
		SoftLimit:   quota.SoftLimit,
		Remaining:   usage.Remaining(quota.Limit),
		PercentUsed: usage.PercentUsed(quota.Limit),
		ResetAt:     periodEnd,
		QuotaType:   quotaType,
	}

	// Check soft limit
	if quota.HasSoftLimit() && newUsage > quota.SoftLimit {
		result.Warning = true
		if m.cfg.EnforceSoft {
			logger.Warn("quota soft limit exceeded",
				"tenant", tenantID,
				"user", userID,
				"type", string(quotaType),
				"current", usage.Current,
				"soft_limit", quota.SoftLimit)
		}
		// Send webhook for soft limit warning
		m.sendWebhook(billing.EventQuotaWarning, tenantID, userID, map[string]any{
			"quota_type": string(quotaType),
			"current":    usage.Current,
			"soft_limit": quota.SoftLimit,
			"hard_limit": quota.Limit,
			"reset_at":   periodEnd,
		})
	}

	// Check hard limit
	if newUsage > quota.Limit {
		result.Allowed = false
		// Send webhook for hard limit exceeded
		m.sendWebhook(billing.EventQuotaExceeded, tenantID, userID, map[string]any{
			"quota_type": string(quotaType),
			"current":    usage.Current,
			"limit":      quota.Limit,
			"reset_at":   periodEnd,
		})
		if m.cfg.EnforceHard {
			metrics.QuotaExceededTotal.WithLabelValues(tenantID, string(quotaType)).Inc()
			return result, ErrQuotaExceeded
		}
		// Log but don't block if enforcement is disabled
		logger.Warn("quota hard limit exceeded (not enforced)",
			"tenant", tenantID,
			"user", userID,
			"type", string(quotaType),
			"current", usage.Current,
			"limit", quota.Limit)
		result.Allowed = true
	}

	return result, nil
}

// Increment adds to the usage counter.
func (m *DefaultManager) Increment(ctx context.Context, tenantID, userID string, quotaType QuotaType, amount int64) error {
	if !m.enabled {
		return nil
	}

	// Get quota to determine period
	quota, err := m.getEffectiveQuota(ctx, tenantID, userID, quotaType)
	if err != nil {
		quota = m.defaultQuota(tenantID, quotaType)
	}

	periodStart, periodEnd := PeriodBounds(quota.Period, time.Now())

	newUsage, err := m.store.IncrementUsage(ctx, tenantID, userID, quotaType, amount, periodStart, periodEnd)
	if err != nil {
		return err
	}

	// Update metrics
	metrics.QuotaUsageGauge.WithLabelValues(tenantID, string(quotaType)).Set(float64(newUsage))

	return nil
}

// GetUsage returns current usage for a tenant/user.
func (m *DefaultManager) GetUsage(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Usage, error) {
	if !m.enabled {
		return Usage{}, nil
	}

	quota, err := m.getEffectiveQuota(ctx, tenantID, userID, quotaType)
	if err != nil {
		quota = m.defaultQuota(tenantID, quotaType)
	}

	periodStart, _ := PeriodBounds(quota.Period, time.Now())
	return m.store.GetUsage(ctx, tenantID, userID, quotaType, periodStart)
}

// SetQuota creates or updates a quota.
func (m *DefaultManager) SetQuota(ctx context.Context, quota Quota) error {
	if m.store == nil {
		return fmt.Errorf("quota store not initialized")
	}
	return m.store.SetQuota(ctx, quota)
}

// GetQuota retrieves a quota definition.
func (m *DefaultManager) GetQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Quota, error) {
	if m.store == nil {
		return Quota{}, ErrQuotaNotFound
	}
	return m.store.GetQuota(ctx, tenantID, userID, quotaType)
}

// Close releases resources.
func (m *DefaultManager) Close() error {
	if m.store != nil {
		return m.store.Close()
	}
	return nil
}

// Enabled returns whether quota management is active.
func (m *DefaultManager) Enabled() bool {
	return m.enabled
}

// SetWebhookSender sets the billing webhook sender for quota events.
func (m *DefaultManager) SetWebhookSender(sender billing.WebhookSender) {
	m.webhookSender = sender
}

// sendWebhook sends a billing webhook event asynchronously.
func (m *DefaultManager) sendWebhook(eventType billing.EventType, tenantID, userID string, data map[string]any) {
	if m.webhookSender == nil {
		return
	}
	event := billing.Event{
		Type:     eventType,
		TenantID: tenantID,
		UserID:   userID,
		Data:     data,
	}
	m.webhookSender.SendAsync(event)
}

// getEffectiveQuota finds the applicable quota, checking user-level first.
func (m *DefaultManager) getEffectiveQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Quota, error) {
	// Try user-specific quota first
	if userID != "" {
		quota, err := m.store.GetQuota(ctx, tenantID, userID, quotaType)
		if err == nil {
			return quota, nil
		}
	}

	// Fall back to tenant-level quota
	return m.store.GetQuota(ctx, tenantID, "", quotaType)
}

// defaultQuota returns a default quota when none is defined.
func (m *DefaultManager) defaultQuota(tenantID string, quotaType QuotaType) Quota {
	limit := m.cfg.DefaultDailyRequests
	period := PeriodDaily

	// Use monthly for monthly config
	if m.cfg.DefaultMonthlyRequests > 0 && m.cfg.DefaultDailyRequests == 0 {
		limit = m.cfg.DefaultMonthlyRequests
		period = PeriodMonthly
	}

	return Quota{
		TenantID: tenantID,
		Type:     quotaType,
		Limit:    limit,
		Period:   period,
	}
}

// NoopManager is a quota manager that does nothing.
type NoopManager struct{}

// NewNoopManager creates a manager that always allows.
func NewNoopManager() *NoopManager {
	return &NoopManager{}
}

func (NoopManager) Check(ctx context.Context, tenantID, userID string, quotaType QuotaType, cost int64) (CheckResult, error) {
	return CheckResult{Allowed: true}, nil
}

func (NoopManager) Increment(ctx context.Context, tenantID, userID string, quotaType QuotaType, amount int64) error {
	return nil
}

func (NoopManager) GetUsage(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Usage, error) {
	return Usage{}, nil
}

func (NoopManager) SetQuota(ctx context.Context, quota Quota) error {
	return nil
}

func (NoopManager) GetQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Quota, error) {
	return Quota{}, ErrQuotaNotFound
}

func (NoopManager) SetWebhookSender(sender billing.WebhookSender) {}

func (NoopManager) Close() error {
	return nil
}
