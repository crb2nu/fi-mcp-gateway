package auth

import (
	"net/http"
)

// CompositeAuthenticator tries multiple authenticators in order.
// The first one to return a valid principal wins.
type CompositeAuthenticator struct {
	authenticators []Authenticator
	required       bool
}

// NewCompositeAuthenticator creates an authenticator that tries multiple methods.
// Authenticators are tried in order until one succeeds.
func NewCompositeAuthenticator(required bool, authenticators ...Authenticator) *CompositeAuthenticator {
	return &CompositeAuthenticator{
		authenticators: authenticators,
		required:       required,
	}
}

// Authenticate tries each authenticator in order.
// Returns the first successful principal, or an error if all fail and required=true.
func (c *CompositeAuthenticator) Authenticate(r *http.Request) (*Principal, error) {
	var lastErr error

	for _, auth := range c.authenticators {
		if auth == nil {
			continue
		}

		principal, err := auth.Authenticate(r)
		if err != nil {
			// Save error but continue to next authenticator
			lastErr = err
			continue
		}

		if principal != nil {
			// Success! Return this principal
			return principal, nil
		}

		// nil principal, nil error means "no credentials for this method"
		// Continue to next authenticator
	}

	// No authenticator succeeded
	if c.required {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, ErrUnauthorized
	}

	// Not required, return nil principal (anonymous access)
	return nil, nil
}

// AuthenticatorBuilder helps construct composite authenticators.
type AuthenticatorBuilder struct {
	authenticators []Authenticator
	required       bool
}

// NewAuthenticatorBuilder creates a builder for composing authenticators.
func NewAuthenticatorBuilder() *AuthenticatorBuilder {
	return &AuthenticatorBuilder{}
}

// WithJWT adds a JWT authenticator to the chain.
func (b *AuthenticatorBuilder) WithJWT(auth Authenticator) *AuthenticatorBuilder {
	if auth != nil {
		b.authenticators = append(b.authenticators, auth)
	}
	return b
}

// WithAPIKey adds an API key authenticator to the chain.
func (b *AuthenticatorBuilder) WithAPIKey(auth Authenticator) *AuthenticatorBuilder {
	if auth != nil {
		b.authenticators = append(b.authenticators, auth)
	}
	return b
}

// WithAuthenticator adds any authenticator to the chain.
func (b *AuthenticatorBuilder) WithAuthenticator(auth Authenticator) *AuthenticatorBuilder {
	if auth != nil {
		b.authenticators = append(b.authenticators, auth)
	}
	return b
}

// Required sets whether authentication is required.
func (b *AuthenticatorBuilder) Required(required bool) *AuthenticatorBuilder {
	b.required = required
	return b
}

// Build creates the composite authenticator.
func (b *AuthenticatorBuilder) Build() Authenticator {
	if len(b.authenticators) == 0 {
		return NoAuth{}
	}
	if len(b.authenticators) == 1 {
		return b.authenticators[0]
	}
	return NewCompositeAuthenticator(b.required, b.authenticators...)
}
