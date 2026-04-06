package convention_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// generateTestKey returns a deterministic ed25519 key pair for testing.
func generateTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	return pub, priv
}

// TestNoopIdentityResolver verifies that NoopIdentityResolver always returns
// only MachineKey, with IdentityVerified=false and Identity="".
func TestNoopIdentityResolver(t *testing.T) {
	pub, _ := generateTestKey(t)

	resolver := convention.NoopIdentityResolver{}
	info := resolver.Resolve(pub)

	if info.MachineKey == nil {
		t.Fatal("expected MachineKey to be set")
	}
	if !info.MachineKey.Equal(pub) {
		t.Errorf("MachineKey mismatch: got %x, want %x", info.MachineKey, pub)
	}
	if info.IdentityVerified {
		t.Error("expected IdentityVerified=false for NoopIdentityResolver")
	}
	if info.Identity != "" {
		t.Errorf("expected empty Identity, got %q", info.Identity)
	}
}

// validCampfireID is a valid 64-char lowercase hex campfire ID for tests.
const validCampfireID = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

// TestCacheIdentityResolver_Hit verifies that when the VerificationCache has a
// verified entry, CacheIdentityResolver returns the campfire ID with IdentityVerified=true.
func TestCacheIdentityResolver_Hit(t *testing.T) {
	pub, _ := generateTestKey(t)

	cache := protocol.NewVerificationCache()
	cache.Set(pub, validCampfireID, 5*time.Minute)

	resolver := convention.NewCacheIdentityResolver(cache)
	info := resolver.Resolve(pub)

	if !info.MachineKey.Equal(pub) {
		t.Errorf("MachineKey mismatch: got %x, want %x", info.MachineKey, pub)
	}
	if !info.IdentityVerified {
		t.Error("expected IdentityVerified=true on cache hit")
	}
	if info.Identity != validCampfireID {
		t.Errorf("Identity = %q; want %q", info.Identity, validCampfireID)
	}
}

// TestCacheIdentityResolver_Miss verifies that when there is no cache entry for
// a sender, CacheIdentityResolver returns MachineKey-only with IdentityVerified=false.
func TestCacheIdentityResolver_Miss(t *testing.T) {
	pub, _ := generateTestKey(t)

	cache := protocol.NewVerificationCache()
	// No entry set for pub.

	resolver := convention.NewCacheIdentityResolver(cache)
	info := resolver.Resolve(pub)

	if !info.MachineKey.Equal(pub) {
		t.Errorf("MachineKey mismatch: got %x, want %x", info.MachineKey, pub)
	}
	if info.IdentityVerified {
		t.Error("expected IdentityVerified=false on cache miss")
	}
	if info.Identity != "" {
		t.Errorf("expected empty Identity on cache miss, got %q", info.Identity)
	}
}

// mockEmptyCache is a VerificationCache that returns ("", true) for any lookup,
// simulating a cache that has an entry but with an empty campfire ID.
type mockEmptyCache struct{}

func (mockEmptyCache) Set(_ ed25519.PublicKey, _ string, _ time.Duration) {}
func (mockEmptyCache) Get(_ ed25519.PublicKey) (string, bool)              { return "", true }
func (mockEmptyCache) Invalidate(_ ed25519.PublicKey)                      {}

// TestCacheIdentityResolver_NilFromCache verifies that when the cache returns
// an empty campfire ID string (ok=true but empty), IdentityVerified is false.
// An empty campfire ID is not a valid identity.
func TestCacheIdentityResolver_NilFromCache(t *testing.T) {
	pub, _ := generateTestKey(t)

	resolver := convention.NewCacheIdentityResolver(mockEmptyCache{})
	info := resolver.Resolve(pub)

	if !info.MachineKey.Equal(pub) {
		t.Errorf("MachineKey mismatch: got %x, want %x", info.MachineKey, pub)
	}
	if info.IdentityVerified {
		t.Error("expected IdentityVerified=false when campfire ID is empty string")
	}
	if info.Identity != "" {
		t.Errorf("expected empty Identity, got %q", info.Identity)
	}
}
