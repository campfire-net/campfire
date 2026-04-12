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
	info, err := resolver.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

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
	if err := cache.Set(pub, validCampfireID, 5*time.Minute); err != nil {
		t.Fatalf("cache.Set: %v", err)
	}

	resolver := convention.NewCacheIdentityResolver(cache)
	info, err := resolver.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

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
	info, err := resolver.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

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

// Compile-time assertion: mockEmptyCache must implement protocol.VerificationCache.
// If the interface changes, this fails to compile — catching interface mismatches
// before the mock can silently diverge.
var _ protocol.VerificationCache = mockEmptyCache{}

// mockEmptyCache is a VerificationCache that returns ("", true) for any lookup,
// simulating a cache that has an entry but with an empty campfire ID.
// This is an edge case (semantically invalid state) not producible with the real
// VerificationCache, so a mock is appropriate here. Integration coverage using the
// real cache is in TestCacheIdentityResolver_Integration_* below.
type mockEmptyCache struct{}

func (mockEmptyCache) Set(_ ed25519.PublicKey, _ string, _ time.Duration) error { return nil }
func (mockEmptyCache) Get(_ ed25519.PublicKey) (string, bool)              { return "", true }
func (mockEmptyCache) Invalidate(_ ed25519.PublicKey)                      {}

// TestCacheIdentityResolver_NilFromCache verifies that when the cache returns
// an empty campfire ID string (ok=true but empty), IdentityVerified is false.
// An empty campfire ID is not a valid identity.
func TestCacheIdentityResolver_NilFromCache(t *testing.T) {
	pub, _ := generateTestKey(t)

	resolver := convention.NewCacheIdentityResolver(mockEmptyCache{})
	info, err := resolver.Resolve(context.Background(), pub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

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

// setupResolverTestEnvFull creates a minimal server+caller pair for resolver integration tests
// and also returns the callerID so tests can pre-populate the VerificationCache.
func setupResolverTestEnvFull(t *testing.T) (serverClient *protocol.Client, callerClient *protocol.Client, cfID string, callerID *identity.Identity) {
	t.Helper()
	serverClient, callerClient, cfID, callerID = setupResolverTestEnvImpl(t)
	return
}

// setupResolverTestEnv creates a minimal server+caller pair for resolver integration tests.
func setupResolverTestEnv(t *testing.T) (serverClient *protocol.Client, callerClient *protocol.Client, cfID string) {
	t.Helper()
	serverClient, callerClient, cfID, _ = setupResolverTestEnvImpl(t)
	return
}

// setupResolverTestEnvImpl is the shared implementation for both setupResolverTestEnv variants.
func setupResolverTestEnvImpl(t *testing.T) (serverClient *protocol.Client, callerClient *protocol.Client, cfID string, callerID *identity.Identity) {
	t.Helper()

	storeDir := t.TempDir()
	transportDir := t.TempDir()

	serverID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating server identity: %v", err)
	}
	callerID, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating caller identity: %v", err)
	}
	campfireIdentity, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	cfID = campfireIdentity.PublicKeyHex()

	cfDir := filepath.Join(transportDir, cfID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             campfireIdentity.PublicKey,
		PrivateKey:            campfireIdentity.PrivateKey,
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
		if err := tr.WriteMember(cfID, campfire.MemberRecord{
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
		CampfireID:    cfID,
		TransportDir:  tr.CampfireDir(cfID),
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

	return protocol.New(serverStore, serverID), protocol.New(callerStore, callerID), cfID, callerID
}

// TestWithIdentityResolver_Nil_FallsBackToNoop verifies that passing nil to
// WithIdentityResolver does not leave the server's resolver as nil. The server
// must dispatch a message without panicking, and the handler must receive
// IdentityVerified=false (NoopIdentityResolver behaviour).
func TestWithIdentityResolver_Nil_FallsBackToNoop(t *testing.T) {
	serverClient, callerClient, campfireID := setupResolverTestEnv(t)

	decl := &convention.Declaration{
		Convention: "resolver-nil-test",
		Operation:  "ping",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "msg", Type: "string", Required: true, MaxLength: 256},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "resolver-nil-test:ping", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}

	var mu sync.Mutex
	var gotIdentityVerified bool
	var dispatched bool

	srv := convention.NewServer(serverClient, decl).
		WithPollInterval(50 * time.Millisecond).
		WithIdentityResolver(nil) // must not panic; should fall back to NoopIdentityResolver

	srv.RegisterHandler("ping", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		gotIdentityVerified = req.Identity.IdentityVerified
		dispatched = true
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
	time.Sleep(100 * time.Millisecond)

	if _, err := callerClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Tags:       []string{"resolver-nil-test:ping"},
		Payload:    []byte(`{"msg":"hello"}`),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := dispatched
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if !dispatched {
		t.Fatal("handler was never dispatched — nil resolver may have caused a panic or message was not received")
	}
	if gotIdentityVerified {
		t.Error("expected IdentityVerified=false with NoopIdentityResolver fallback, got true")
	}
}

// TestServer_DispatchPopulatesIdentityInfo verifies the full path:
// WithIdentityResolver → Server.dispatch → req.Identity populated → handler receives it.
//
// A real CacheIdentityResolver backed by a VerificationCache with a verified entry
// for the caller's public key is attached to the Server. After delivering a message,
// the handler must observe req.Identity.IdentityVerified == true and
// req.Identity.Identity == expectedHomeID.
func TestServer_DispatchPopulatesIdentityInfo(t *testing.T) {
	serverClient, callerClient, campfireID, callerID := setupResolverTestEnvFull(t)

	// Pre-populate the VerificationCache with a verified entry for the caller.
	// This simulates the caller having completed identity verification out-of-band.
	const expectedHomeID = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	cache := protocol.NewVerificationCache()
	if err := cache.Set(ed25519.PublicKey(callerID.PublicKey), expectedHomeID, 5*time.Minute); err != nil {
		t.Fatalf("cache.Set: %v", err)
	}

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

	time.Sleep(20 * time.Millisecond)

	if _, err := callerClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(`{"text":"identity test"}`),
		Tags:       []string{"social:post"},
	}); err != nil {
		t.Fatalf("caller Send: %v", err)
	}

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
