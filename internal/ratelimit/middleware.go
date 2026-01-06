package ratelimit

import (
	"net/http"
	"strconv"
	"time"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/metrics"
)

// Middleware provides HTTP middleware for rate limiting.
type Middleware struct {
	limiter Limiter
	keyFunc KeyFunc
}

// KeyFunc extracts a rate limit Key from an HTTP request.
type KeyFunc func(r *http.Request) Key

// DefaultKeyFunc extracts tenant and user from standard headers/auth.
func DefaultKeyFunc(r *http.Request) Key {
	return Key{
		Scope:  "http",
		Tenant: r.Header.Get("X-Tenant-ID"),
		User:   r.Header.Get("X-User-ID"),
	}
}

// NewMiddleware creates a new rate limiting HTTP middleware.
func NewMiddleware(limiter Limiter, keyFunc KeyFunc) *Middleware {
	if keyFunc == nil {
		keyFunc = DefaultKeyFunc
	}
	return &Middleware{
		limiter: limiter,
		keyFunc: keyFunc,
	}
}

// Handler wraps an HTTP handler with rate limiting.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := m.keyFunc(r)

		result, err := m.limiter.Allow(r.Context(), key)
		if err == ErrRateLimited {
			m.writeRateLimitedResponse(w, result)
			metrics.RateLimitedTotal.WithLabelValues(key.Tenant, key.User).Inc()
			return
		}

		// Add rate limit headers even on success
		m.setRateLimitHeaders(w, result)

		next.ServeHTTP(w, r)
	})
}

// HandlerFunc wraps an HTTP handler function with rate limiting.
func (m *Middleware) HandlerFunc(next http.HandlerFunc) http.HandlerFunc {
	return m.Handler(next).ServeHTTP
}

func (m *Middleware) writeRateLimitedResponse(w http.ResponseWriter, result Result) {
	m.setRateLimitHeaders(w, result)

	if result.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(int64(result.RetryAfter.Seconds()), 10))
	}

	w.WriteHeader(http.StatusTooManyRequests)
	w.Write([]byte(`{"error":"rate_limited","message":"too many requests"}`))
}

func (m *Middleware) setRateLimitHeaders(w http.ResponseWriter, result Result) {
	if result.Limit.Requests > 0 {
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(result.Limit.Requests))
	}
	if result.Remaining >= 0 {
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
	}
	if !result.ResetAt.IsZero() {
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
	}
}

// WSRateLimiter provides rate limiting for WebSocket connections.
type WSRateLimiter struct {
	limiter Limiter
	cfg     Config
}

// NewWSRateLimiter creates a rate limiter for WebSocket sessions.
func NewWSRateLimiter(limiter Limiter, cfg Config) *WSRateLimiter {
	return &WSRateLimiter{
		limiter: limiter,
		cfg:     cfg,
	}
}

// CheckRequest checks if a WebSocket request should be allowed.
// Returns the result and any error.
func (l *WSRateLimiter) CheckRequest(tenant, user, tool string) (Result, error) {
	key := Key{
		Scope:  "ws",
		Tenant: tenant,
		User:   user,
		Tool:   tool,
	}

	return l.limiter.Allow(nil, key)
}

// CheckConnection checks if a new WebSocket connection should be allowed.
// This is called during connection upgrade, before the WebSocket handshake.
func (l *WSRateLimiter) CheckConnection(tenant, user string) (Result, error) {
	key := Key{
		Scope:  "ws_connect",
		Tenant: tenant,
		User:   user,
	}

	return l.limiter.Allow(nil, key)
}

// FormatRetryAfter formats the retry-after duration for error messages.
func FormatRetryAfter(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	if d < time.Second {
		return "less than 1 second"
	}
	if d < time.Minute {
		return strconv.FormatInt(int64(d.Seconds()), 10) + " seconds"
	}
	if d < time.Hour {
		return strconv.FormatInt(int64(d.Minutes()), 10) + " minutes"
	}
	return strconv.FormatInt(int64(d.Hours()), 10) + " hours"
}
