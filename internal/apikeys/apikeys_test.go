package apikeys

import (
	"context"
	"testing"
	"time"
)

func TestGenerateKey(t *testing.T) {
	prefix := "test_"
	plaintext, hash, err := GenerateKey(prefix)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	// Check prefix
	if !hasPrefix(plaintext, prefix) {
		t.Errorf("key should start with prefix %q, got %q", prefix, plaintext[:len(prefix)])
	}

	// Check length (prefix + 64 hex chars from 32 bytes)
	expectedLen := len(prefix) + 64
	if len(plaintext) != expectedLen {
		t.Errorf("key length = %d, want %d", len(plaintext), expectedLen)
	}

	// Verify hash matches
	computedHash := HashKey(plaintext)
	if computedHash != hash {
		t.Errorf("hash mismatch: got %q, want %q", computedHash, hash)
	}

	// Check hash length (64 hex chars from SHA-256)
	if len(hash) != 64 {
		t.Errorf("hash length = %d, want 64", len(hash))
	}
}

func TestHashKey(t *testing.T) {
	// Same input should produce same hash
	key := "test_abc123"
	hash1 := HashKey(key)
	hash2 := HashKey(key)
	if hash1 != hash2 {
		t.Error("HashKey should be deterministic")
	}

	// Different inputs should produce different hashes
	hash3 := HashKey("test_xyz789")
	if hash1 == hash3 {
		t.Error("different keys should produce different hashes")
	}
}

func TestExtractPrefix(t *testing.T) {
	tests := []struct {
		key       string
		prefixLen int
		want      string
	}{
		{"fmg_abc123def456", 8, "fmg_abc1"},
		{"short", 10, "short"},
		{"", 5, ""},
		{"exactly10c", 10, "exactly10c"},
	}

	for _, tc := range tests {
		got := ExtractPrefix(tc.key, tc.prefixLen)
		if got != tc.want {
			t.Errorf("ExtractPrefix(%q, %d) = %q, want %q", tc.key, tc.prefixLen, got, tc.want)
		}
	}
}

