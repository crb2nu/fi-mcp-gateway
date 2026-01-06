// Package apikeys provides API key management for the MCP gateway.
//
// API keys offer an alternative to JWT authentication, useful for
// service-to-service communication and CLI tools.
package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"
)

// APIKey represents an API key with its metadata.
type APIKey struct {
	// ID is the unique identifier (UUID)
	ID string
	// TenantID is the owning tenant
	TenantID string
	// UserID is the user who created the key (optional)
	UserID string
	// Name is a human-readable name for the key
	Name string
	// KeyHash is the SHA-256 hash of the key (never store plaintext)
	KeyHash string
	// KeyPrefix is the first few characters for identification (e.g., "fmg_abc...")
	KeyPrefix string
	// Scopes defines what operations the key can perform
	Scopes []string
	// ExpiresAt is when the key expires (nil = never)
	ExpiresAt *time.Time
	// CreatedAt is when the key was created
	CreatedAt time.Time
	// LastUsedAt is when the key was last used (nil = never)
	LastUsedAt *time.Time
	// RevokedAt is when the key was revoked (nil = active)
	RevokedAt *time.Time
}

// IsValid returns true if the key is active and not expired.
func (k APIKey) IsValid() bool {
	if k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return false
	}
	return true
}

// IsExpired returns true if the key has expired.
func (k APIKey) IsExpired() bool {
	return k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt)
}

// IsRevoked returns true if the key has been revoked.
func (k APIKey) IsRevoked() bool {
	return k.RevokedAt != nil
}

// HasScope returns true if the key has the specified scope.
func (k APIKey) HasScope(scope string) bool {
	// Empty scopes means full access
	if len(k.Scopes) == 0 {
		return true
	}
	for _, s := range k.Scopes {
		if s == scope || s == "*" {
			return true
		}
	}
	return false
}

// CreateKeyRequest holds parameters for creating a new API key.
type CreateKeyRequest struct {
	TenantID  string
	UserID    string
	Name      string
	Scopes    []string
	ExpiresIn time.Duration // 0 = never expires
}

// CreateKeyResult contains the created key and the plaintext key (shown once).
type CreateKeyResult struct {
	Key          APIKey
	PlaintextKey string // Only returned on creation, never stored
}

// Config holds API key configuration.
type Config struct {
	// Enabled controls whether API key auth is active
	Enabled bool
	// Prefix is prepended to all generated keys (e.g., "fmg_")
	Prefix string
	// Store is the storage backend: "postgres" or "memory"
	Store string
	// PostgresURL is the connection string for Postgres store
	PostgresURL string
	// DefaultExpiry is the default key expiration (0 = never)
	DefaultExpiry time.Duration
	// MaxKeysPerUser limits keys per user (0 = unlimited)
	MaxKeysPerUser int
}

// LoadConfigFromEnv loads API key configuration from environment variables.
func LoadConfigFromEnv() Config {
	return Config{
		Enabled:        envBoolDefault("FI_MCP_APIKEY_ENABLED", false),
		Prefix:         envDefault("FI_MCP_APIKEY_PREFIX", "fmg_"),
		Store:          envDefault("FI_MCP_APIKEY_STORE", "memory"),
		PostgresURL:    os.Getenv("FI_MCP_APIKEY_POSTGRES_URL"),
		DefaultExpiry:  envDurationDefault("FI_MCP_APIKEY_DEFAULT_EXPIRY", 0),
		MaxKeysPerUser: envIntDefault("FI_MCP_APIKEY_MAX_PER_USER", 10),
	}
}

// Manager is the interface for API key management.
type Manager interface {
	// Create generates a new API key.
	Create(ctx context.Context, req CreateKeyRequest) (CreateKeyResult, error)
	// Get retrieves a key by ID.
	Get(ctx context.Context, id string) (APIKey, error)
	// List returns all keys for a tenant/user.
	List(ctx context.Context, tenantID, userID string) ([]APIKey, error)
	// Validate checks if a plaintext key is valid and returns the key metadata.
	Validate(ctx context.Context, plaintextKey string) (APIKey, error)
	// Revoke marks a key as revoked.
	Revoke(ctx context.Context, id string) error
	// Rotate creates a new key with the same metadata and revokes the old one.
	Rotate(ctx context.Context, id string) (CreateKeyResult, error)
	// UpdateLastUsed records the last usage time.
	UpdateLastUsed(ctx context.Context, id string) error
	// Close releases resources.
	Close() error
}

// Store is the storage interface for API keys.
type Store interface {
	// Create stores a new API key.
	Create(ctx context.Context, key APIKey) error
	// Get retrieves a key by ID.
	Get(ctx context.Context, id string) (APIKey, error)
	// GetByHash retrieves a key by its hash.
	GetByHash(ctx context.Context, keyHash string) (APIKey, error)
	// List returns all keys for a tenant/user.
	List(ctx context.Context, tenantID, userID string) ([]APIKey, error)
	// Update modifies an existing key.
	Update(ctx context.Context, key APIKey) error
	// Delete removes a key.
	Delete(ctx context.Context, id string) error
	// CountByUser returns the number of active keys for a user.
	CountByUser(ctx context.Context, tenantID, userID string) (int, error)
	// Close releases resources.
	Close() error
}

// Common errors
var (
	ErrKeyNotFound    = errors.New("api key not found")
	ErrKeyRevoked     = errors.New("api key has been revoked")
	ErrKeyExpired     = errors.New("api key has expired")
	ErrInvalidKey     = errors.New("invalid api key format")
	ErrTooManyKeys    = errors.New("maximum keys per user exceeded")
	ErrInvalidRequest = errors.New("invalid key request")
)

// GenerateKey creates a new random API key with the given prefix.
func GenerateKey(prefix string) (plaintext, hash string, err error) {
	// Generate 32 random bytes
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}

	// Create the plaintext key
	plaintext = prefix + hex.EncodeToString(bytes)

	// Hash it for storage
	hashBytes := sha256.Sum256([]byte(plaintext))
	hash = hex.EncodeToString(hashBytes[:])

	return plaintext, hash, nil
}

// HashKey returns the SHA-256 hash of a plaintext key.
func HashKey(plaintextKey string) string {
	hashBytes := sha256.Sum256([]byte(plaintextKey))
	return hex.EncodeToString(hashBytes[:])
}

// ExtractPrefix returns the prefix portion of a key for identification.
func ExtractPrefix(plaintextKey string, prefixLen int) string {
	if len(plaintextKey) <= prefixLen {
		return plaintextKey
	}
	return plaintextKey[:prefixLen]
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

func envIntDefault(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	var n int
	if _, err := strings.NewReader(v).Read([]byte{byte(n)}); err != nil {
		return fallback
	}
	return n
}

func envDurationDefault(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return fallback
	}
	return d
}
