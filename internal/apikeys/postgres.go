package apikeys

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/storage"
)

// PostgresStore implements Store using PostgreSQL.
type PostgresStore struct {
	pg *storage.Postgres
}

// NewPostgresStore creates a new Postgres-backed API key store.
func NewPostgresStore(pg *storage.Postgres) *PostgresStore {
	return &PostgresStore{pg: pg}
}

// Create stores a new API key.
func (s *PostgresStore) Create(ctx context.Context, key APIKey) error {
	_, err := s.pg.Exec(ctx, `
		INSERT INTO api_keys (id, tenant_id, user_id, name, key_hash, key_prefix, scopes, created_at, expires_at, last_used_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		key.ID,
		key.TenantID,
		key.UserID,
		key.Name,
		key.KeyHash,
		key.KeyPrefix,
		pq.Array(key.Scopes),
		key.CreatedAt,
		key.ExpiresAt,
		key.LastUsedAt,
		key.RevokedAt,
	)
	return err
}

// Get retrieves a key by ID.
func (s *PostgresStore) Get(ctx context.Context, id string) (APIKey, error) {
	var key APIKey
	var scopes []string

	err := s.pg.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, name, key_hash, key_prefix, scopes, created_at, expires_at, last_used_at, revoked_at
		FROM api_keys
		WHERE id = $1
	`, id).Scan(
		&key.ID,
		&key.TenantID,
		&key.UserID,
		&key.Name,
		&key.KeyHash,
		&key.KeyPrefix,
		pq.Array(&scopes),
		&key.CreatedAt,
		&key.ExpiresAt,
		&key.LastUsedAt,
		&key.RevokedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrKeyNotFound
	}
	if err != nil {
		return APIKey{}, err
	}

	key.Scopes = scopes
	return key, nil
}

// GetByHash retrieves a key by its hash.
func (s *PostgresStore) GetByHash(ctx context.Context, keyHash string) (APIKey, error) {
	var key APIKey
	var scopes []string

	err := s.pg.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, name, key_hash, key_prefix, scopes, created_at, expires_at, last_used_at, revoked_at
		FROM api_keys
		WHERE key_hash = $1
	`, keyHash).Scan(
		&key.ID,
		&key.TenantID,
		&key.UserID,
		&key.Name,
		&key.KeyHash,
		&key.KeyPrefix,
		pq.Array(&scopes),
		&key.CreatedAt,
		&key.ExpiresAt,
		&key.LastUsedAt,
		&key.RevokedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrKeyNotFound
	}
	if err != nil {
		return APIKey{}, err
	}

	key.Scopes = scopes
	return key, nil
}

// List returns all keys for a tenant/user.
func (s *PostgresStore) List(ctx context.Context, tenantID, userID string) ([]APIKey, error) {
	rows, err := s.pg.Query(ctx, `
		SELECT id, tenant_id, user_id, name, key_hash, key_prefix, scopes, created_at, expires_at, last_used_at, revoked_at
		FROM api_keys
		WHERE tenant_id = $1 AND user_id = $2
		ORDER BY created_at DESC
	`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var key APIKey
		var scopes []string

		if err := rows.Scan(
			&key.ID,
			&key.TenantID,
			&key.UserID,
			&key.Name,
			&key.KeyHash,
			&key.KeyPrefix,
			pq.Array(&scopes),
			&key.CreatedAt,
			&key.ExpiresAt,
			&key.LastUsedAt,
			&key.RevokedAt,
		); err != nil {
			return nil, err
		}

		key.Scopes = scopes
		keys = append(keys, key)
	}

	return keys, rows.Err()
}

// Update modifies an existing key.
func (s *PostgresStore) Update(ctx context.Context, key APIKey) error {
	result, err := s.pg.Exec(ctx, `
		UPDATE api_keys
		SET name = $2, scopes = $3, expires_at = $4, last_used_at = $5, revoked_at = $6
		WHERE id = $1
	`,
		key.ID,
		key.Name,
		pq.Array(key.Scopes),
		key.ExpiresAt,
		key.LastUsedAt,
		key.RevokedAt,
	)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrKeyNotFound
	}

	return nil
}

// Delete removes a key.
func (s *PostgresStore) Delete(ctx context.Context, id string) error {
	result, err := s.pg.Exec(ctx, `
		DELETE FROM api_keys
		WHERE id = $1
	`, id)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrKeyNotFound
	}

	return nil
}

// CountByUser returns the number of active keys for a user.
func (s *PostgresStore) CountByUser(ctx context.Context, tenantID, userID string) (int, error) {
	var count int
	err := s.pg.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM api_keys
		WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
	`, tenantID, userID).Scan(&count)
	return count, err
}

// Close releases resources.
func (s *PostgresStore) Close() error {
	return s.pg.Close()
}

// CleanupExpired removes expired keys older than the given duration.
func (s *PostgresStore) CleanupExpired(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := s.pg.Exec(ctx, `
		DELETE FROM api_keys
		WHERE expires_at < $1 OR (revoked_at IS NOT NULL AND revoked_at < $1)
	`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
