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
	}, []string{"source"}) // source: "auth", "route", "backend_dial", "backend_io"

	BackendDialDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mcp_gateway_backend_dial_duration_seconds",
		Help:    "Time taken to dial backend servers",
		Buckets: prometheus.DefBuckets,
	})
)
