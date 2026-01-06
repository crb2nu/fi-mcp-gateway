package auth

import (
	"context"
	"net/http"
	"strings"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/apikeys"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/metrics"
)

// APIKeyAuthenticator validates API keys for authentication.
type APIKeyAuthenticator struct {
	manager  apikeys.Manager
	required bool
}

// NewAPIKeyAuthenticator creates a new API key authenticator.
func NewAPIKeyAuthenticator(manager apikeys.Manager, required bool) *APIKeyAuthenticator {
	return &APIKeyAuthenticator{
		manager:  manager,
		required: required,
	}
}

// Authenticate extracts and validates an API key from the request.
// It checks the X-API-Key header first, then falls back to Authorization header.
func (a *APIKeyAuthenticator) Authenticate(r *http.Request) (*Principal, error) {
	// Try X-API-Key header first (preferred for API keys)
	key := strings.TrimSpace(r.Header.Get("X-API-Key"))

	// Fall back to Authorization header with Bearer prefix
	if key == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(auth, "Bearer ") {
			key = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
	}

	if key == "" {
		if a.required {
			return nil, ErrUnauthorized
		}
		return nil, nil
	}

	// Validate the API key
	ctx := r.Context()
	apiKey, err := a.manager.Validate(ctx, key)
	if err != nil {
		tenant := extractTenantFromKey(key)
		metrics.APIKeyAuthTotal.WithLabelValues(tenant, "failure").Inc()

		if a.required {
			return nil, ErrUnauthorized
		}
		return nil, nil
	}

	// Update last used time asynchronously
	go func() {
		_ = a.manager.UpdateLastUsed(context.Background(), apiKey.ID)
	}()

	metrics.APIKeyAuthTotal.WithLabelValues(apiKey.TenantID, "success").Inc()

	// Build principal from API key
	return &Principal{
		Subject:  apiKey.UserID,
		Issuer:   "apikey",
		Audience: []string{apiKey.TenantID},
		Claims: map[string]any{
			"tenant_id": apiKey.TenantID,
			"user_id":   apiKey.UserID,
			"key_id":    apiKey.ID,
			"key_name":  apiKey.Name,
			"scopes":    apiKey.Scopes,
			"auth_type": "apikey",
		},
	}, nil
}

// extractTenantFromKey tries to get tenant info for metrics even on failure.
// This is best-effort and returns "unknown" if extraction fails.
func extractTenantFromKey(key string) string {
	// Keys have format: prefix_randomhex
	// We can't extract tenant from the key itself, so return unknown
	return "unknown"
}
