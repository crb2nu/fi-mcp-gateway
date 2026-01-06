package tenant

import (
	"errors"
	"net/http"
	"strings"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
)

var (
	// ErrTenantRequired is returned when tenant is required but not found
	ErrTenantRequired = errors.New("tenant identification required")

	// ErrInvalidTenant is returned when tenant ID is invalid
	ErrInvalidTenant = errors.New("invalid tenant identifier")
)

// Resolver extracts tenant information from requests.
type Resolver struct {
	cfg Config
}

// NewResolver creates a new tenant resolver.
func NewResolver(cfg Config) *Resolver {
	return &Resolver{cfg: cfg}
}

// Resolve extracts tenant from the request, using principal claims and headers.
// Resolution order:
// 1. JWT claims (if principal is provided)
// 2. HTTP header (if AllowHeaderOverride or no JWT tenant)
// 3. Default tenant (if not Required)
func (r *Resolver) Resolve(req *http.Request, principal *auth.Principal) (Tenant, error) {
	if !r.cfg.Enabled {
		return DefaultTenant(), nil
	}

	var tenant Tenant

	// Try JWT claims first
	if principal != nil && principal.Claims != nil {
		tenant = r.extractFromClaims(principal.Claims)
	}

	// Try header if no JWT tenant or override is allowed
	headerTenant := r.extractFromHeader(req)
	if headerTenant.IsValid() {
		if !tenant.IsValid() || r.cfg.AllowHeaderOverride {
			tenant = headerTenant
		}
	}

	// Use default if still no tenant
	if !tenant.IsValid() {
		if r.cfg.Required {
			return Tenant{}, ErrTenantRequired
		}
		tenant = Tenant{
			ID:   r.cfg.DefaultTenantID,
			Plan: "free",
		}
	}

	return tenant, nil
}

// ResolveFromPrincipal extracts tenant from principal claims only.
// Used for WebSocket connections where headers aren't available after upgrade.
func (r *Resolver) ResolveFromPrincipal(principal *auth.Principal) (Tenant, error) {
	if !r.cfg.Enabled {
		return DefaultTenant(), nil
	}

	var tenant Tenant

	if principal != nil && principal.Claims != nil {
		tenant = r.extractFromClaims(principal.Claims)
	}

	if !tenant.IsValid() {
		if r.cfg.Required {
			return Tenant{}, ErrTenantRequired
		}
		tenant = Tenant{
			ID:   r.cfg.DefaultTenantID,
			Plan: "free",
		}
	}

	return tenant, nil
}

// extractFromClaims extracts tenant info from JWT claims.
func (r *Resolver) extractFromClaims(claims map[string]any) Tenant {
	tenant := Tenant{
		Metadata: make(map[string]string),
	}

	// Extract tenant ID
	if id := r.getStringClaim(claims, r.cfg.JWTClaim); id != "" {
		tenant.ID = id
	}

	// Extract optional name
	if name := r.getStringClaim(claims, r.cfg.JWTNameClaim); name != "" {
		tenant.Name = name
	}

	// Extract optional plan
	if plan := r.getStringClaim(claims, r.cfg.JWTPlanClaim); plan != "" {
		tenant.Plan = plan
	}

	return tenant
}

// extractFromHeader extracts tenant ID from HTTP header.
func (r *Resolver) extractFromHeader(req *http.Request) Tenant {
	if req == nil || r.cfg.HeaderName == "" {
		return Tenant{}
	}

	id := strings.TrimSpace(req.Header.Get(r.cfg.HeaderName))
	if id == "" {
		return Tenant{}
	}

	return Tenant{ID: id}
}

// getStringClaim safely extracts a string claim value.
func (r *Resolver) getStringClaim(claims map[string]any, key string) string {
	if key == "" {
		return ""
	}

	val, ok := claims[key]
	if !ok {
		return ""
	}

	switch v := val.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// Config returns the resolver's configuration.
func (r *Resolver) Config() Config {
	return r.cfg
}

// Enabled returns whether multi-tenancy is enabled.
func (r *Resolver) Enabled() bool {
	return r.cfg.Enabled
}
