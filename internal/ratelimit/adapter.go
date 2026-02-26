package ratelimit

import (
	"context"
	"time"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/metrics"
)

// GatewayAdapter adapts the HierarchicalLimiter to the mcpws.RateLimiter interface.
type GatewayAdapter struct {
	limiter *HierarchicalLimiter
}

// NewGatewayAdapter creates a new adapter for the gateway.
func NewGatewayAdapter(limiter *HierarchicalLimiter) *GatewayAdapter {
	return &GatewayAdapter{limiter: limiter}
}

// CheckMessage checks if a message should be rate limited.
// Implements mcpws.RateLimiter interface.
func (a *GatewayAdapter) CheckMessage(tenant, user, tool string) (allowed bool, retryAfter time.Duration, err error) {
	if a == nil || a.limiter == nil || !a.limiter.Enabled() {
		return true, 0, nil
	}

	start := time.Now()
	defer func() {
		metrics.RateLimitCheckDuration.Observe(time.Since(start).Seconds())
	}()

	key := Key{
		Scope:  "ws",
		Tenant: tenant,
		User:   user,
		Tool:   tool,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := a.limiter.Allow(ctx, key)
	if err == ErrRateLimited {
		return false, result.RetryAfter, nil
	}
	if err != nil {
		// On error, allow the request but log it
		return true, 0, err
	}

	// Update metrics gauge for observability
	if tenant != "" {
		metrics.RateLimitTokensRemaining.WithLabelValues("tenant", tenant).Set(float64(result.Remaining))
	}

	return result.Allowed, result.RetryAfter, nil
}