func TestAPIKey_IsValid(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	tests := []struct {
		name string
		key  APIKey
		want bool
	}{
		{
			name: "valid key",
			key:  APIKey{ExpiresAt: &future},
			want: true,
		},
		{
			name: "no expiry",
			key:  APIKey{},
			want: true,
		},
		{
			name: "expired key",
			key:  APIKey{ExpiresAt: &past},
			want: false,
		},
		{
			name: "revoked key",
			key:  APIKey{RevokedAt: &past},
			want: false,
		},
		{
			name: "revoked and expired",
			key:  APIKey{ExpiresAt: &past, RevokedAt: &past},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.key.IsValid()
			if got != tc.want {
				t.Errorf("IsValid() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAPIKey_HasScope(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		check  string
		want   bool
	}{
		{
			name:   "empty scopes = full access",
			scopes: nil,
			check:  "anything",
			want:   true,
		},
		{
			name:   "wildcard scope",
			scopes: []string{"*"},
			check:  "anything",
			want:   true,
		},
		{
			name:   "has exact scope",
			scopes: []string{"read", "write"},
			check:  "read",
			want:   true,
		},
		{
			name:   "missing scope",
			scopes: []string{"read"},
			check:  "write",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := APIKey{Scopes: tc.scopes}
			got := key.HasScope(tc.check)
			if got != tc.want {
				t.Errorf("HasScope(%q) = %v, want %v", tc.check, got, tc.want)
			}
		})
	}
}

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	// Create a key
	key := APIKey{
		ID:       "key-1",
		TenantID: "tenant-1",
		UserID:   "user-1",
		Name:     "Test Key",
		KeyHash:  "hash123",
	}

	if err := store.Create(ctx, key); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Get by ID
	got, err := store.Get(ctx, "key-1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.ID != key.ID {
		t.Errorf("Get returned wrong key: %v", got)
	}

	// Get by hash
	got, err = store.GetByHash(ctx, "hash123")
	if err != nil {
		t.Fatalf("GetByHash failed: %v", err)
	}
	if got.ID != key.ID {
		t.Errorf("GetByHash returned wrong key: %v", got)
	}

	// List
	keys, err := store.List(ctx, "tenant-1", "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("List returned %d keys, want 1", len(keys))
	}

	// List with user filter
	keys, err = store.List(ctx, "tenant-1", "user-1")
	if err != nil {
		t.Fatalf("List with user failed: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("List with user returned %d keys, want 1", len(keys))
	}

	// List with wrong user
	keys, err = store.List(ctx, "tenant-1", "other-user")
	if err != nil {
		t.Fatalf("List with wrong user failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("List with wrong user returned %d keys, want 0", len(keys))
	}

	// Update
	key.Name = "Updated Name"
	if err := store.Update(ctx, key); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got, _ = store.Get(ctx, "key-1")
	if got.Name != "Updated Name" {
		t.Errorf("Update did not persist: name = %q", got.Name)
	}

	// Count by user
	count, err := store.CountByUser(ctx, "tenant-1", "user-1")
	if err != nil {
		t.Fatalf("CountByUser failed: %v", err)
	}
	if count != 1 {
		t.Errorf("CountByUser = %d, want 1", count)
	}

	// Delete
	if err := store.Delete(ctx, "key-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = store.Get(ctx, "key-1")
	if err != ErrKeyNotFound {
		t.Errorf("Get after delete: err = %v, want ErrKeyNotFound", err)
	}
}

func TestManager_CreateAndValidate(t *testing.T) {
	ctx := context.Background()

	manager, err := New(Config{
		Enabled:        true,
		Prefix:         "test_",
		Store:          "memory",
		MaxKeysPerUser: 5,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer manager.Close()

	// Create a key
	result, err := manager.Create(ctx, CreateKeyRequest{
		TenantID: "tenant-1",
		UserID:   "user-1",
		Name:     "My API Key",
		Scopes:   []string{"read", "write"},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if result.PlaintextKey == "" {
		t.Error("PlaintextKey should not be empty")
	}
	if !hasPrefix(result.PlaintextKey, "test_") {
		t.Errorf("key should have prefix test_, got %q", result.PlaintextKey[:5])
	}
	if result.Key.ID == "" {
		t.Error("Key.ID should not be empty")
	}

	// Validate the key
	key, err := manager.Validate(ctx, result.PlaintextKey)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if key.ID != result.Key.ID {
		t.Errorf("Validate returned different key ID")
	}

	// Validate with wrong key
	_, err = manager.Validate(ctx, "test_wrongkey")
	if err != ErrKeyNotFound {
		t.Errorf("Validate wrong key: err = %v, want ErrKeyNotFound", err)
	}

	// Validate with wrong prefix
	_, err = manager.Validate(ctx, "wrong_prefix")
	if err != ErrInvalidKey {
		t.Errorf("Validate wrong prefix: err = %v, want ErrInvalidKey", err)
	}
}

func TestManager_Revoke(t *testing.T) {
	ctx := context.Background()

	manager, _ := New(Config{
		Enabled: true,
		Prefix:  "test_",
		Store:   "memory",
	})
	defer manager.Close()

	// Create a key
	result, _ := manager.Create(ctx, CreateKeyRequest{
		TenantID: "tenant-1",
		UserID:   "user-1",
		Name:     "Revokable Key",
	})

	// Revoke it
	if err := manager.Revoke(ctx, result.Key.ID); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	// Validate should fail
	_, err := manager.Validate(ctx, result.PlaintextKey)
	if err != ErrKeyRevoked {
		t.Errorf("Validate revoked key: err = %v, want ErrKeyRevoked", err)
	}
}

func TestManager_Rotate(t *testing.T) {
	ctx := context.Background()

	manager, _ := New(Config{
		Enabled: true,
		Prefix:  "test_",
		Store:   "memory",
	})
	defer manager.Close()

	// Create a key
	result, _ := manager.Create(ctx, CreateKeyRequest{
		TenantID: "tenant-1",
		UserID:   "user-1",
		Name:     "Rotatable Key",
		Scopes:   []string{"admin"},
	})
	oldKey := result.PlaintextKey
	oldID := result.Key.ID

	// Rotate it
	newResult, err := manager.Rotate(ctx, oldID)
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}

	if newResult.PlaintextKey == oldKey {
		t.Error("Rotate should create a new key")
	}
	if newResult.Key.ID == oldID {
		t.Error("Rotate should create new key ID")
	}

	// Old key should be revoked
	_, err = manager.Validate(ctx, oldKey)
	if err != ErrKeyRevoked {
		t.Errorf("Old key should be revoked: err = %v", err)
	}

	// New key should work
	key, err := manager.Validate(ctx, newResult.PlaintextKey)
	if err != nil {
		t.Fatalf("New key validation failed: %v", err)
	}
	if !key.HasScope("admin") {
		t.Error("New key should inherit scopes")
	}
}

func TestManager_Expiry(t *testing.T) {
	ctx := context.Background()

	manager, _ := New(Config{
		Enabled: true,
		Prefix:  "test_",
		Store:   "memory",
	})
	defer manager.Close()

	// Create a key that expires immediately
	result, _ := manager.Create(ctx, CreateKeyRequest{
		TenantID:  "tenant-1",
		UserID:    "user-1",
		Name:      "Expiring Key",
		ExpiresIn: time.Millisecond,
	})

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	// Validate should fail
	_, err := manager.Validate(ctx, result.PlaintextKey)
	if err != ErrKeyExpired {
		t.Errorf("Validate expired key: err = %v, want ErrKeyExpired", err)
	}
}

func TestManager_MaxKeysPerUser(t *testing.T) {
	ctx := context.Background()

	manager, _ := New(Config{
		Enabled:        true,
		Prefix:         "test_",
		Store:          "memory",
		MaxKeysPerUser: 2,
	})
	defer manager.Close()

	// Create max keys
	for i := 0; i < 2; i++ {
		_, err := manager.Create(ctx, CreateKeyRequest{
			TenantID: "tenant-1",
			UserID:   "user-1",
			Name:     "Key",
		})
		if err != nil {
			t.Fatalf("Create key %d failed: %v", i, err)
		}
	}

	// Third key should fail
	_, err := manager.Create(ctx, CreateKeyRequest{
		TenantID: "tenant-1",
		UserID:   "user-1",
		Name:     "Too Many",
	})
	if err != ErrTooManyKeys {
		t.Errorf("Create over limit: err = %v, want ErrTooManyKeys", err)
	}

	// Different user should work
	_, err = manager.Create(ctx, CreateKeyRequest{
		TenantID: "tenant-1",
		UserID:   "user-2",
		Name:     "Different User",
	})
	if err != nil {
		t.Errorf("Create for different user failed: %v", err)
	}
}

func TestManager_Disabled(t *testing.T) {
	ctx := context.Background()

	manager, err := New(Config{
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// All operations should fail gracefully
	_, err = manager.Create(ctx, CreateKeyRequest{
		TenantID: "tenant-1",
		UserID:   "user-1",
		Name:     "Disabled",
	})
	if err == nil {
		t.Error("Create should fail when disabled")
	}

	_, err = manager.Validate(ctx, "test_anykey")
	if err != ErrKeyNotFound {
		t.Errorf("Validate when disabled: err = %v, want ErrKeyNotFound", err)
	}
}

func TestNoopManager(t *testing.T) {
	ctx := context.Background()
	m := NewNoopManager()

	_, err := m.Create(ctx, CreateKeyRequest{})
	if err == nil {
		t.Error("NoopManager.Create should fail")
	}

	_, err = m.Validate(ctx, "key")
	if err != ErrKeyNotFound {
		t.Error("NoopManager.Validate should return ErrKeyNotFound")
	}
}
