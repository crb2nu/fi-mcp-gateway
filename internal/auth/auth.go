package auth

import (
	"context"
	"errors"
	"net/http"
)

var ErrUnauthorized = errors.New("unauthorized")

// contextKey is a private type for context keys to prevent collisions.
type contextKey int

const principalKey contextKey = iota

type Principal struct {
	Subject  string
	Issuer   string
	Audience []string
	Claims   map[string]any
}

// TenantID returns the tenant_id claim if present.
func (p *Principal) TenantID() string {
	if p == nil || p.Claims == nil {
		return ""
	}
	if id, ok := p.Claims["tenant_id"].(string); ok {
		return id
	}
	return ""
}

type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, error)
}

type NoAuth struct{}

func (NoAuth) Authenticate(r *http.Request) (*Principal, error) {
	return nil, nil
}

// WithPrincipal returns a context with the principal attached.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFromContext extracts the principal from context.
func PrincipalFromContext(ctx context.Context) *Principal {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(principalKey).(*Principal)
	return p
}
