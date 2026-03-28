package hosting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
)

// mockKeyResolver implements ForgeKeyResolver for tests.
type mockKeyResolver struct {
	calls  int
	record forge.KeyRecord
	err    error
}

func (m *mockKeyResolver) ResolveKey(_ context.Context, _ string) (forge.KeyRecord, error) {
	m.calls++
	return m.record, m.err
}

func TestResolveKey_HappyPath(t *testing.T) {
	mock := &mockKeyResolver{
		record: forge.KeyRecord{
			AccountID: "acc-abc123",
			Role:      "tenant",
		},
	}
	r := NewForgeIdentityResolver(mock)

	identity, err := r.ResolveKey(context.Background(), "forge-key-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identity.AccountID != "acc-abc123" {
		t.Errorf("AccountID = %q, want %q", identity.AccountID, "acc-abc123")
	}
	if identity.Role != "tenant" {
		t.Errorf("Role = %q, want %q", identity.Role, "tenant")
	}
	if mock.calls != 1 {
		t.Errorf("Forge calls = %d, want 1", mock.calls)
	}
}

func TestResolveKey_CacheHit(t *testing.T) {
	mock := &mockKeyResolver{
		record: forge.KeyRecord{AccountID: "acc-cached", Role: "tenant"},
	}
	r := NewForgeIdentityResolver(mock)

	// First call populates cache.
	_, err := r.ResolveKey(context.Background(), "forge-key-cached")
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}

	// Second call should be served from cache — Forge not called again.
	identity, err := r.ResolveKey(context.Background(), "forge-key-cached")
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if identity.AccountID != "acc-cached" {
		t.Errorf("AccountID = %q, want %q", identity.AccountID, "acc-cached")
	}
	if mock.calls != 1 {
		t.Errorf("Forge calls = %d after two resolves, want 1 (cache should prevent second call)", mock.calls)
	}
}

func TestResolveKey_ExpiredCache(t *testing.T) {
	mock := &mockKeyResolver{
		record: forge.KeyRecord{AccountID: "acc-expired", Role: "tenant"},
	}
	r := NewForgeIdentityResolver(mock)

	// Use a very short TTL.
	r.TTL = time.Millisecond

	// Synthetic clock starting in the past so cache entry is immediately expired.
	base := time.Now()
	r.now = func() time.Time { return base }

	// First call — populates cache with expiry = base + 1ms.
	_, err := r.ResolveKey(context.Background(), "forge-key-expired")
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}

	// Advance clock past the TTL.
	r.now = func() time.Time { return base.Add(2 * time.Millisecond) }

	// Second call — cache expired, should call Forge again.
	identity, err := r.ResolveKey(context.Background(), "forge-key-expired")
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if identity.AccountID != "acc-expired" {
		t.Errorf("AccountID = %q, want %q", identity.AccountID, "acc-expired")
	}
	if mock.calls != 2 {
		t.Errorf("Forge calls = %d, want 2 (expired cache should re-call Forge)", mock.calls)
	}
}

func TestResolveKey_InvalidKey(t *testing.T) {
	mock := &mockKeyResolver{
		err: errors.New("forge: server returned 401"),
	}
	r := NewForgeIdentityResolver(mock)

	_, err := r.ResolveKey(context.Background(), "bad-key")
	if err == nil {
		t.Fatal("expected error for invalid key, got nil")
	}
	if mock.calls != 1 {
		t.Errorf("Forge calls = %d, want 1", mock.calls)
	}
}

func TestResolveKey_ErrorNotCached(t *testing.T) {
	callCount := 0
	mock := &mockKeyResolver{}

	r := NewForgeIdentityResolver(mock)

	// First call fails.
	mock.err = errors.New("forge: server returned 401")
	_, err := r.ResolveKey(context.Background(), "forge-key-error")
	if err == nil {
		t.Fatal("expected error on first call")
	}
	callCount++

	// Second call — error should NOT be cached; Forge should be called again.
	mock.err = nil
	mock.record = forge.KeyRecord{AccountID: "acc-recovered", Role: "tenant"}
	identity, err := r.ResolveKey(context.Background(), "forge-key-error")
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if identity.AccountID != "acc-recovered" {
		t.Errorf("AccountID = %q, want acc-recovered", identity.AccountID)
	}
	callCount++

	if mock.calls != callCount {
		t.Errorf("Forge calls = %d, want %d", mock.calls, callCount)
	}
}
