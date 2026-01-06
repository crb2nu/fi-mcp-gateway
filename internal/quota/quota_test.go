package quota

import (
	"context"
	"testing"
	"time"
)

func TestQuota_IsUserQuota(t *testing.T) {
	tests := []struct {
		name   string
		quota  Quota
		want   bool
	}{
		{
			name:   "tenant quota",
			quota:  Quota{TenantID: "acme", UserID: ""},
			want:   false,
		},
		{
			name:   "user quota",
			quota:  Quota{TenantID: "acme", UserID: "alice"},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.quota.IsUserQuota(); got != tt.want {
				t.Errorf("Quota.IsUserQuota() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQuota_HasSoftLimit(t *testing.T) {
	tests := []struct {
		name  string
		quota Quota
		want  bool
	}{
		{
			name:  "no soft limit",
			quota: Quota{Limit: 1000, SoftLimit: 0},
			want:  false,
		},
		{
			name:  "with soft limit",
			quota: Quota{Limit: 1000, SoftLimit: 800},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.quota.HasSoftLimit(); got != tt.want {
				t.Errorf("Quota.HasSoftLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUsage_Remaining(t *testing.T) {
	usage := Usage{Current: 300}

	if got := usage.Remaining(1000); got != 700 {
		t.Errorf("Usage.Remaining() = %d, want 700", got)
	}

	// Over limit
	usage.Current = 1200
	if got := usage.Remaining(1000); got != 0 {
		t.Errorf("Usage.Remaining() when over limit = %d, want 0", got)
	}
}

func TestUsage_PercentUsed(t *testing.T) {
	usage := Usage{Current: 500}

	if got := usage.PercentUsed(1000); got != 50.0 {
		t.Errorf("Usage.PercentUsed() = %f, want 50.0", got)
	}

	// Zero limit
	if got := usage.PercentUsed(0); got != 0 {
		t.Errorf("Usage.PercentUsed() with zero limit = %f, want 0", got)
	}
}

func TestPeriodBounds_Daily(t *testing.T) {
	now := time.Date(2025, 1, 15, 14, 30, 0, 0, time.UTC)
	start, end := PeriodBounds(PeriodDaily, now)

	expectedStart := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("Daily start = %v, want %v", start, expectedStart)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("Daily end = %v, want %v", end, expectedEnd)
	}
}

func TestPeriodBounds_Monthly(t *testing.T) {
	now := time.Date(2025, 1, 15, 14, 30, 0, 0, time.UTC)
	start, end := PeriodBounds(PeriodMonthly, now)

	expectedStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("Monthly start = %v, want %v", start, expectedStart)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("Monthly end = %v, want %v", end, expectedEnd)
	}
}

func TestPeriodBounds_Weekly(t *testing.T) {
	// Wednesday January 15, 2025
	now := time.Date(2025, 1, 15, 14, 30, 0, 0, time.UTC)
	start, end := PeriodBounds(PeriodWeekly, now)

	// Week should start Monday January 13
	expectedStart := time.Date(2025, 1, 13, 0, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2025, 1, 20, 0, 0, 0, 0, time.UTC)

	if !start.Equal(expectedStart) {
		t.Errorf("Weekly start = %v, want %v", start, expectedStart)
	}
	if !end.Equal(expectedEnd) {
		t.Errorf("Weekly end = %v, want %v", end, expectedEnd)
	}
}

func TestMemoryStore_QuotaCRUD(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	quota := Quota{
		TenantID:  "acme",
		UserID:    "",
		Type:      QuotaTypeRequests,
		Limit:     10000,
		SoftLimit: 8000,
		Period:    PeriodDaily,
	}

	// Set quota
	err := store.SetQuota(ctx, quota)
	if err != nil {
		t.Fatalf("SetQuota() error = %v", err)
	}

	// Get quota
	got, err := store.GetQuota(ctx, "acme", "", QuotaTypeRequests)
	if err != nil {
		t.Fatalf("GetQuota() error = %v", err)
	}
	if got.Limit != 10000 {
		t.Errorf("Quota.Limit = %d, want 10000", got.Limit)
	}
	if got.SoftLimit != 8000 {
		t.Errorf("Quota.SoftLimit = %d, want 8000", got.SoftLimit)
	}

	// Delete quota
	err = store.DeleteQuota(ctx, "acme", "", QuotaTypeRequests)
	if err != nil {
		t.Fatalf("DeleteQuota() error = %v", err)
	}

	// Should not find deleted quota
	_, err = store.GetQuota(ctx, "acme", "", QuotaTypeRequests)
	if err != ErrQuotaNotFound {
		t.Errorf("GetQuota() after delete should return ErrQuotaNotFound, got %v", err)
	}
}

func TestMemoryStore_UsageIncrement(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	periodStart := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC)

	// Initial increment
	newUsage, err := store.IncrementUsage(ctx, "acme", "alice", QuotaTypeRequests, 100, periodStart, periodEnd)
	if err != nil {
		t.Fatalf("IncrementUsage() error = %v", err)
	}
	if newUsage != 100 {
		t.Errorf("newUsage = %d, want 100", newUsage)
	}

	// Second increment
	newUsage, err = store.IncrementUsage(ctx, "acme", "alice", QuotaTypeRequests, 50, periodStart, periodEnd)
	if err != nil {
		t.Fatalf("IncrementUsage() error = %v", err)
	}
	if newUsage != 150 {
		t.Errorf("newUsage = %d, want 150", newUsage)
	}

	// Get usage
	usage, err := store.GetUsage(ctx, "acme", "alice", QuotaTypeRequests, periodStart)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage.Current != 150 {
		t.Errorf("usage.Current = %d, want 150", usage.Current)
	}
}

func TestDefaultManager_Disabled(t *testing.T) {
	cfg := Config{Enabled: false}
	mgr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer mgr.Close()

	result, err := mgr.Check(context.Background(), "acme", "alice", QuotaTypeRequests, 1)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Allowed {
		t.Error("disabled manager should always allow")
	}
}

func TestDefaultManager_Check(t *testing.T) {
	cfg := Config{
		Enabled:              true,
		Store:                "memory",
		DefaultDailyRequests: 100,
		EnforceHard:          true,
	}
	mgr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer mgr.Close()

	ctx := context.Background()

	// Check without any usage - should pass
	result, err := mgr.Check(ctx, "acme", "alice", QuotaTypeRequests, 1)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Allowed {
		t.Error("first check should be allowed")
	}
	if result.Limit != 100 {
		t.Errorf("result.Limit = %d, want 100 (default)", result.Limit)
	}
}

func TestDefaultManager_CheckAndIncrement(t *testing.T) {
	cfg := Config{
		Enabled:              true,
		Store:                "memory",
		DefaultDailyRequests: 10,
		EnforceHard:          true,
	}
	mgr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer mgr.Close()

	ctx := context.Background()

	// Simulate 10 requests
	for i := 0; i < 10; i++ {
		err := mgr.Increment(ctx, "acme", "alice", QuotaTypeRequests, 1)
		if err != nil {
			t.Fatalf("Increment() error = %v", err)
		}
	}

	// 11th request should fail
	result, err := mgr.Check(ctx, "acme", "alice", QuotaTypeRequests, 1)
	if err != ErrQuotaExceeded {
		t.Errorf("Check() should return ErrQuotaExceeded, got %v", err)
	}
	if result.Allowed {
		t.Error("11th request should not be allowed")
	}
}

func TestDefaultManager_SoftLimit(t *testing.T) {
	cfg := Config{
		Enabled:              true,
		Store:                "memory",
		DefaultDailyRequests: 100,
		EnforceHard:          true,
		EnforceSoft:          true,
	}
	mgr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer mgr.Close()

	ctx := context.Background()

	// Set quota with soft limit
	err = mgr.SetQuota(ctx, Quota{
		TenantID:  "acme",
		UserID:    "",
		Type:      QuotaTypeRequests,
		Limit:     100,
		SoftLimit: 80,
		Period:    PeriodDaily,
	})
	if err != nil {
		t.Fatalf("SetQuota() error = %v", err)
	}

	// Use up to 85%
	err = mgr.Increment(ctx, "acme", "", QuotaTypeRequests, 85)
	if err != nil {
		t.Fatalf("Increment() error = %v", err)
	}

	// Check should show warning
	result, err := mgr.Check(ctx, "acme", "", QuotaTypeRequests, 1)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Allowed {
		t.Error("should still be allowed (under hard limit)")
	}
	if !result.Warning {
		t.Error("should have warning (over soft limit)")
	}
}

func TestNoopManager(t *testing.T) {
	mgr := NewNoopManager()

	result, err := mgr.Check(context.Background(), "acme", "alice", QuotaTypeRequests, 1)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Allowed {
		t.Error("NoopManager should always allow")
	}
}

func TestFormatUsage(t *testing.T) {
	result := FormatUsage(500, 1000)
	expected := "500/1000 (50.0%)"
	if result != expected {
		t.Errorf("FormatUsage() = %q, want %q", result, expected)
	}
}
