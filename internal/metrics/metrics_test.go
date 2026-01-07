package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/metrics"
)

func TestMetricsEndpoint(t *testing.T) {
	// Increment a metric to see if it appears
	metrics.SessionsActive.Inc()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()

	handler := promhttp.Handler()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "mcp_gateway_sessions_active") {
		t.Errorf("expected metrics to contain 'mcp_gateway_sessions_active', got:\n%s", body)
	}

	// Check for the value 1
	if !strings.Contains(body, "mcp_gateway_sessions_active 1") {
		t.Errorf("expected 'mcp_gateway_sessions_active 1', got:\n%s", body)
	}
}
