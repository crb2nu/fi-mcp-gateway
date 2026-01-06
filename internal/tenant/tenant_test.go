package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
)

func TestTenant_IsValid(t *testing.T) {
	tests := []struct {
		name   string
		tenant Tenant
		want   bool
	}{
		{
			name:   "empty tenant",
			tenant: Tenant{},
			want:   false,
		},
		{
			name:   "whitespace ID",
			tenant: Tenant{ID: "   "},
			want:   false,
		},
		{
			name:   "valid tenant",
			tenant: Tenant{ID: "acme"},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tenant.IsValid(); got != tt.want {
				t.Errorf("Tenant.IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultTenant(t *testing.T) {
	tenant := DefaultTenant()
	if tenant.ID != "default" {
		t.Errorf("DefaultTenant().ID = %q, want %q", tenant.ID, "default")
	}
	if tenant.Plan != "free" {
		t.Errorf("DefaultTenant().Plan = %q, want %q", tenant.Plan, "free")
	}
}

func TestResolver_Resolve_Disabled(t *testing.T) {
	cfg := Config{Enabled: false}
	resolver := NewResolver(cfg)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	tenant, err := resolver.Resolve(req, nil)

	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if tenant.ID != "default" {
		t.Errorf("tenant.ID = %q, want %q", tenant.ID, "default")
	}
}

func TestResolver_Resolve_FromJWT(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		JWTClaim: "tenant_id",
	}
	resolver := NewResolver(cfg)

	principal := &auth.Principal{
		Subject: "user@example.com",
		Claims: map[string]any{
			"tenant_id": "acme-corp",
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	tenant, err := resolver.Resolve(req, principal)

	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if tenant.ID != "acme-corp" {
		t.Errorf("tenant.ID = %q, want %q", tenant.ID, "acme-corp")
	}
}

func TestResolver_Resolve_FromHeader(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		HeaderName: "X-Tenant-ID",
	}
	resolver := NewResolver(cfg)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-ID", "header-tenant")

	tenant, err := resolver.Resolve(req, nil)

	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if tenant.ID != "header-tenant" {
		t.Errorf("tenant.ID = %q, want %q", tenant.ID, "header-tenant")
	}
}

func TestResolver_Resolve_JWTOverHeader(t *testing.T) {
	cfg := Config{
		Enabled:             true,
		JWTClaim:            "tenant_id",
		HeaderName:          "X-Tenant-ID",
		AllowHeaderOverride: false,
	}
	resolver := NewResolver(cfg)

	principal := &auth.Principal{
		Claims: map[string]any{
			"tenant_id": "jwt-tenant",
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-ID", "header-tenant")

	tenant, err := resolver.Resolve(req, principal)

	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	// JWT should take precedence when AllowHeaderOverride is false
	if tenant.ID != "jwt-tenant" {
		t.Errorf("tenant.ID = %q, want %q", tenant.ID, "jwt-tenant")
	}
}

func TestResolver_Resolve_HeaderOverride(t *testing.T) {
	cfg := Config{
		Enabled:             true,
		JWTClaim:            "tenant_id",
		HeaderName:          "X-Tenant-ID",
		AllowHeaderOverride: true,
	}
	resolver := NewResolver(cfg)

	principal := &auth.Principal{
		Claims: map[string]any{
			"tenant_id": "jwt-tenant",
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-ID", "header-tenant")

	tenant, err := resolver.Resolve(req, principal)

	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	// Header should override JWT when AllowHeaderOverride is true
	if tenant.ID != "header-tenant" {
		t.Errorf("tenant.ID = %q, want %q", tenant.ID, "header-tenant")
	}
}

func TestResolver_Resolve_Required(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Required: true,
	}
	resolver := NewResolver(cfg)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := resolver.Resolve(req, nil)

	if err != ErrTenantRequired {
		t.Errorf("Resolve() error = %v, want %v", err, ErrTenantRequired)
	}
}

func TestResolver_Resolve_DefaultFallback(t *testing.T) {
	cfg := Config{
		Enabled:         true,
		Required:        false,
		DefaultTenantID: "fallback-tenant",
	}
	resolver := NewResolver(cfg)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	tenant, err := resolver.Resolve(req, nil)

	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if tenant.ID != "fallback-tenant" {
		t.Errorf("tenant.ID = %q, want %q", tenant.ID, "fallback-tenant")
	}
}

func TestResolver_ResolveFromPrincipal(t *testing.T) {
	cfg := Config{
		Enabled:      true,
		JWTClaim:     "tenant_id",
		JWTNameClaim: "tenant_name",
		JWTPlanClaim: "tenant_plan",
	}
	resolver := NewResolver(cfg)

	principal := &auth.Principal{
		Subject: "user@example.com",
		Claims: map[string]any{
			"tenant_id":   "acme",
			"tenant_name": "Acme Corporation",
			"tenant_plan": "enterprise",
		},
	}

	tenant, err := resolver.ResolveFromPrincipal(principal)

	if err != nil {
		t.Fatalf("ResolveFromPrincipal() error = %v", err)
	}
	if tenant.ID != "acme" {
		t.Errorf("tenant.ID = %q, want %q", tenant.ID, "acme")
	}
	if tenant.Name != "Acme Corporation" {
		t.Errorf("tenant.Name = %q, want %q", tenant.Name, "Acme Corporation")
	}
	if tenant.Plan != "enterprise" {
		t.Errorf("tenant.Plan = %q, want %q", tenant.Plan, "enterprise")
	}
}

func TestContext_WithTenant(t *testing.T) {
	tenant := Tenant{ID: "test-tenant", Plan: "pro"}
	ctx := WithTenant(context.Background(), tenant)

	got := FromContext(ctx)
	if got.ID != tenant.ID {
		t.Errorf("FromContext().ID = %q, want %q", got.ID, tenant.ID)
	}
	if got.Plan != tenant.Plan {
		t.Errorf("FromContext().Plan = %q, want %q", got.Plan, tenant.Plan)
	}
}

func TestContext_FromContext_Empty(t *testing.T) {
	ctx := context.Background()
	tenant := FromContext(ctx)

	// Should return default tenant
	if tenant.ID != "default" {
		t.Errorf("FromContext().ID = %q, want %q", tenant.ID, "default")
	}
}

func TestContext_FromContextOK(t *testing.T) {
	// With tenant set
	tenant := Tenant{ID: "test"}
	ctx := WithTenant(context.Background(), tenant)
	got, ok := FromContextOK(ctx)
	if !ok {
		t.Error("FromContextOK() ok = false, want true")
	}
	if got.ID != "test" {
		t.Errorf("FromContextOK().ID = %q, want %q", got.ID, "test")
	}

	// Without tenant set
	_, ok = FromContextOK(context.Background())
	if ok {
		t.Error("FromContextOK() ok = true for empty context, want false")
	}
}

func TestIDFromContext(t *testing.T) {
	tenant := Tenant{ID: "my-tenant"}
	ctx := WithTenant(context.Background(), tenant)

	id := IDFromContext(ctx)
	if id != "my-tenant" {
		t.Errorf("IDFromContext() = %q, want %q", id, "my-tenant")
	}
}
