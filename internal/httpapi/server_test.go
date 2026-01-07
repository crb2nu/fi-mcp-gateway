package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/apikeys"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/billing"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/quota"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/usage"
)

// Mock implementations

type mockAuthenticator struct {
	principal *auth.Principal
	err       error
}

func (m *mockAuthenticator) Authenticate(r *http.Request) (*auth.Principal, error) {
	return m.principal, m.err
}

type mockAPIKeysManager struct {
	keys       map[string]apikeys.APIKey
	createErr  error
	listErr    error
	getErr     error
	revokeErr  error
	rotateErr  error
	createResp *apikeys.CreateKeyResult
	rotateResp *apikeys.CreateKeyResult
}

func newMockAPIKeysManager() *mockAPIKeysManager {
	return &mockAPIKeysManager{keys: make(map[string]apikeys.APIKey)}
}

func (m *mockAPIKeysManager) Create(ctx context.Context, req apikeys.CreateKeyRequest) (apikeys.CreateKeyResult, error) {
	if m.createErr != nil {
		return apikeys.CreateKeyResult{}, m.createErr
	}
	if m.createResp != nil {
		return *m.createResp, nil
	}
	now := time.Now()
	key := apikeys.APIKey{
		ID:        "key-123",
		TenantID:  req.TenantID,
		UserID:    req.UserID,
		Name:      req.Name,
		KeyPrefix: "fimcp_abc...",
		Scopes:    req.Scopes,
		CreatedAt: now,
	}
	m.keys[key.ID] = key
	return apikeys.CreateKeyResult{
		Key:          key,
		PlaintextKey: "fimcp_abcdef123456",
	}, nil
}

func (m *mockAPIKeysManager) Get(ctx context.Context, id string) (apikeys.APIKey, error) {
	if m.getErr != nil {
		return apikeys.APIKey{}, m.getErr
	}
	key, ok := m.keys[id]
	if !ok {
		return apikeys.APIKey{}, apikeys.ErrKeyNotFound
	}
	return key, nil
}

func (m *mockAPIKeysManager) GetByHash(ctx context.Context, hash string) (apikeys.APIKey, error) {
	return apikeys.APIKey{}, nil
}

func (m *mockAPIKeysManager) List(ctx context.Context, tenantID, userID string) ([]apikeys.APIKey, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var result []apikeys.APIKey
	for _, k := range m.keys {
		if k.TenantID == tenantID && k.UserID == userID {
			result = append(result, k)
		}
	}
	return result, nil
}

func (m *mockAPIKeysManager) Revoke(ctx context.Context, id string) error {
	if m.revokeErr != nil {
		return m.revokeErr
	}
	if _, ok := m.keys[id]; !ok {
		return apikeys.ErrKeyNotFound
	}
	now := time.Now()
	key := m.keys[id]
	key.RevokedAt = &now
	m.keys[id] = key
	return nil
}

func (m *mockAPIKeysManager) Rotate(ctx context.Context, id string) (apikeys.CreateKeyResult, error) {
	if m.rotateErr != nil {
		return apikeys.CreateKeyResult{}, m.rotateErr
	}
	if m.rotateResp != nil {
		return *m.rotateResp, nil
	}
	key, ok := m.keys[id]
	if !ok {
		return apikeys.CreateKeyResult{}, apikeys.ErrKeyNotFound
	}
	newKey := key
	newKey.ID = "key-456"
	newKey.KeyPrefix = "fimcp_xyz..."
	m.keys[newKey.ID] = newKey
	return apikeys.CreateKeyResult{
		Key:          newKey,
		PlaintextKey: "fimcp_newkey789",
	}, nil
}

func (m *mockAPIKeysManager) Validate(ctx context.Context, plaintextKey string) (apikeys.APIKey, error) {
	return apikeys.APIKey{}, nil
}

func (m *mockAPIKeysManager) UpdateLastUsed(ctx context.Context, id string) error {
	return nil
}

func (m *mockAPIKeysManager) Close() error {
	return nil
}

type mockQuotaManager struct {
	quotas map[quota.QuotaType]quota.Quota
	usages map[quota.QuotaType]quota.Usage
}

func newMockQuotaManager() *mockQuotaManager {
	return &mockQuotaManager{
		quotas: make(map[quota.QuotaType]quota.Quota),
		usages: make(map[quota.QuotaType]quota.Usage),
	}
}

func (m *mockQuotaManager) Check(ctx context.Context, tenantID, userID string, quotaType quota.QuotaType, cost int64) (quota.CheckResult, error) {
	return quota.CheckResult{Allowed: true}, nil
}

func (m *mockQuotaManager) Increment(ctx context.Context, tenantID, userID string, quotaType quota.QuotaType, amount int64) error {
	return nil
}

func (m *mockQuotaManager) GetUsage(ctx context.Context, tenantID, userID string, quotaType quota.QuotaType) (quota.Usage, error) {
	u, ok := m.usages[quotaType]
	if !ok {
		return quota.Usage{}, quota.ErrQuotaNotFound
	}
	return u, nil
}

