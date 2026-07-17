package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/apikeys"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/mcpws"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/policy"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/quota"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/ratelimit"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/usage"
)

type Server struct {
	reg           *registry.Registry
	ws            *mcpws.Gateway
	authenticator auth.Authenticator
	pol           policy.Policy
	apikeys       apikeys.Manager
	quotas        quota.Manager
	usage         usage.Tracker
	httpLimiter   ratelimit.Limiter
	toolTimeout   time.Duration
}

type Config struct {
	Registry      *registry.Registry
	Authenticator auth.Authenticator
	Policy        policy.Policy
	RateLimiter   mcpws.RateLimiter
	HTTPLimiter   ratelimit.Limiter // Optional rate limiter for HTTP API
	APIKeys       apikeys.Manager
	Quotas        quota.Manager
	Usage         usage.Tracker

	// ToolCallTimeout bounds a single REST tool invocation. Defaults to
	// FI_MCP_TOOL_CALL_TIMEOUT or 30s.
	ToolCallTimeout time.Duration
}

func New(cfg Config) *Server {
	toolTimeout := cfg.ToolCallTimeout
	if toolTimeout <= 0 {
		toolTimeout = toolCallTimeoutFromEnv()
	}

	return &Server{
		reg:           cfg.Registry,
		authenticator: cfg.Authenticator,
		pol:           cfg.Policy,
		apikeys:       cfg.APIKeys,
		quotas:        cfg.Quotas,
		usage:         cfg.Usage,
		httpLimiter:   cfg.HTTPLimiter,
		toolTimeout:   toolTimeout,
		ws: mcpws.New(mcpws.Config{
			Registry:      cfg.Registry,
			Authenticator: cfg.Authenticator,
			Policy:        cfg.Policy,
			RateLimiter:   cfg.RateLimiter,
			UsageTracker:  cfg.Usage,
			QuotaManager:  cfg.Quotas,
		}),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health and readiness endpoints (no rate limiting)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		})
	})

	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ready := s.reg != nil && len(s.reg.Servers) > 0
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{
			"ready":   ready,
			"servers": len(s.reg.Servers),
		})
	})

	// Metrics endpoint (no rate limiting)
	mux.Handle("/metrics", promhttp.Handler())

	mux.HandleFunc("GET /hosts", func(w http.ResponseWriter, r *http.Request) {
		hosts := make([]string, 0, len(s.reg.Servers))
		for _, srv := range s.reg.Servers {
			if srv != nil && !srv.IsLocalOnly() {
				hosts = append(hosts, srv.Name)
			}
		}
		writeJSON(w, http.StatusOK, hosts)
	})

	// Build API mux (these get rate limited)
	apiMux := http.NewServeMux()

	apiMux.HandleFunc("GET /api/servers", func(w http.ResponseWriter, r *http.Request) {
		type item struct {
			Name        string   `json:"name"`
			Categories  []string `json:"categories"`
			Description string   `json:"description,omitempty"`
		}

		out := make([]item, 0, len(s.reg.Servers))
		for _, srv := range s.reg.Servers {
			if srv == nil {
				continue
			}
			desc := ""
			if srv.Common != nil {
				desc = srv.Common.Description
			}
			out = append(out, item{
				Name:        srv.Name,
				Categories:  srv.Categories,
				Description: desc,
			})
		}

		writeJSON(w, http.StatusOK, map[string]any{"servers": out})
	})

	// REST tool invocation (keyless like GET /api/servers; gated by the
	// registry always_allow allowlist instead of API keys).
	apiMux.HandleFunc("POST /api/v1/tools/{server}/{tool}", s.handleInvokeTool)

	// API Key management endpoints
	if s.apikeys != nil {
		apiMux.HandleFunc("POST /api/v1/keys", s.handleCreateKey)
		apiMux.HandleFunc("GET /api/v1/keys", s.handleListKeys)
		apiMux.HandleFunc("GET /api/v1/keys/{id}", s.handleGetKey)
		apiMux.HandleFunc("DELETE /api/v1/keys/{id}", s.handleRevokeKey)
		apiMux.HandleFunc("POST /api/v1/keys/{id}/rotate", s.handleRotateKey)
	}

	// Quota status endpoint
	if s.quotas != nil {
		apiMux.HandleFunc("GET /api/v1/quotas", s.handleGetQuotas)
	}

	// Usage analytics endpoints
	if s.usage != nil {
		apiMux.HandleFunc("GET /api/v1/usage", s.handleGetUsage)
		apiMux.HandleFunc("GET /api/v1/usage/export", s.handleExportUsage)
	}

	// Apply rate limiting middleware to API routes
	var apiHandler http.Handler = apiMux
	if s.httpLimiter != nil {
		rateLimitMiddleware := ratelimit.NewMiddleware(s.httpLimiter, nil)
		apiHandler = rateLimitMiddleware.Handler(apiMux)
	}

	// Mount rate-limited API handler
	mux.Handle("/api/", apiHandler)

	// WebSocket MCP gateway (has its own rate limiting).
	mux.HandleFunc("/ws", s.ws.HandleWS)

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// requireAuth authenticates the request and returns the principal.
// Returns nil if authentication fails (error already written to response).
func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) *auth.Principal {
	if s.authenticator == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil
	}

	principal, err := s.authenticator.Authenticate(r)
	if err != nil || principal == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil
	}

	return principal
}

