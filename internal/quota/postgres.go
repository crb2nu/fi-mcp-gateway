package quota

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/storage"
)

// PostgresStore implements Store using PostgreSQL.
type PostgresStore struct {
	pg *storage.Postgres
}

// NewPostgresStore creates a new Postgres-backed quota store.
func NewPostgresStore(pg *storage.Postgres) *PostgresStore {
	return &PostgresStore{pg: pg}
}

// GetQuota retrieves a quota definition.
func (s *PostgresStore) GetQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Quota, error) {
	var quota Quota
	var softLimit sql.NullInt64

	err := s.pg.QueryRow(ctx, `
		SELECT tenant_id, user_id, quota_type, limit_value, soft_limit, period, created_at, updated_at
		FROM quotas
		WHERE tenant_id = $1 AND user_id = $2 AND quota_type = $3
	`, tenantID, userID, string(quotaType)).Scan(
		&quota.TenantID,
		&quota.UserID,
		&quota.Type,
		&quota.Limit,
		&softLimit,
		&quota.Period,
		&quota.CreatedAt,
		&quota.UpdatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return Quota{}, ErrQuotaNotFound
	}
	if err != nil {
		return Quota{}, err
	}

	if softLimit.Valid {
		quota.SoftLimit = softLimit.Int64
	}

	return quota, nil
}

// SetQuota creates or updates a quota.
func (s *PostgresStore) SetQuota(ctx context.Context, quota Quota) error {
	var softLimit *int64
	if quota.SoftLimit > 0 {
		softLimit = &quota.SoftLimit
	}

	_, err := s.pg.Exec(ctx, `
		INSERT INTO quotas (tenant_id, user_id, quota_type, limit_value, soft_limit, period, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		ON CONFLICT (tenant_id, user_id, quota_type)
		DO UPDATE SET
			limit_value = EXCLUDED.limit_value,
			soft_limit = EXCLUDED.soft_limit,
			period = EXCLUDED.period,
			updated_at = NOW()
	`, quota.TenantID, quota.UserID, string(quota.Type), quota.Limit, softLimit, string(quota.Period))

	return err
}

// DeleteQuota removes a quota.
func (s *PostgresStore) DeleteQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) error {
	_, err := s.pg.Exec(ctx, `
		DELETE FROM quotas
		WHERE tenant_id = $1 AND user_id = $2 AND quota_type = $3
	`, tenantID, userID, string(quotaType))

	return err
}

// GetUsage retrieves usage for a period.
func (s *PostgresStore) GetUsage(ctx context.Context, tenantID, userID string, quotaType QuotaType, periodStart time.Time) (Usage, error) {
	var usage Usage

	err := s.pg.QueryRow(ctx, `
		SELECT tenant_id, user_id, quota_type, period_start, period_end, current_usage, last_updated
		FROM quota_usage
		WHERE tenant_id = $1 AND user_id = $2 AND quota_type = $3 AND period_start = $4
	`, tenantID, userID, string(quotaType), periodStart).Scan(
		&usage.TenantID,
		&usage.UserID,
		&usage.Type,
		&usage.PeriodStart,
		&usage.PeriodEnd,
		&usage.Current,
		&usage.LastUpdated,
	)

	if errors.Is(err, sql.ErrNoRows) {
		// Return zero usage
		return Usage{
			TenantID:    tenantID,
			UserID:      userID,
			Type:        quotaType,
			PeriodStart: periodStart,
			Current:     0,
		}, nil
	}
	if err != nil {
		return Usage{}, err
	}

	return usage, nil
}

// IncrementUsage atomically increments usage.
func (s *PostgresStore) IncrementUsage(ctx context.Context, tenantID, userID string, quotaType QuotaType, amount int64, periodStart, periodEnd time.Time) (int64, error) {
	var newUsage int64

	err := s.pg.QueryRow(ctx, `
		INSERT INTO quota_usage (tenant_id, user_id, quota_type, period_start, period_end, current_usage, last_updated)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (tenant_id, user_id, quota_type, period_start)
		DO UPDATE SET
			current_usage = quota_usage.current_usage + EXCLUDED.current_usage,
			last_updated = NOW()
		RETURNING current_usage
	`, tenantID, userID, string(quotaType), periodStart, periodEnd, amount).Scan(&newUsage)

	if err != nil {
		return 0, err
	}

	return newUsage, nil
}

// Close releases resources.
func (s *PostgresStore) Close() error {
	return s.pg.Close()
}

// GetUsageHistory retrieves usage records for a tenant over time.
func (s *PostgresStore) GetUsageHistory(ctx context.Context, tenantID string, quotaType QuotaType, since time.Time) ([]Usage, error) {
	rows, err := s.pg.Query(ctx, `
		SELECT tenant_id, user_id, quota_type, period_start, period_end, current_usage, last_updated
		FROM quota_usage
		WHERE tenant_id = $1 AND quota_type = $2 AND period_start >= $3
		ORDER BY period_start DESC
	`, tenantID, string(quotaType), since)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var usages []Usage
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.TenantID, &u.UserID, &u.Type, &u.PeriodStart, &u.PeriodEnd, &u.Current, &u.LastUpdated); err != nil {
			return nil, err
		}
		usages = append(usages, u)
	}

	return usages, rows.Err()
}
