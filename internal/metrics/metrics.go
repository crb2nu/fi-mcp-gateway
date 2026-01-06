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
)
