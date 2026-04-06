package convention_test

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
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

// TestServer_DispatchPopulatesIdentityInfo verifies the full path:
// WithIdentityResolver → Server.dispatch → req.Identity populated → handler receives it.
//
// This test constructs a real Server with a CacheIdentityResolver backed by a
// VerificationCache that has a verified entry for the caller's public key. It then
// delivers a message via Server.Serve and asserts that the handler sees
// req.Identity.IdentityVerified == true and req.Identity.Identity == expectedHomeID.
func TestServer_DispatchPopulatesIdentityInfo(t *testing.T) {
	// Build a minimal two-party campfire environment (server + caller).
	storeDir := t.TempDir()
	transportDir := t.TempDir()

	serverID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating server identity: %v", err)
	}
	callerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating caller identity: %v", err)
	}
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	cfDir := filepath.Join(transportDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	tr := fs.New(transportDir)
	for _, id := range []*identity.Identity{serverID, callerID} {
		if err := tr.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: id.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
			Role:      campfire.RoleFull,
		}); err != nil {
			t.Fatalf("writing member: %v", err)
		}
	}

	serverStore, err := store.Open(filepath.Join(storeDir, "server.db"))
	if err != nil {
		t.Fatalf("opening server store: %v", err)
	}
	t.Cleanup(func() { serverStore.Close() })

	callerStore, err := store.Open(filepath.Join(storeDir, "caller.db"))
	if err != nil {
		t.Fatalf("opening caller store: %v", err)
	}
	t.Cleanup(func() { callerStore.Close() })

	membership := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}
	if err := serverStore.AddMembership(membership); err != nil {
		t.Fatalf("server store add membership: %v", err)
	}
	if err := callerStore.AddMembership(membership); err != nil {
		t.Fatalf("caller store add membership: %v", err)
	}

	serverClient := protocol.New(serverStore, serverID)
	callerClient := protocol.New(callerStore, callerID)

	// Pre-populate the VerificationCache with a verified entry for the caller.
	// This simulates the caller having completed identity verification out-of-band.
	const expectedHomeID = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	cache := protocol.NewVerificationCache()
	cache.Set(ed25519.PublicKey(callerID.PublicKey), expectedHomeID, 5*time.Minute)

	resolver := convention.NewCacheIdentityResolver(cache)

	decl := &convention.Declaration{
		Convention: "social-post-format",
		Operation:  "post",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "text", Type: "string", Required: true, MaxLength: 65536},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "social:post", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}

	// Capture the IdentityInfo the handler receives.
	var mu sync.Mutex
	var capturedIdentity convention.IdentityInfo

	srv := convention.NewServer(serverClient, decl).
		WithIdentityResolver(resolver).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		capturedIdentity = req.Identity
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, campfireID) //nolint:errcheck
	}()

	// Give the server a moment to start polling.
	time.Sleep(20 * time.Millisecond)

	// Send a convention operation message from the caller.
	if _, err := callerClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(`{"text":"identity test"}`),
		Tags:       []string{"social:post"},
	}); err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Wait for the server to dispatch the message to the handler.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		verified := capturedIdentity.IdentityVerified
		mu.Unlock()
		if verified {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if capturedIdentity.MachineKey == nil {
		t.Fatal("expected MachineKey to be set in handler")
	}
	if !capturedIdentity.MachineKey.Equal(ed25519.PublicKey(callerID.PublicKey)) {
		t.Errorf("MachineKey mismatch: got %x, want %x", capturedIdentity.MachineKey, callerID.PublicKey)
	}
	if !capturedIdentity.IdentityVerified {
		t.Error("expected IdentityVerified=true: handler did not receive verified identity")
	}
	if capturedIdentity.Identity != expectedHomeID {
		t.Errorf("Identity = %q; want %q", capturedIdentity.Identity, expectedHomeID)
	}
}