func (m *mockQuotaManager) SetQuota(ctx context.Context, q quota.Quota) error {
	m.quotas[q.Type] = q
	return nil
}

func (m *mockQuotaManager) GetQuota(ctx context.Context, tenantID, userID string, quotaType quota.QuotaType) (quota.Quota, error) {
	q, ok := m.quotas[quotaType]
	if !ok {
		return quota.Quota{}, quota.ErrQuotaNotFound
	}
	return q, nil
}

func (m *mockQuotaManager) SetWebhookSender(sender billing.WebhookSender) {}

func (m *mockQuotaManager) Close() error {
	return nil
}

type mockUsageTracker struct {
	events  []usage.Event
	summary usage.Summary
}

func (m *mockUsageTracker) Track(ctx context.Context, event usage.Event) {
	m.events = append(m.events, event)
}

func (m *mockUsageTracker) Query(ctx context.Context, params usage.QueryParams) ([]usage.Event, error) {
	return m.events, nil
}

func (m *mockUsageTracker) GetSummary(ctx context.Context, tenantID, userID string, start, end time.Time) (usage.Summary, error) {
	return m.summary, nil
}

func (m *mockUsageTracker) Close() error {
	return nil
}

// Helper functions

func testPrincipal() *auth.Principal {
	return &auth.Principal{
		Subject: "user-123",
		Claims: map[string]any{
			"tenant_id": "tenant-abc",
		},
	}
}

func createTestServer(opts ...func(*Config)) *Server {
	reg := &registry.Registry{
		Servers: []*registry.Server{
			{Name: "test-server", Categories: []string{"test"}},
			{Name: "dev-server", Categories: []string{"dev"}},
		},
	}

	cfg := Config{
		Registry: reg,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	return New(cfg)
}

// Tests

func TestHealthEndpoint(t *testing.T) {
	srv := createTestServer()
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", resp["status"])
	}

	if _, ok := resp["timestamp"]; !ok {
		t.Error("expected timestamp in response")
	}
}

func TestReadyEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		servers    int
		wantStatus int
		wantReady  bool
	}{
		{
			name:       "ready with servers",
			servers:    2,
			wantStatus: http.StatusOK,
			wantReady:  true,
		},
		{
			name:       "not ready without servers",
			servers:    0,
			wantStatus: http.StatusServiceUnavailable,
			wantReady:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &registry.Registry{}
			for i := 0; i < tt.servers; i++ {
				reg.Servers = append(reg.Servers, &registry.Server{Name: "test"})
			}

			srv := New(Config{Registry: reg})
			handler := srv.Handler()

			req := httptest.NewRequest(http.MethodGet, "/ready", nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}

			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}

			if resp["ready"] != tt.wantReady {
				t.Errorf("expected ready=%v, got %v", tt.wantReady, resp["ready"])
			}
		})
	}
}

func TestServersEndpoint(t *testing.T) {
	srv := createTestServer()
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/servers", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	servers, ok := resp["servers"].([]any)
	if !ok {
		t.Fatal("expected servers array in response")
	}

	if len(servers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(servers))
	}
}

