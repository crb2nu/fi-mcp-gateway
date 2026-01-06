package ratelimit

import (
	"context"
	"sync"
	"time"
)

// MemoryStore implements Store using an in-memory token bucket.
// Suitable for single-instance deployments or development.
type MemoryStore struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
	clock   func() time.Time

	// Cleanup configuration
	cleanupInterval time.Duration
	done            chan struct{}
}

type bucket struct {
	tokens     float64
	lastUpdate time.Time
	limit      Limit
}

// MemoryStoreOption configures a MemoryStore.
type MemoryStoreOption func(*MemoryStore)

// WithClock sets a custom clock function for testing.
func WithClock(clock func() time.Time) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.clock = clock
	}
}

// WithCleanupInterval sets how often to clean up expired buckets.
func WithCleanupInterval(d time.Duration) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.cleanupInterval = d
	}
}

// NewMemoryStore creates a new in-memory rate limit store.
func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore {
	s := &MemoryStore{
		buckets:         make(map[string]*bucket),
		clock:           time.Now,
		cleanupInterval: 5 * time.Minute,
		done:            make(chan struct{}),
	}

	for _, opt := range opts {
		opt(s)
	}

	go s.cleanupLoop()

	return s
}

// Take consumes n tokens from the bucket.
func (s *MemoryStore) Take(ctx context.Context, key string, limit Limit, n int) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	b := s.getOrCreateBucket(key, limit, now)

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastUpdate)
	refillRate := limit.RequestsPerSecond()
	newTokens := elapsed.Seconds() * refillRate
	b.tokens = min(float64(limit.EffectiveBurst()), b.tokens+newTokens)
	b.lastUpdate = now

	// Check if we can consume
	if b.tokens >= float64(n) {
		b.tokens -= float64(n)
		return Result{
			Allowed:   true,
			Remaining: int(b.tokens),
			ResetAt:   s.calculateResetTime(now, limit),
			Limit:     limit,
		}, nil
	}

	// Calculate retry-after
	tokensNeeded := float64(n) - b.tokens
	retryAfter := time.Duration(tokensNeeded / refillRate * float64(time.Second))

	return Result{
		Allowed:    false,
		Remaining:  int(b.tokens),
		ResetAt:    s.calculateResetTime(now, limit),
		RetryAfter: retryAfter,
		Limit:      limit,
	}, nil
}

// Peek returns the current state without consuming tokens.
func (s *MemoryStore) Peek(ctx context.Context, key string, limit Limit) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.clock()
	b, exists := s.buckets[key]
	if !exists {
		return Result{
			Allowed:   true,
			Remaining: limit.EffectiveBurst(),
			ResetAt:   s.calculateResetTime(now, limit),
			Limit:     limit,
		}, nil
	}

	// Calculate current tokens with refill
	elapsed := now.Sub(b.lastUpdate)
	refillRate := limit.RequestsPerSecond()
	newTokens := elapsed.Seconds() * refillRate
	currentTokens := min(float64(limit.EffectiveBurst()), b.tokens+newTokens)

	return Result{
		Allowed:   currentTokens >= 1,
		Remaining: int(currentTokens),
		ResetAt:   s.calculateResetTime(now, limit),
		Limit:     limit,
	}, nil
}

// Reset clears the rate limit state for a key.
func (s *MemoryStore) Reset(ctx context.Context, key string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.buckets, key)
	return nil
}

// Close stops the cleanup goroutine and releases resources.
func (s *MemoryStore) Close() error {
	close(s.done)
	return nil
}

func (s *MemoryStore) getOrCreateBucket(key string, limit Limit, now time.Time) *bucket {
	b, exists := s.buckets[key]
	if !exists {
		b = &bucket{
			tokens:     float64(limit.EffectiveBurst()),
			lastUpdate: now,
			limit:      limit,
		}
		s.buckets[key] = b
	}
	return b
}

func (s *MemoryStore) calculateResetTime(now time.Time, limit Limit) time.Time {
	// Reset time is when the bucket would be full again
	return now.Add(limit.Window)
}

func (s *MemoryStore) cleanupLoop() {
	ticker := time.NewTicker(s.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

func (s *MemoryStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	for key, b := range s.buckets {
		// Remove buckets that are full and haven't been used recently
		elapsed := now.Sub(b.lastUpdate)
		if elapsed > s.cleanupInterval*2 {
			// Check if bucket would be full
			refillRate := b.limit.RequestsPerSecond()
			if refillRate > 0 {
				potentialTokens := elapsed.Seconds() * refillRate
				if b.tokens+potentialTokens >= float64(b.limit.EffectiveBurst()) {
					delete(s.buckets, key)
				}
			}
		}
	}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
