package apikeys

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/metrics"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/storage"
)

// DefaultManager implements the Manager interface.
type DefaultManager struct {
	store   Store
	cfg     Config
	enabled bool
}

// New creates a new API key manager from configuration.
func New(cfg Config) (*DefaultManager, error) {
	if !cfg.Enabled {
		return &DefaultManager{
			cfg:     cfg,
			enabled: false,
		}, nil
	}

	var store Store
	var err error

	switch cfg.Store {
	case "memory", "":
		store = NewMemoryStore()
	case "postgres":
		store, err = newPostgresStoreFromConfig(cfg)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown api key store: %s", cfg.Store)
	}

	return &DefaultManager{
		store:   store,
		cfg:     cfg,
		enabled: true,
	}, nil
}

// Create generates a new API key.
func (m *DefaultManager) Create(ctx context.Context, req CreateKeyRequest) (CreateKeyResult, error) {
	if !m.enabled {
		return CreateKeyResult{}, fmt.Errorf("api key management is disabled")
	}

	// Validate request
	if req.TenantID == "" {
		return CreateKeyResult{}, ErrInvalidRequest
	}
	if req.Name == "" {
		return CreateKeyResult{}, ErrInvalidRequest
	}

	// Check key limit
	if m.cfg.MaxKeysPerUser > 0 && req.UserID != "" {
		count, err := m.store.CountByUser(ctx, req.TenantID, req.UserID)
		if err != nil {
			return CreateKeyResult{}, err
		}
		if count >= m.cfg.MaxKeysPerUser {
			return CreateKeyResult{}, ErrTooManyKeys
		}
	}

	// Generate key
	plaintext, hash, err := GenerateKey(m.cfg.Prefix)
	if err != nil {
		return CreateKeyResult{}, fmt.Errorf("generate key: %w", err)
	}

	now := time.Now()
	key := APIKey{
		ID:        uuid.New().String(),
		TenantID:  req.TenantID,
		UserID:    req.UserID,
		Name:      req.Name,
		KeyHash:   hash,
		KeyPrefix: ExtractPrefix(plaintext, len(m.cfg.Prefix)+8),
		Scopes:    req.Scopes,
		CreatedAt: now,
	}

	// Set expiration
	if req.ExpiresIn > 0 {
		exp := now.Add(req.ExpiresIn)
		key.ExpiresAt = &exp
	} else if m.cfg.DefaultExpiry > 0 {
		exp := now.Add(m.cfg.DefaultExpiry)
		key.ExpiresAt = &exp
	}

	// Store key
	if err := m.store.Create(ctx, key); err != nil {
		return CreateKeyResult{}, err
	}

	metrics.APIKeysCreatedTotal.WithLabelValues(req.TenantID).Inc()

	return CreateKeyResult{
		Key:          key,
		PlaintextKey: plaintext,
	}, nil
}

// Get retrieves a key by ID.
func (m *DefaultManager) Get(ctx context.Context, id string) (APIKey, error) {
	if !m.enabled {
		return APIKey{}, ErrKeyNotFound
	}
	return m.store.Get(ctx, id)
}

// List returns all keys for a tenant/user.
func (m *DefaultManager) List(ctx context.Context, tenantID, userID string) ([]APIKey, error) {
	if !m.enabled {
		return nil, nil
	}
	return m.store.List(ctx, tenantID, userID)
}

// Validate checks if a plaintext key is valid and returns the key metadata.
func (m *DefaultManager) Validate(ctx context.Context, plaintextKey string) (APIKey, error) {
	if !m.enabled {
		return APIKey{}, ErrKeyNotFound
	}

	// Validate format
	if !hasPrefix(plaintextKey, m.cfg.Prefix) {
		return APIKey{}, ErrInvalidKey
	}

	// Look up by hash
	hash := HashKey(plaintextKey)
	key, err := m.store.GetByHash(ctx, hash)
	if err != nil {
		return APIKey{}, err
	}

	// Check validity
	if key.IsRevoked() {
		metrics.APIKeyAuthFailedTotal.WithLabelValues(key.TenantID, "revoked").Inc()
		return APIKey{}, ErrKeyRevoked
	}
	if key.IsExpired() {
		metrics.APIKeyAuthFailedTotal.WithLabelValues(key.TenantID, "expired").Inc()
		return APIKey{}, ErrKeyExpired
	}

	return key, nil
}

