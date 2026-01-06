package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/apikeys"
)

func TestAPIKeyAuthenticator_XAPIKeyHeader(t *testing.T) {
	manager := setupTestManager(t)

	// Create a test key
	result, err := manager.Create(context.Background(), apikeys.CreateKeyRequest{
		TenantID: "tenant-1",
		UserID:   "user-1",
		Name:     "Test Key",
	})
	if err != nil {
		t.Fatalf("Create key failed: %v", err)
	}

	auth := NewAPIKeyAuthenticator(manager, true)

	// Test with X-API-Key header
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", result.PlaintextKey)

	principal, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if principal == nil {
		t.Fatal("principal should not be nil")
	}
	if principal.Subject != "user-1" {
		t.Errorf("Subject = %q, want %q", principal.Subject, "user-1")
	}
	if principal.TenantID() != "tenant-1" {
		t.Errorf("TenantID = %q, want %q", principal.TenantID(), "tenant-1")
	}
	if principal.Claims["auth_type"] != "apikey" {
		t.Errorf("auth_type = %v, want apikey", principal.Claims["auth_type"])
	}
}

func TestAPIKeyAuthenticator_BearerToken(t *testing.T) {
	manager := setupTestManager(t)

	result, _ := manager.Create(context.Background(), apikeys.CreateKeyRequest{
		TenantID: "tenant-2",
		UserID:   "user-2",
		Name:     "Bearer Key",
	})

	auth := NewAPIKeyAuthenticator(manager, true)

	// Test with Authorization: Bearer header
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+result.PlaintextKey)

	principal, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if principal == nil {
		t.Fatal("principal should not be nil")
	}
	if principal.TenantID() != "tenant-2" {
		t.Errorf("TenantID = %q, want %q", principal.TenantID(), "tenant-2")
	}
}

func TestAPIKeyAuthenticator_InvalidKey(t *testing.T) {
	manager := setupTestManager(t)
	auth := NewAPIKeyAuthenticator(manager, true)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "test_invalidkey")

	_, err := auth.Authenticate(req)
	if err != ErrUnauthorized {
		t.Errorf("Authenticate invalid key: err = %v, want ErrUnauthorized", err)
	}
}

func TestAPIKeyAuthenticator_MissingKey_Required(t *testing.T) {
	manager := setupTestManager(t)
	auth := NewAPIKeyAuthenticator(manager, true)

	req := httptest.NewRequest("GET", "/", nil)

	_, err := auth.Authenticate(req)
	if err != ErrUnauthorized {
		t.Errorf("Authenticate missing key (required): err = %v, want ErrUnauthorized", err)
	}
}

func TestAPIKeyAuthenticator_MissingKey_NotRequired(t *testing.T) {
	manager := setupTestManager(t)
	auth := NewAPIKeyAuthenticator(manager, false)

	req := httptest.NewRequest("GET", "/", nil)

	principal, err := auth.Authenticate(req)
	if err != nil {
		t.Errorf("Authenticate missing key (not required): err = %v", err)
	}
	if principal != nil {
		t.Error("principal should be nil when no key provided")
	}
}

func TestAPIKeyAuthenticator_RevokedKey(t *testing.T) {
	manager := setupTestManager(t)

	result, _ := manager.Create(context.Background(), apikeys.CreateKeyRequest{
		TenantID: "tenant-1",
		UserID:   "user-1",
		Name:     "Revoked Key",
	})

	// Revoke the key
	manager.Revoke(context.Background(), result.Key.ID)

	auth := NewAPIKeyAuthenticator(manager, true)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", result.PlaintextKey)

	_, err := auth.Authenticate(req)
	if err != ErrUnauthorized {
		t.Errorf("Authenticate revoked key: err = %v, want ErrUnauthorized", err)
	}
}

func TestAPIKeyAuthenticator_ExpiredKey(t *testing.T) {
	manager := setupTestManager(t)

	result, _ := manager.Create(context.Background(), apikeys.CreateKeyRequest{
		TenantID:  "tenant-1",
		UserID:    "user-1",
		Name:      "Expiring Key",
		ExpiresIn: time.Millisecond,
	})

	time.Sleep(10 * time.Millisecond)

	auth := NewAPIKeyAuthenticator(manager, true)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", result.PlaintextKey)

	_, err := auth.Authenticate(req)
	if err != ErrUnauthorized {
		t.Errorf("Authenticate expired key: err = %v, want ErrUnauthorized", err)
	}
}

func setupTestManager(t *testing.T) apikeys.Manager {
	t.Helper()
	manager, err := apikeys.New(apikeys.Config{
		Enabled: true,
		Prefix:  "test_",
		Store:   "memory",
	})
	if err != nil {
		t.Fatalf("New manager failed: %v", err)
	}
	t.Cleanup(func() { manager.Close() })
	return manager
}

