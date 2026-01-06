package quota

import (
	"context"
	"sync"
	"time"
)

// MemoryStore implements Store using in-memory storage.
// Suitable for development and single-instance deployments.
type MemoryStore struct {
	mu     sync.RWMutex
	quotas map[string]Quota
	usage  map[string]Usage
}

// NewMemoryStore creates a new in-memory quota store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		quotas: make(map[string]Quota),
		usage:  make(map[string]Usage),
	}
}

func makeQuotaKey(tenantID, userID string, quotaType QuotaType) string {
	return tenantID + ":" + userID + ":" + string(quotaType)
}

func makeUsageKey(tenantID, userID string, quotaType QuotaType, periodStart time.Time) string {
	return tenantID + ":" + userID + ":" + string(quotaType) + ":" + periodStart.Format(time.RFC3339)
}

// GetQuota retrieves a quota definition.
func (s *MemoryStore) GetQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) (Quota, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := makeQuotaKey(tenantID, userID, quotaType)
	quota, ok := s.quotas[key]
	if !ok {
		return Quota{}, ErrQuotaNotFound
	}
	return quota, nil
}

// SetQuota creates or updates a quota.
func (s *MemoryStore) SetQuota(ctx context.Context, quota Quota) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := makeQuotaKey(quota.TenantID, quota.UserID, quota.Type)
	now := time.Now()
	if quota.CreatedAt.IsZero() {
		quota.CreatedAt = now
	}
	quota.UpdatedAt = now
	s.quotas[key] = quota
	return nil
}

// DeleteQuota removes a quota.
func (s *MemoryStore) DeleteQuota(ctx context.Context, tenantID, userID string, quotaType QuotaType) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := makeQuotaKey(tenantID, userID, quotaType)
	delete(s.quotas, key)
	return nil
}

// GetUsage retrieves usage for a period.
func (s *MemoryStore) GetUsage(ctx context.Context, tenantID, userID string, quotaType QuotaType, periodStart time.Time) (Usage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := makeUsageKey(tenantID, userID, quotaType, periodStart)
	usage, ok := s.usage[key]
	if !ok {
		// Return zero usage if not found
		return Usage{
			TenantID:    tenantID,
			UserID:      userID,
			Type:        quotaType,
			PeriodStart: periodStart,
			Current:     0,
		}, nil
	}
	return usage, nil
}

// IncrementUsage atomically increments usage.
func (s *MemoryStore) IncrementUsage(ctx context.Context, tenantID, userID string, quotaType QuotaType, amount int64, periodStart, periodEnd time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := makeUsageKey(tenantID, userID, quotaType, periodStart)
	usage, ok := s.usage[key]
	if !ok {
		usage = Usage{
			TenantID:    tenantID,
			UserID:      userID,
			Type:        quotaType,
			PeriodStart: periodStart,
			PeriodEnd:   periodEnd,
			Current:     0,
		}
	}

	usage.Current += amount
	usage.LastUpdated = time.Now()
	s.usage[key] = usage

	return usage.Current, nil
}

// Close releases resources.
func (s *MemoryStore) Close() error {
	return nil
}

// Reset clears all data (for testing).
func (s *MemoryStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotas = make(map[string]Quota)
	s.usage = make(map[string]Usage)
}