// Revoke marks a key as revoked.
func (m *DefaultManager) Revoke(ctx context.Context, id string) error {
	if !m.enabled {
		return fmt.Errorf("api key management is disabled")
	}

	key, err := m.store.Get(ctx, id)
	if err != nil {
		return err
	}

	now := time.Now()
	key.RevokedAt = &now

	if err := m.store.Update(ctx, key); err != nil {
		return err
	}

	metrics.APIKeysRevokedTotal.WithLabelValues(key.TenantID).Inc()
	return nil
}

// Rotate creates a new key with the same metadata and revokes the old one.
func (m *DefaultManager) Rotate(ctx context.Context, id string) (CreateKeyResult, error) {
	if !m.enabled {
		return CreateKeyResult{}, fmt.Errorf("api key management is disabled")
	}

	// Get existing key
	oldKey, err := m.store.Get(ctx, id)
	if err != nil {
		return CreateKeyResult{}, err
	}

	// Calculate remaining expiry
	var expiresIn time.Duration
	if oldKey.ExpiresAt != nil {
		remaining := time.Until(*oldKey.ExpiresAt)
		if remaining > 0 {
			expiresIn = remaining
		}
	}

	// Create new key with same metadata
	result, err := m.Create(ctx, CreateKeyRequest{
		TenantID:  oldKey.TenantID,
		UserID:    oldKey.UserID,
		Name:      oldKey.Name + " (rotated)",
		Scopes:    oldKey.Scopes,
		ExpiresIn: expiresIn,
	})
	if err != nil {
		return CreateKeyResult{}, fmt.Errorf("create new key: %w", err)
	}

	// Revoke old key
	if err := m.Revoke(ctx, id); err != nil {
		// Log but don't fail - new key is already created
		fmt.Printf("warning: failed to revoke old key %s: %v\n", id, err)
	}

	metrics.APIKeysRotatedTotal.WithLabelValues(oldKey.TenantID).Inc()
	return result, nil
}

// UpdateLastUsed records the last usage time.
func (m *DefaultManager) UpdateLastUsed(ctx context.Context, id string) error {
	if !m.enabled {
		return nil
	}

	key, err := m.store.Get(ctx, id)
	if err != nil {
		return err
	}

	now := time.Now()
	key.LastUsedAt = &now

	return m.store.Update(ctx, key)
}

// Close releases resources.
func (m *DefaultManager) Close() error {
	if m.store != nil {
		return m.store.Close()
	}
	return nil
}

// Enabled returns whether API key management is active.
func (m *DefaultManager) Enabled() bool {
	return m.enabled
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// newPostgresStoreFromConfig creates a Postgres store from configuration.
func newPostgresStoreFromConfig(cfg Config) (*PostgresStore, error) {
	pgCfg := storage.PostgresConfig{
		URL: cfg.PostgresURL,
	}

	pg, err := storage.NewPostgres(pgCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres client: %w", err)
	}

	ctx := context.Background()
	if err := pg.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	// Run migrations
	if err := pg.MigrateAPIKeysSchema(ctx); err != nil {
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return NewPostgresStore(pg), nil
}

// NoopManager is a manager that does nothing.
type NoopManager struct{}

// NewNoopManager creates a manager that always returns not found.
func NewNoopManager() *NoopManager {
	return &NoopManager{}
}

func (NoopManager) Create(ctx context.Context, req CreateKeyRequest) (CreateKeyResult, error) {
	return CreateKeyResult{}, fmt.Errorf("api key management is disabled")
}

func (NoopManager) Get(ctx context.Context, id string) (APIKey, error) {
	return APIKey{}, ErrKeyNotFound
}

func (NoopManager) List(ctx context.Context, tenantID, userID string) ([]APIKey, error) {
	return nil, nil
}

func (NoopManager) Validate(ctx context.Context, plaintextKey string) (APIKey, error) {
	return APIKey{}, ErrKeyNotFound
}

func (NoopManager) Revoke(ctx context.Context, id string) error {
	return ErrKeyNotFound
}

func (NoopManager) Rotate(ctx context.Context, id string) (CreateKeyResult, error) {
	return CreateKeyResult{}, ErrKeyNotFound
}

func (NoopManager) UpdateLastUsed(ctx context.Context, id string) error {
	return nil
}

func (NoopManager) Close() error {
	return nil
}
