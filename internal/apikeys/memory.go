package apikeys

import (
	"context"
	"sync"
)

// MemoryStore implements Store using in-memory storage.
type MemoryStore struct {
	mu        sync.RWMutex
	keys      map[string]APIKey // ID -> key
	hashIndex map[string]string // hash -> ID
}

// NewMemoryStore creates a new in-memory API key store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		keys:      make(map[string]APIKey),
		hashIndex: make(map[string]string),
	}
}

// Create stores a new API key.
func (s *MemoryStore) Create(ctx context.Context, key APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.keys[key.ID] = key
	s.hashIndex[key.KeyHash] = key.ID
	return nil
}

// Get retrieves a key by ID.
func (s *MemoryStore) Get(ctx context.Context, id string) (APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key, ok := s.keys[id]
	if !ok {
		return APIKey{}, ErrKeyNotFound
	}
	return key, nil
}

// GetByHash retrieves a key by its hash.
func (s *MemoryStore) GetByHash(ctx context.Context, keyHash string) (APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, ok := s.hashIndex[keyHash]
	if !ok {
		return APIKey{}, ErrKeyNotFound
	}

	key, ok := s.keys[id]
	if !ok {
		return APIKey{}, ErrKeyNotFound
	}

	return key, nil
}

// List returns all keys for a tenant/user.
func (s *MemoryStore) List(ctx context.Context, tenantID, userID string) ([]APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []APIKey
	for _, key := range s.keys {
		if key.TenantID != tenantID {
			continue
		}
		// If userID is specified, filter by user
		if userID != "" && key.UserID != userID {
			continue
		}
		result = append(result, key)
	}
	return result, nil
}

// Update modifies an existing key.
func (s *MemoryStore) Update(ctx context.Context, key APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.keys[key.ID]; !ok {
		return ErrKeyNotFound
	}

	s.keys[key.ID] = key
	return nil
}

// Delete removes a key.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key, ok := s.keys[id]
	if !ok {
		return ErrKeyNotFound
	}

	delete(s.hashIndex, key.KeyHash)
	delete(s.keys, id)
	return nil
}

// CountByUser returns the number of active keys for a user.
func (s *MemoryStore) CountByUser(ctx context.Context, tenantID, userID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, key := range s.keys {
		if key.TenantID == tenantID && key.UserID == userID && key.IsValid() {
			count++
		}
	}
	return count, nil
}

// Close releases resources.
func (s *MemoryStore) Close() error {
	return nil
}

// Reset clears all data (for testing).
func (s *MemoryStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = make(map[string]APIKey)
	s.hashIndex = make(map[string]string)
}
