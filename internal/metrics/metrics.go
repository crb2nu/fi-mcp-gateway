package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	SessionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mcp_gateway_sessions_active",
		Help: "Current number of active client WebSocket sessions",
	})

	SessionsActiveByTenant = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mcp_gateway_sessions_active_by_tenant",
		Help: "Current number of active sessions per tenant",
	}, []string{"tenant"})

	MessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_messages_total",
		Help: "Total number of MCP messages processed",
	}, []string{"direction", "type"}) // direction: "in", "out"; type: "text", "binary"

	ErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_errors_total",
		Help: "Total number of errors encountered",
	}, []string{"source"}) // source: "auth", "route", "backend_dial", "backend_io", "ratelimit"

	BackendDialDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mcp_gateway_backend_dial_duration_seconds",
		Help:    "Time taken to dial backend servers",
		Buckets: prometheus.DefBuckets,
	})

	// Rate limiting metrics
	RateLimitedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_ratelimited_total",
		Help: "Total number of requests rejected due to rate limiting",
	}, []string{"tenant", "user"})

	RateLimitTokensRemaining = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mcp_gateway_ratelimit_tokens_remaining",
		Help: "Current number of rate limit tokens remaining",
	}, []string{"scope", "tenant"})

	RateLimitCheckDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mcp_gateway_ratelimit_check_duration_seconds",
		Help:    "Time taken to check rate limits",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1},
	})

	// Quota metrics
	QuotaUsageGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mcp_gateway_quota_usage",
		Help: "Current quota usage by tenant and type",
	}, []string{"tenant", "quota_type"})

	QuotaExceededTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_quota_exceeded_total",
		Help: "Total number of requests blocked due to quota exceeded",
	}, []string{"tenant", "quota_type"})

	QuotaWarningTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_quota_warning_total",
		Help: "Total number of requests that triggered soft limit warnings",
	}, []string{"tenant", "quota_type"})

	// API Key metrics
	APIKeysCreatedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_apikeys_created_total",
		Help: "Total number of API keys created",
	}, []string{"tenant"})

	APIKeysRevokedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_apikeys_revoked_total",
		Help: "Total number of API keys revoked",
	}, []string{"tenant"})

	APIKeysRotatedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_apikeys_rotated_total",
		Help: "Total number of API keys rotated",
	}, []string{"tenant"})

	APIKeyAuthTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_apikey_auth_total",
		Help: "Total number of API key authentication attempts",
	}, []string{"tenant", "result"})

	APIKeyAuthFailedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_apikey_auth_failed_total",
		Help: "Total number of failed API key authentication attempts",
	}, []string{"tenant", "reason"})

	// Usage tracking metrics
	UsageEventsStoredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_usage_events_stored_total",
		Help: "Total number of usage events successfully stored",
	})

	UsageEventsFailedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_usage_events_failed_total",
		Help: "Total number of usage events that failed to store",
	})

	UsageEventsRetriedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_usage_events_retried_total",
		Help: "Total number of usage event store retry attempts",
	})

	UsageEventsDLQTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_usage_events_dlq_total",
		Help: "Total number of usage events sent to dead-letter queue",
	})

	UsageFlushDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mcp_gateway_usage_flush_duration_seconds",
		Help:    "Time taken to flush usage events to storage",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
	})
)
