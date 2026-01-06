package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestKey_String(t *testing.T) {
	tests := []struct {
		name string
		key  Key
		want string
	}{
		{
			name: "empty key",
			key:  Key{},
			want: "global",
		},
		{
			name: "tenant only",
			key:  Key{Tenant: "acme"},
			want: "t:acme",
		},
		{
			name: "user only",
			key:  Key{User: "alice"},
			want: "u:alice",
		},
		{
			name: "tenant and user",
			key:  Key{Tenant: "acme", User: "alice"},
			want: "t:acme:u:alice",
		},
		{
			name: "full key",
			key:  Key{Scope: "ws", Tenant: "acme", Team: "eng", User: "alice", Tool: "search"},
			want: "s:ws:t:acme:tm:eng:u:alice:tl:search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.key.String()
			if got != tt.want {
				t.Errorf("Key.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLimit_RequestsPerSecond(t *testing.T) {
	tests := []struct {
		name  string
		limit Limit
		want  float64
	}{
		{
			name:  "100 per second",
			limit: Limit{Requests: 100, Window: time.Second},
			want:  100,
		},
		{
			name:  "60 per minute",
			limit: Limit{Requests: 60, Window: time.Minute},
			want:  1,
		},
		{
			name:  "zero window",
			limit: Limit{Requests: 100, Window: 0},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.limit.RequestsPerSecond()
			if got != tt.want {
				t.Errorf("Limit.RequestsPerSecond() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLimit_EffectiveBurst(t *testing.T) {
	tests := []struct {
		name  string
		limit Limit
		want  int
	}{
		{
			name:  "explicit burst",
			limit: Limit{Requests: 100, Burst: 150},
			want:  150,
		},
		{
			name:  "default to requests",
			limit: Limit{Requests: 100, Burst: 0},
			want:  100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.limit.EffectiveBurst()
			if got != tt.want {
				t.Errorf("Limit.EffectiveBurst() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemoryStore_Take(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	defer store.Close()

	limit := Limit{
		Requests: 10,
		Window:   time.Second,
		Burst:    10,
	}

	// First request should be allowed
	result, err := store.Take(ctx, "test-key", limit, 1)
	if err != nil {
		t.Fatalf("Take() error = %v", err)
	}
	if !result.Allowed {
		t.Error("first request should be allowed")
	}
	if result.Remaining != 9 {
		t.Errorf("Remaining = %d, want 9", result.Remaining)
	}

	// Consume all remaining tokens
	for i := 0; i < 9; i++ {
		result, err = store.Take(ctx, "test-key", limit, 1)
		if err != nil {
			t.Fatalf("Take() error = %v", err)
		}
		if !result.Allowed {
			t.Errorf("request %d should be allowed", i+2)
		}
	}

	// 11th request should be denied
	result, err = store.Take(ctx, "test-key", limit, 1)
	if err != nil {
		t.Fatalf("Take() error = %v", err)
	}
	if result.Allowed {
		t.Error("11th request should be denied")
	}
	if result.RetryAfter <= 0 {
		t.Error("RetryAfter should be positive")
	}
}

func TestMemoryStore_TakeN(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	defer store.Close()

	limit := Limit{
		Requests: 10,
		Window:   time.Second,
		Burst:    10,
	}

	// Take 5 tokens
	result, err := store.Take(ctx, "test-key", limit, 5)
	if err != nil {
		t.Fatalf("Take() error = %v", err)
	}
	if !result.Allowed {
		t.Error("request should be allowed")
	}
	if result.Remaining != 5 {
		t.Errorf("Remaining = %d, want 5", result.Remaining)
	}

	// Take 6 more - should fail (only 5 left)
	result, err = store.Take(ctx, "test-key", limit, 6)
	if err != nil {
		t.Fatalf("Take() error = %v", err)
	}
	if result.Allowed {
		t.Error("request should be denied (not enough tokens)")
	}
}

func TestMemoryStore_Peek(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	defer store.Close()

	limit := Limit{
		Requests: 10,
		Window:   time.Second,
		Burst:    10,
	}

	// Peek before any requests - should be full
	result, err := store.Peek(ctx, "test-key", limit)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if !result.Allowed {
		t.Error("Peek should show allowed when full")
	}
	if result.Remaining != 10 {
		t.Errorf("Remaining = %d, want 10", result.Remaining)
	}

	// Take some tokens
	_, _ = store.Take(ctx, "test-key", limit, 3)

	// Peek should show reduced tokens
	result, err = store.Peek(ctx, "test-key", limit)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if result.Remaining != 7 {
		t.Errorf("Remaining = %d, want 7", result.Remaining)
	}
}

func TestMemoryStore_Reset(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	defer store.Close()

	limit := Limit{
		Requests: 10,
		Window:   time.Second,
		Burst:    10,
	}

	// Exhaust tokens
	for i := 0; i < 10; i++ {
		_, _ = store.Take(ctx, "test-key", limit, 1)
	}

	// Verify exhausted
	result, _ := store.Take(ctx, "test-key", limit, 1)
	if result.Allowed {
		t.Error("should be exhausted")
	}

	// Reset
	err := store.Reset(ctx, "test-key")
	if err != nil {
		t.Fatalf("Reset() error = %v", err)
	}

	// Should have full bucket again
	result, _ = store.Peek(ctx, "test-key", limit)
	if result.Remaining != 10 {
		t.Errorf("after reset: Remaining = %d, want 10", result.Remaining)
	}
}

func TestMemoryStore_TokenRefill(t *testing.T) {
	now := time.Now()
	currentTime := now

	store := NewMemoryStore(
		WithClock(func() time.Time { return currentTime }),
	)
	defer store.Close()

	ctx := context.Background()
	limit := Limit{
		Requests: 10,
		Window:   time.Second,
		Burst:    10,
	}

	// Exhaust all tokens
	for i := 0; i < 10; i++ {
		_, _ = store.Take(ctx, "test-key", limit, 1)
	}

	// Verify exhausted
	result, _ := store.Peek(ctx, "test-key", limit)
	if result.Remaining != 0 {
		t.Errorf("should be exhausted, got %d", result.Remaining)
	}

	// Advance time by 500ms - should refill 5 tokens
	currentTime = now.Add(500 * time.Millisecond)

	result, _ = store.Peek(ctx, "test-key", limit)
	if result.Remaining != 5 {
		t.Errorf("after 500ms: Remaining = %d, want 5", result.Remaining)
	}

	// Advance time by another 600ms - should be full (capped at burst)
	currentTime = now.Add(1100 * time.Millisecond)

	result, _ = store.Peek(ctx, "test-key", limit)
	if result.Remaining != 10 {
		t.Errorf("after 1100ms: Remaining = %d, want 10 (capped at burst)", result.Remaining)
	}
}

func TestHierarchicalLimiter_Disabled(t *testing.T) {
	cfg := Config{Enabled: false}
	limiter, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer limiter.Close()

	if limiter.Enabled() {
		t.Error("limiter should be disabled")
	}

	// Should always allow when disabled
	result, err := limiter.Allow(context.Background(), Key{User: "test"})
	if err != nil {
		t.Fatalf("Allow() error = %v", err)
	}
	if !result.Allowed {
		t.Error("disabled limiter should always allow")
	}
}

func TestHierarchicalLimiter_UserLimit(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Store:   "memory",
		UserRPS: 5,
		Window:  time.Second,
	}
	limiter, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer limiter.Close()

	ctx := context.Background()
	key := Key{User: "alice"}

	// Make 5 requests - all should pass
	for i := 0; i < 5; i++ {
		result, err := limiter.Allow(ctx, key)
		if err != nil {
			t.Fatalf("Allow() error = %v", err)
		}
		if !result.Allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// 6th request should fail
	_, err = limiter.Allow(ctx, key)
	if err != ErrRateLimited {
		t.Errorf("6th request should return ErrRateLimited, got %v", err)
	}
}

func TestNoopLimiter(t *testing.T) {
	limiter := NewNoopLimiter()

	ctx := context.Background()
	key := Key{User: "test"}

	// Should always allow
	for i := 0; i < 1000; i++ {
		result, err := limiter.Allow(ctx, key)
		if err != nil {
			t.Fatalf("Allow() error = %v", err)
		}
		if !result.Allowed {
			t.Error("NoopLimiter should always allow")
		}
	}
}

func TestGatewayAdapter_CheckMessage(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Store:   "memory",
		UserRPS: 2,
		Window:  time.Second,
	}
	limiter, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer limiter.Close()

	adapter := NewGatewayAdapter(limiter)

	// First two calls should be allowed
	allowed, _, err := adapter.CheckMessage("tenant1", "user1", "tool1")
	if err != nil {
		t.Fatalf("CheckMessage() error = %v", err)
	}
	if !allowed {
		t.Error("first call should be allowed")
	}

	allowed, _, err = adapter.CheckMessage("tenant1", "user1", "tool1")
	if err != nil {
		t.Fatalf("CheckMessage() error = %v", err)
	}
	if !allowed {
		t.Error("second call should be allowed")
	}

	// Third call should be denied
	allowed, retryAfter, err := adapter.CheckMessage("tenant1", "user1", "tool1")
	if err != nil {
		t.Fatalf("CheckMessage() error = %v", err)
	}
	if allowed {
		t.Error("third call should be denied")
	}
	if retryAfter <= 0 {
		t.Error("retryAfter should be positive")
	}

	// Different user should still be allowed
	allowed, _, err = adapter.CheckMessage("tenant1", "user2", "tool1")
	if err != nil {
		t.Fatalf("CheckMessage() error = %v", err)
	}
	if !allowed {
		t.Error("different user should be allowed")
	}
}