func TestCompositeAuthenticator_FirstWins(t *testing.T) {
	// Create two mock authenticators
	auth1 := &mockAuthenticator{
		principal: &Principal{Subject: "from-auth1"},
	}
	auth2 := &mockAuthenticator{
		principal: &Principal{Subject: "from-auth2"},
	}

	composite := NewCompositeAuthenticator(true, auth1, auth2)

	req := httptest.NewRequest("GET", "/", nil)
	principal, err := composite.Authenticate(req)

	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if principal.Subject != "from-auth1" {
		t.Errorf("Subject = %q, want from-auth1", principal.Subject)
	}
}

func TestCompositeAuthenticator_Fallback(t *testing.T) {
	// First authenticator returns nil (no credentials)
	auth1 := &mockAuthenticator{principal: nil, err: nil}
	// Second authenticator succeeds
	auth2 := &mockAuthenticator{
		principal: &Principal{Subject: "from-auth2"},
	}

	composite := NewCompositeAuthenticator(true, auth1, auth2)

	req := httptest.NewRequest("GET", "/", nil)
	principal, err := composite.Authenticate(req)

	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if principal.Subject != "from-auth2" {
		t.Errorf("Subject = %q, want from-auth2", principal.Subject)
	}
}

func TestCompositeAuthenticator_AllFail_Required(t *testing.T) {
	auth1 := &mockAuthenticator{principal: nil, err: nil}
	auth2 := &mockAuthenticator{principal: nil, err: nil}

	composite := NewCompositeAuthenticator(true, auth1, auth2)

	req := httptest.NewRequest("GET", "/", nil)
	_, err := composite.Authenticate(req)

	if err != ErrUnauthorized {
		t.Errorf("Authenticate all fail (required): err = %v, want ErrUnauthorized", err)
	}
}

func TestCompositeAuthenticator_AllFail_NotRequired(t *testing.T) {
	auth1 := &mockAuthenticator{principal: nil, err: nil}
	auth2 := &mockAuthenticator{principal: nil, err: nil}

	composite := NewCompositeAuthenticator(false, auth1, auth2)

	req := httptest.NewRequest("GET", "/", nil)
	principal, err := composite.Authenticate(req)

	if err != nil {
		t.Errorf("Authenticate all fail (not required): err = %v", err)
	}
	if principal != nil {
		t.Error("principal should be nil")
	}
}

func TestCompositeAuthenticator_SkipErrors(t *testing.T) {
	// First authenticator returns error
	auth1 := &mockAuthenticator{err: ErrUnauthorized}
	// Second authenticator succeeds
	auth2 := &mockAuthenticator{
		principal: &Principal{Subject: "from-auth2"},
	}

	composite := NewCompositeAuthenticator(true, auth1, auth2)

	req := httptest.NewRequest("GET", "/", nil)
	principal, err := composite.Authenticate(req)

	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if principal.Subject != "from-auth2" {
		t.Errorf("Subject = %q, want from-auth2", principal.Subject)
	}
}

func TestAuthenticatorBuilder(t *testing.T) {
	auth1 := &mockAuthenticator{principal: &Principal{Subject: "jwt"}}
	auth2 := &mockAuthenticator{principal: &Principal{Subject: "apikey"}}

	authenticator := NewAuthenticatorBuilder().
		WithJWT(auth1).
		WithAPIKey(auth2).
		Required(true).
		Build()

	req := httptest.NewRequest("GET", "/", nil)
	principal, err := authenticator.Authenticate(req)

	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if principal.Subject != "jwt" {
		t.Errorf("Subject = %q, want jwt", principal.Subject)
	}
}

func TestAuthenticatorBuilder_Empty(t *testing.T) {
	authenticator := NewAuthenticatorBuilder().Build()

	// Should return NoAuth
	req := httptest.NewRequest("GET", "/", nil)
	principal, err := authenticator.Authenticate(req)

	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if principal != nil {
		t.Error("NoAuth should return nil principal")
	}
}

func TestAuthenticatorBuilder_Single(t *testing.T) {
	auth1 := &mockAuthenticator{principal: &Principal{Subject: "single"}}

	authenticator := NewAuthenticatorBuilder().
		WithJWT(auth1).
		Build()

	// Should return the single authenticator directly, not wrapped
	req := httptest.NewRequest("GET", "/", nil)
	principal, _ := authenticator.Authenticate(req)

	if principal.Subject != "single" {
		t.Errorf("Subject = %q, want single", principal.Subject)
	}
}

// mockAuthenticator is a test double for Authenticator.
type mockAuthenticator struct {
	principal *Principal
	err       error
}

func (m *mockAuthenticator) Authenticate(r *http.Request) (*Principal, error) {
	return m.principal, m.err
}
