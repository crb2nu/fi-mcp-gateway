// Package tenant provides multi-tenancy support for the MCP gateway.
//
// Tenants are resolved from JWT claims or HTTP headers and propagated
// through request context for use in rate limiting, metrics, and policy.
package tenant

import (
	"os"
	"strings"
)

// Tenant represents an organization/tenant in the system.
type Tenant struct {
	// ID is the unique tenant identifier
	ID string

	// Name is the human-readable tenant name (optional)
	Name string

	// Plan is the subscription tier (e.g., "free", "pro", "enterprise")
	Plan string

	// Metadata holds additional tenant attributes
	Metadata map[string]string
}

// IsValid returns true if the tenant has a non-empty ID.
func (t Tenant) IsValid() bool {
	return strings.TrimSpace(t.ID) != ""
}

// DefaultTenant returns a tenant used when no tenant can be resolved.
func DefaultTenant() Tenant {
	return Tenant{
		ID:   "default",
		Plan: "free",
	}
}

// Config holds multi-tenancy configuration.
type Config struct {
	// Enabled controls whether tenant isolation is enforced
	Enabled bool

	// Required makes tenant identification mandatory (reject requests without tenant)
	Required bool

	// DefaultTenantID is used when no tenant can be resolved and Required is false
	DefaultTenantID string

	// JWTClaim is the JWT claim containing the tenant ID
	JWTClaim string

	// JWTNameClaim is the JWT claim containing the tenant name (optional)
	JWTNameClaim string

	// JWTPlanClaim is the JWT claim containing the subscription plan (optional)
	JWTPlanClaim string

	// HeaderName is the HTTP header for explicit tenant specification
	HeaderName string

	// AllowHeaderOverride allows the header to override JWT-extracted tenant
	AllowHeaderOverride bool
}

// LoadConfigFromEnv loads tenant configuration from environment variables.
func LoadConfigFromEnv() Config {
	return Config{
		Enabled:             envBoolDefault("FI_MCP_TENANT_ENABLED", false),
		Required:            envBoolDefault("FI_MCP_TENANT_REQUIRED", false),
		DefaultTenantID:     envDefault("FI_MCP_TENANT_DEFAULT_ID", "default"),
		JWTClaim:            envDefault("FI_MCP_TENANT_JWT_CLAIM", "tenant_id"),
		JWTNameClaim:        envDefault("FI_MCP_TENANT_JWT_NAME_CLAIM", "tenant_name"),
		JWTPlanClaim:        envDefault("FI_MCP_TENANT_JWT_PLAN_CLAIM", "tenant_plan"),
		HeaderName:          envDefault("FI_MCP_TENANT_HEADER", "X-Tenant-ID"),
		AllowHeaderOverride: envBoolDefault("FI_MCP_TENANT_ALLOW_HEADER_OVERRIDE", false),
	}
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBoolDefault(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "true" || v == "1" || v == "yes"
}