// API Key handlers

type createKeyRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes,omitempty"`
	ExpiresIn string   `json:"expires_in,omitempty"` // e.g., "720h" for 30 days
}

type keyResponse struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	KeyPrefix string     `json:"key_prefix"`
	Scopes    []string   `json:"scopes,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	LastUsed  *time.Time `json:"last_used_at,omitempty"`
	Revoked   bool       `json:"revoked"`
}

func apiKeyToResponse(k apikeys.APIKey) keyResponse {
	return keyResponse{
		ID:        k.ID,
		Name:      k.Name,
		KeyPrefix: k.KeyPrefix,
		Scopes:    k.Scopes,
		ExpiresAt: k.ExpiresAt,
		CreatedAt: k.CreatedAt,
		LastUsed:  k.LastUsedAt,
		Revoked:   k.RevokedAt != nil,
	}
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	principal := s.requireAuth(w, r)
	if principal == nil {
		return
	}

	var req createKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	var expiresIn time.Duration
	if req.ExpiresIn != "" {
		var err error
		expiresIn, err = time.ParseDuration(req.ExpiresIn)
		if err != nil || expiresIn < 0 {
			writeError(w, http.StatusBadRequest, "invalid expires_in duration")
			return
		}
	}

	result, err := s.apikeys.Create(r.Context(), apikeys.CreateKeyRequest{
		TenantID:  principal.TenantID(),
		UserID:    principal.Subject,
		Name:      strings.TrimSpace(req.Name),
		Scopes:    req.Scopes,
		ExpiresIn: expiresIn,
	})
	if err != nil {
		switch err {
		case apikeys.ErrTooManyKeys:
			writeError(w, http.StatusConflict, "maximum keys per user exceeded")
		case apikeys.ErrInvalidRequest:
			writeError(w, http.StatusBadRequest, "invalid request")
		default:
			writeError(w, http.StatusInternalServerError, "failed to create key")
		}
		return
	}

	// Return the plaintext key only once
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":        result.PlaintextKey,
		"key_id":     result.Key.ID,
		"key_prefix": result.Key.KeyPrefix,
		"name":       result.Key.Name,
		"expires_at": result.Key.ExpiresAt,
		"created_at": result.Key.CreatedAt,
		"message":    "Save this key securely. It will not be shown again.",
	})
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	principal := s.requireAuth(w, r)
	if principal == nil {
		return
	}

	keys, err := s.apikeys.List(r.Context(), principal.TenantID(), principal.Subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list keys")
		return
	}

	response := make([]keyResponse, 0, len(keys))
	for _, k := range keys {
		response = append(response, apiKeyToResponse(k))
	}

	writeJSON(w, http.StatusOK, map[string]any{"keys": response})
}

func (s *Server) handleGetKey(w http.ResponseWriter, r *http.Request) {
	principal := s.requireAuth(w, r)
	if principal == nil {
		return
	}

	keyID := r.PathValue("id")
	if keyID == "" {
		writeError(w, http.StatusBadRequest, "key id required")
		return
	}

	key, err := s.apikeys.Get(r.Context(), keyID)
	if err != nil {
		if err == apikeys.ErrKeyNotFound {
			writeError(w, http.StatusNotFound, "key not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to get key")
		}
		return
	}

	// Verify ownership
	if key.TenantID != principal.TenantID() || key.UserID != principal.Subject {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}

	writeJSON(w, http.StatusOK, apiKeyToResponse(key))
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	principal := s.requireAuth(w, r)
	if principal == nil {
		return
	}

	keyID := r.PathValue("id")
	if keyID == "" {
		writeError(w, http.StatusBadRequest, "key id required")
		return
	}

	// Verify ownership first
	key, err := s.apikeys.Get(r.Context(), keyID)
	if err != nil {
		if err == apikeys.ErrKeyNotFound {
			writeError(w, http.StatusNotFound, "key not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to get key")
		}
		return
	}

	if key.TenantID != principal.TenantID() || key.UserID != principal.Subject {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}

	if err := s.apikeys.Revoke(r.Context(), keyID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke key")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"message": "key revoked"})
}

func (s *Server) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	principal := s.requireAuth(w, r)
	if principal == nil {
		return
	}

	keyID := r.PathValue("id")
	if keyID == "" {
		writeError(w, http.StatusBadRequest, "key id required")
		return
	}

	// Verify ownership first
	key, err := s.apikeys.Get(r.Context(), keyID)
	if err != nil {
		if err == apikeys.ErrKeyNotFound {
			writeError(w, http.StatusNotFound, "key not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to get key")
		}
		return
	}

	if key.TenantID != principal.TenantID() || key.UserID != principal.Subject {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}

	result, err := s.apikeys.Rotate(r.Context(), keyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rotate key")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"key":            result.PlaintextKey,
		"key_id":         result.Key.ID,
		"key_prefix":     result.Key.KeyPrefix,
		"name":           result.Key.Name,
		"old_key_id":     keyID,
		"old_key_status": "revoked",
		"message":        "Save this key securely. It will not be shown again.",
	})
}

// Quota handlers

func (s *Server) handleGetQuotas(w http.ResponseWriter, r *http.Request) {
	principal := s.requireAuth(w, r)
	if principal == nil {
		return
	}

	tenantID := principal.TenantID()
	userID := principal.Subject

	// Get all quota types for this user
	quotaTypes := []quota.QuotaType{
		quota.QuotaTypeRequests,
		quota.QuotaTypeTokensIn,
		quota.QuotaTypeTokensOut,
		quota.QuotaTypeToolCalls,
	}

	type quotaStatus struct {
		Type      string  `json:"type"`
		Limit     int64   `json:"limit"`
		SoftLimit int64   `json:"soft_limit,omitempty"`
		Current   int64   `json:"current"`
		Remaining int64   `json:"remaining"`
		Percent   float64 `json:"percent_used"`
		Period    string  `json:"period"`
	}

	var statuses []quotaStatus
	for _, qt := range quotaTypes {
		quotaUsage, err := s.quotas.GetUsage(r.Context(), tenantID, userID, qt)
		if err != nil {
			continue // Skip if no quota set
		}

		q, err := s.quotas.GetQuota(r.Context(), tenantID, userID, qt)
		if err != nil {
			continue
		}

		remaining := q.Limit - quotaUsage.Current
		if remaining < 0 {
			remaining = 0
		}

		var percent float64
		if q.Limit > 0 {
			percent = float64(quotaUsage.Current) / float64(q.Limit) * 100
		}

		statuses = append(statuses, quotaStatus{
			Type:      string(qt),
			Limit:     q.Limit,
			SoftLimit: q.SoftLimit,
			Current:   quotaUsage.Current,
			Remaining: remaining,
			Percent:   percent,
			Period:    string(q.Period),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": tenantID,
		"user_id":   userID,
		"quotas":    statuses,
	})
}

// Usage handlers

func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	principal := s.requireAuth(w, r)
	if principal == nil {
		return
	}

	tenantID := principal.TenantID()
	userID := principal.Subject

	// Parse time range from query params
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	var start, end time.Time
	if startStr != "" {
		var err error
		start, err = time.Parse(time.RFC3339, startStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start time format (use RFC3339)")
			return
		}
	} else {
		// Default to last 24 hours
		start = time.Now().Add(-24 * time.Hour)
	}

	if endStr != "" {
		var err error
		end, err = time.Parse(time.RFC3339, endStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid end time format (use RFC3339)")
			return
		}
	} else {
		end = time.Now()
	}

	summary, err := s.usage.GetSummary(r.Context(), tenantID, userID, start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage summary")
		return
	}

	// Convert duration to milliseconds for JSON
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":         tenantID,
		"user_id":           userID,
		"period_start":      start,
		"period_end":        end,
		"total_events":      summary.TotalEvents,
		"success_count":     summary.SuccessCount,
		"error_count":       summary.ErrorCount,
		"total_tokens_in":   summary.TotalTokensIn,
		"total_tokens_out":  summary.TotalTokensOut,
		"total_duration_ms": summary.TotalDuration.Milliseconds(),
		"avg_duration_ms":   summary.AvgDuration.Milliseconds(),
		"tool_breakdown":    summary.ToolBreakdown,
	})
}

func (s *Server) handleExportUsage(w http.ResponseWriter, r *http.Request) {
	principal := s.requireAuth(w, r)
	if principal == nil {
		return
	}

	tenantID := principal.TenantID()
	userID := principal.Subject

	// Parse query params
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		writeError(w, http.StatusBadRequest, "format must be 'json' or 'csv'")
		return
	}

	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	limitStr := r.URL.Query().Get("limit")

	var start, end time.Time
	if startStr != "" {
		var err error
		start, err = time.Parse(time.RFC3339, startStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start time format")
			return
		}
	}
	if endStr != "" {
		var err error
		end, err = time.Parse(time.RFC3339, endStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid end time format")
			return
		}
	}

	limit := 1000 // Default limit
	if limitStr != "" {
		var n int
		for _, c := range limitStr {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		if n > 0 && n <= 10000 {
			limit = n
		}
	}

	params := usage.QueryParams{
		TenantID:  tenantID,
		UserID:    userID,
		StartTime: start,
		EndTime:   end,
		Limit:     limit,
	}

	exporter := usage.NewExporter(s.usage)

	// Set appropriate content type
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=usage.csv")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}

	var exportFormat usage.ExportFormat
	if format == "csv" {
		exportFormat = usage.FormatCSV
	} else {
		exportFormat = usage.FormatJSON
	}

	if err := exporter.Export(w, params, exportFormat); err != nil {
		// Headers already sent, can't send error response
		return
	}
}