func TestCreateKeyEndpoint(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockKeys := newMockAPIKeysManager()

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.APIKeys = mockKeys
	})
	handler := srv.Handler()

	body := `{"name": "test-key", "scopes": ["read", "write"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["key"] == nil {
		t.Error("expected 'key' in response")
	}

	if resp["key_id"] == nil {
		t.Error("expected 'key_id' in response")
	}
}

func TestCreateKeyEndpoint_Unauthorized(t *testing.T) {
	mockAuth := &mockAuthenticator{err: auth.ErrUnauthorized}
	mockKeys := newMockAPIKeysManager()

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.APIKeys = mockKeys
	})
	handler := srv.Handler()

	body := `{"name": "test-key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestCreateKeyEndpoint_MissingName(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockKeys := newMockAPIKeysManager()

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.APIKeys = mockKeys
	})
	handler := srv.Handler()

	body := `{"scopes": ["read"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestListKeysEndpoint(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockKeys := newMockAPIKeysManager()

	// Create some test keys
	mockKeys.keys["key-1"] = apikeys.APIKey{
		ID:       "key-1",
		TenantID: "tenant-abc",
		UserID:   "user-123",
		Name:     "Key 1",
	}
	mockKeys.keys["key-2"] = apikeys.APIKey{
		ID:       "key-2",
		TenantID: "tenant-abc",
		UserID:   "user-123",
		Name:     "Key 2",
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.APIKeys = mockKeys
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	keys, ok := resp["keys"].([]any)
	if !ok {
		t.Fatal("expected 'keys' array in response")
	}

	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestGetKeyEndpoint(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockKeys := newMockAPIKeysManager()

	mockKeys.keys["key-123"] = apikeys.APIKey{
		ID:       "key-123",
		TenantID: "tenant-abc",
		UserID:   "user-123",
		Name:     "Test Key",
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.APIKeys = mockKeys
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/keys/key-123", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["id"] != "key-123" {
		t.Errorf("expected id 'key-123', got %v", resp["id"])
	}
}

func TestGetKeyEndpoint_NotFound(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockKeys := newMockAPIKeysManager()

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.APIKeys = mockKeys
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/keys/nonexistent", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestRevokeKeyEndpoint(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockKeys := newMockAPIKeysManager()

	mockKeys.keys["key-123"] = apikeys.APIKey{
		ID:       "key-123",
		TenantID: "tenant-abc",
		UserID:   "user-123",
		Name:     "Test Key",
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.APIKeys = mockKeys
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/keys/key-123", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify key was revoked
	if mockKeys.keys["key-123"].RevokedAt == nil {
		t.Error("expected key to be revoked")
	}
}

func TestRotateKeyEndpoint(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockKeys := newMockAPIKeysManager()

	mockKeys.keys["key-123"] = apikeys.APIKey{
		ID:       "key-123",
		TenantID: "tenant-abc",
		UserID:   "user-123",
		Name:     "Test Key",
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.APIKeys = mockKeys
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/key-123/rotate", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["key"] == nil {
		t.Error("expected new 'key' in response")
	}

	if resp["old_key_id"] != "key-123" {
		t.Errorf("expected old_key_id 'key-123', got %v", resp["old_key_id"])
	}
}

func TestGetQuotasEndpoint(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockQuotas := newMockQuotaManager()

	// Set up some quotas
	mockQuotas.quotas[quota.QuotaTypeToolCalls] = quota.Quota{
		Type:   quota.QuotaTypeToolCalls,
		Limit:  1000,
		Period: quota.PeriodDaily,
	}
	mockQuotas.usages[quota.QuotaTypeToolCalls] = quota.Usage{
		Current: 100,
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.Quotas = mockQuotas
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/quotas", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["tenant_id"] != "tenant-abc" {
		t.Errorf("expected tenant_id 'tenant-abc', got %v", resp["tenant_id"])
	}

	quotas, ok := resp["quotas"].([]any)
	if !ok {
		t.Fatal("expected 'quotas' array in response")
	}

	if len(quotas) != 1 {
		t.Errorf("expected 1 quota, got %d", len(quotas))
	}
}

func TestGetUsageEndpoint(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockUsage := &mockUsageTracker{
		summary: usage.Summary{
			TotalEvents:  100,
			SuccessCount: 95,
			ErrorCount:   5,
			ToolBreakdown: map[string]int64{
				"tool1": 50,
				"tool2": 50,
			},
		},
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.Usage = mockUsage
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["total_events"].(float64) != 100 {
		t.Errorf("expected total_events=100, got %v", resp["total_events"])
	}

	if resp["success_count"].(float64) != 95 {
		t.Errorf("expected success_count=95, got %v", resp["success_count"])
	}
}

func TestGetUsageEndpoint_WithTimeRange(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockUsage := &mockUsageTracker{
		summary: usage.Summary{TotalEvents: 50},
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.Usage = mockUsage
	})
	handler := srv.Handler()

	start := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	end := time.Now().Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage?start="+start+"&end="+end, nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetUsageEndpoint_InvalidTimeFormat(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockUsage := &mockUsageTracker{}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.Usage = mockUsage
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage?start=invalid", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestExportUsageEndpoint_JSON(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockUsage := &mockUsageTracker{
		events: []usage.Event{
			{ID: "evt-1", ToolName: "tool1", Success: true},
			{ID: "evt-2", ToolName: "tool2", Success: false},
		},
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.Usage = mockUsage
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?format=json", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %s", contentType)
	}
}

func TestExportUsageEndpoint_CSV(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockUsage := &mockUsageTracker{
		events: []usage.Event{
			{ID: "evt-1", ToolName: "tool1", Success: true},
		},
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.Usage = mockUsage
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?format=csv", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType != "text/csv" {
		t.Errorf("expected Content-Type 'text/csv', got %s", contentType)
	}
}

func TestExportUsageEndpoint_InvalidFormat(t *testing.T) {
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockUsage := &mockUsageTracker{}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.Usage = mockUsage
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?format=xml", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestKeyOwnershipVerification(t *testing.T) {
	// Test that users can't access other users' keys
	mockAuth := &mockAuthenticator{principal: testPrincipal()}
	mockKeys := newMockAPIKeysManager()

	// Create a key owned by a different user
	mockKeys.keys["other-key"] = apikeys.APIKey{
		ID:       "other-key",
		TenantID: "other-tenant",
		UserID:   "other-user",
		Name:     "Other User's Key",
	}

	srv := createTestServer(func(cfg *Config) {
		cfg.Authenticator = mockAuth
		cfg.APIKeys = mockKeys
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/keys/other-key", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Should return 404 to hide the existence of other users' keys
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404 for other user's key, got %d", rec.Code)
	}
}
