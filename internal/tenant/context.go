package tenant

import (
	"context"
	"net/http"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
)

// contextKey is a private type for context keys to prevent collisions.
type contextKey int

const (
	tenantKey contextKey = iota
)

// WithTenant returns a new context with the tenant attached.
func WithTenant(ctx context.Context, t Tenant) context.Context {
	return context.WithValue(ctx, tenantKey, t)
}

// FromContext extracts the tenant from context.
// Returns the default tenant if none is set.
func FromContext(ctx context.Context) Tenant {
	if ctx == nil {
		return DefaultTenant()
	}
	t, ok := ctx.Value(tenantKey).(Tenant)
	if !ok {
		return DefaultTenant()
	}
	return t
}

// FromContextOK extracts the tenant from context with an existence check.
func FromContextOK(ctx context.Context) (Tenant, bool) {
	if ctx == nil {
		return Tenant{}, false
	}
	t, ok := ctx.Value(tenantKey).(Tenant)
	return t, ok
}

// IDFromContext is a convenience function that returns just the tenant ID.
func IDFromContext(ctx context.Context) string {
	return FromContext(ctx).ID
}

// Middleware creates HTTP middleware that resolves and injects tenant into context.
type Middleware struct {
	resolver *Resolver
}

// NewMiddleware creates tenant injection middleware.
func NewMiddleware(resolver *Resolver) *Middleware {
	return &Middleware{resolver: resolver}
}

// Handler wraps an HTTP handler with tenant resolution.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract principal from context (set by auth middleware)
		principal := principalFromContext(r.Context())

		tenant, err := m.resolver.Resolve(r, principal)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}

		// Inject tenant into context
		ctx := WithTenant(r.Context(), tenant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// HandlerFunc wraps an HTTP handler function.
func (m *Middleware) HandlerFunc(next http.HandlerFunc) http.HandlerFunc {
	return m.Handler(next).ServeHTTP
}

// principalFromContext extracts auth.Principal from context.
// This uses the auth package's context functions.
func principalFromContext(ctx context.Context) *auth.Principal {
	return auth.PrincipalFromContext(ctx)
}
