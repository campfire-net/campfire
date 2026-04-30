package convention_test

// Tests for GrantChainResolver and CompositeResolver.
//
// All tests use real ed25519 keys, real campfires, and real protocol.Client,
// following the pattern established in trust_test.go.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/convention/delegation"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// grantChainTestEnv is the shared test scaffold for GrantChainResolver tests.
// It mirrors the pattern from delegation/trust_test.go's trustTestEnv.
type grantChainTestEnv struct {
	campfireHex string
	transportDir  string
	fsTransport   *fs.Transport
	identities    []*identity.Identity
	clients       []*protocol.Client
	stores        []store.Store
}

// newGrantChainTestEnv sets up a filesystem campfire with n member identities.
// Index 0 is conventionally the "anchor" identity.
func newGrantChainTestEnv(t *testing.T, n int) *grantChainTestEnv {
	t.Helper()

	storeDir := t.TempDir()
	transportDir := t.TempDir()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireHex := cfID.PublicKeyHex()

	cfDir := filepath.Join(transportDir, campfireHex)
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

	ids := make([]*identity.Identity, n)
	for i := 0; i < n; i++ {
		id, err := identity.Generate()
		if err != nil {
			t.Fatalf("generating identity %d: %v", i, err)
		}
		ids[i] = id
		if err := tr.WriteMember(campfireHex, campfire.MemberRecord{
			PublicKey: id.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
			Role:      campfire.RoleFull,
		}); err != nil {
			t.Fatalf("writing member %d: %v", i, err)
		}
	}

	membership := store.Membership{
		CampfireID:    campfireHex,
		TransportDir:  tr.CampfireDir(campfireHex),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}

	clients := make([]*protocol.Client, n)
	stores := make([]store.Store, n)

	for i := 0; i < n; i++ {
		s, err := store.Open(filepath.Join(storeDir, ids[i].PublicKeyHex()[:8]+".db"))
		if err != nil {
			t.Fatalf("opening store %d: %v", i, err)
		}
		t.Cleanup(func() { s.Close() })
		if err := s.AddMembership(membership); err != nil {
			t.Fatalf("adding membership %d: %v", i, err)
		}
		stores[i] = s
		clients[i] = protocol.New(s, ids[i])
	}

	return &grantChainTestEnv{
		campfireHex: campfireHex,
		transportDir:  transportDir,
		fsTransport:   tr,
		identities:    ids,
		clients:       clients,
		stores:        stores,
	}
}

// pubkey returns the ed25519.PublicKey for identity index i.
func (e *grantChainTestEnv) pubkey(i int) ed25519.PublicKey {
	return e.identities[i].PublicKey
}

// anchors returns a slice of pubkeys for the given identity indices.
func (e *grantChainTestEnv) anchors(indices ...int) []ed25519.PublicKey {
	result := make([]ed25519.PublicKey, len(indices))
	for i, idx := range indices {
		result[i] = e.identities[idx].PublicKey
	}
	return result
}

// postGrant sends an identity:granted message from parentIdx delegating to child.
func (e *grantChainTestEnv) postGrant(t *testing.T, parentIdx int, child ed25519.PublicKey, expiresAt int64) {
	t.Helper()
	payload := map[string]interface{}{
		"child_pubkey": hex.EncodeToString(child),
		"campfire_id":  e.campfireHex,
		"expires_at":   expiresAt,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling grant payload: %v", err)
	}
	if _, err := e.clients[parentIdx].Send(protocol.SendRequest{
		CampfireID: e.campfireHex,
		Payload:    b,
		Tags:       []string{delegation.GrantedTag},
	}); err != nil {
		t.Fatalf("posting grant from %d: %v", parentIdx, err)
	}
	e.syncAll(t)
}

// syncAll pulls all filesystem messages into every store.
func (e *grantChainTestEnv) syncAll(t *testing.T) {
	t.Helper()
	msgs, err := e.fsTransport.ListMessages(e.campfireHex)
	if err != nil {
		t.Fatalf("listing messages from transport: %v", err)
	}
	for _, s := range e.stores {
		for idx := range msgs {
			if !msgs[idx].VerifySignature() {
				continue
			}
			s.AddMessage(store.MessageRecordFromMessage(e.campfireHex, &msgs[idx], store.NowNano())) //nolint:errcheck
		}
	}
}

// futureGrantExpiry returns a unix timestamp 1 day from now.
func futureGrantExpiry() int64 { return time.Now().Unix() + 86400 }

// --- Test 1: GrantChainResolver returns TrustResolved=true for valid grant chain ---

func TestGrantChainResolver_TrustResolved_ValidChain(t *testing.T) {
	env := newGrantChainTestEnv(t, 2)

	// ids[0] (anchor) grants ids[1] (sender).
	env.postGrant(t, 0, env.pubkey(1), futureGrantExpiry())

	resolver := convention.NewGrantChainResolver(env.clients[1], env.campfireHex, env.anchors(0))
	info, err := resolver.Resolve(context.Background(), env.pubkey(1))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if info.MachineKey == nil {
		t.Fatal("expected MachineKey to be set")
	}
	if !info.MachineKey.Equal(env.pubkey(1)) {
		t.Errorf("MachineKey mismatch: got %x, want %x", info.MachineKey, env.pubkey(1))
	}
	if !info.TrustResolved {
		t.Error("expected TrustResolved=true for sender with valid grant chain")
	}
	if info.Anchor == nil {
		t.Fatal("expected Anchor to be set on resolved identity")
	}
	if !info.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("Anchor mismatch: got %x, want %x", info.Anchor, env.pubkey(0))
	}
	if len(info.Chain) != 1 {
		t.Errorf("expected chain length 1, got %d", len(info.Chain))
	}
}

// --- Test 2: GrantChainResolver returns TrustResolved=false for unknown sender ---

func TestGrantChainResolver_TrustResolved_UnknownSender(t *testing.T) {
	env := newGrantChainTestEnv(t, 2)
	// No grant posted — ids[1] has no chain to ids[0].

	resolver := convention.NewGrantChainResolver(env.clients[0], env.campfireHex, env.anchors(0))
	info, err := resolver.Resolve(context.Background(), env.pubkey(1))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if info.MachineKey == nil {
		t.Fatal("expected MachineKey to be set")
	}
	if !info.MachineKey.Equal(env.pubkey(1)) {
		t.Errorf("MachineKey mismatch: got %x, want %x", info.MachineKey, env.pubkey(1))
	}
	if info.TrustResolved {
		t.Error("expected TrustResolved=false for unknown sender with no grant chain")
	}
	if info.Chain != nil {
		t.Errorf("expected Chain==nil for unresolved sender, got %v", info.Chain)
	}
	if info.Anchor != nil {
		t.Errorf("expected Anchor==nil for unresolved sender, got %x", info.Anchor)
	}
}

// --- Test 3: CompositeResolver merges CacheIdentityResolver + GrantChainResolver ---

func TestCompositeResolver_MergesCache_AndGrantChain(t *testing.T) {
	env := newGrantChainTestEnv(t, 2)

	// ids[0] (anchor) grants ids[1].
	env.postGrant(t, 0, env.pubkey(1), futureGrantExpiry())

	// Pre-populate the VerificationCache with a verified entry for ids[1].
	const expectedHomeID = "b1b2b3b4b5b60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	cache := protocol.NewVerificationCache()
	if err := cache.Set(env.pubkey(1), expectedHomeID, 5*time.Minute); err != nil {
		t.Fatalf("cache.Set: %v", err)
	}

	cacheResolver := convention.NewCacheIdentityResolver(cache)
	grantResolver := convention.NewGrantChainResolver(env.clients[1], env.campfireHex, env.anchors(0))

	composite := convention.NewCompositeResolver(cacheResolver, grantResolver)
	info, err := composite.Resolve(context.Background(), env.pubkey(1))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if info.MachineKey == nil {
		t.Fatal("expected MachineKey to be set")
	}
	if !info.MachineKey.Equal(env.pubkey(1)) {
		t.Errorf("MachineKey mismatch: got %x, want %x", info.MachineKey, env.pubkey(1))
	}
	// From CacheIdentityResolver:
	if !info.IdentityVerified {
		t.Error("expected IdentityVerified=true from CacheIdentityResolver")
	}
	if info.Identity != expectedHomeID {
		t.Errorf("Identity = %q; want %q", info.Identity, expectedHomeID)
	}
	// From GrantChainResolver:
	if !info.TrustResolved {
		t.Error("expected TrustResolved=true from GrantChainResolver")
	}
	if info.Anchor == nil {
		t.Fatal("expected Anchor to be set")
	}
	if !info.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("Anchor mismatch: got %x, want %x", info.Anchor, env.pubkey(0))
	}
	if len(info.Chain) != 1 {
		t.Errorf("expected chain length 1, got %d", len(info.Chain))
	}
	// Provenance: identity_verified from cache, trust_resolved from grant-chain.
	if info.Provenance == nil {
		t.Fatal("expected Provenance map to be non-nil")
	}
	if got := info.Provenance["identity_verified"]; got != "cache" {
		t.Errorf("Provenance[identity_verified] = %q; want %q", got, "cache")
	}
	if got := info.Provenance["trust_resolved"]; got != "grant-chain" {
		t.Errorf("Provenance[trust_resolved] = %q; want %q", got, "grant-chain")
	}
}

// --- Test 4: Server with GrantChainResolver → handler reads req.Identity.Chain ---

func TestServer_WithGrantChainResolver_HandlerReadsChain(t *testing.T) {
	env := newGrantChainTestEnv(t, 2)

	// ids[0] is the server (anchor); ids[1] is the caller.
	// ids[0] grants ids[1].
	env.postGrant(t, 0, env.pubkey(1), futureGrantExpiry())

	resolver := convention.NewGrantChainResolver(env.clients[0], env.campfireHex, env.anchors(0))

	decl := &convention.Declaration{
		Convention: "grant-chain-server-test",
		Operation:  "ping",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "msg", Type: "string", Required: true, MaxLength: 256},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "grant-chain-server-test:ping", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}

	var mu sync.Mutex
	var capturedIdentity convention.IdentityInfo
	var dispatched bool

	srv := convention.NewServer(env.clients[0], decl).
		WithIdentityResolver(resolver).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("ping", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		capturedIdentity = req.Identity
		dispatched = true
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, env.campfireHex) //nolint:errcheck
	}()

	time.Sleep(50 * time.Millisecond)

	if _, err := env.clients[1].Send(protocol.SendRequest{
		CampfireID: env.campfireHex,
		Tags:       []string{"grant-chain-server-test:ping"},
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
		t.Fatal("handler was never dispatched")
	}
	if !capturedIdentity.TrustResolved {
		t.Error("expected TrustResolved=true in handler")
	}
	if capturedIdentity.Anchor == nil {
		t.Fatal("expected Anchor to be set in handler")
	}
	if !capturedIdentity.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("Anchor mismatch: got %x, want %x", capturedIdentity.Anchor, env.pubkey(0))
	}
	if len(capturedIdentity.Chain) != 1 {
		t.Errorf("expected chain length 1 in handler, got %d", len(capturedIdentity.Chain))
	}
	if !capturedIdentity.Chain[0].ChildPubkey.Equal(env.pubkey(1)) {
		t.Errorf("chain[0].ChildPubkey mismatch: got %x, want %x", capturedIdentity.Chain[0].ChildPubkey, env.pubkey(1))
	}
}

// --- Test 5: Server with NoopIdentityResolver → handler sees Chain==nil (backward compat) ---

func TestServer_WithNoopResolver_ChainIsNil(t *testing.T) {
	env := newGrantChainTestEnv(t, 2)
	// No grant, no special resolver — just the default Noop.

	decl := &convention.Declaration{
		Convention: "noop-chain-compat-test",
		Operation:  "ping",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "msg", Type: "string", Required: true, MaxLength: 256},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "noop-chain-compat-test:ping", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}

	var mu sync.Mutex
	var capturedIdentity convention.IdentityInfo
	var dispatched bool

	// NewServer defaults to NoopIdentityResolver — do not set a custom resolver.
	srv := convention.NewServer(env.clients[0], decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("ping", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		capturedIdentity = req.Identity
		dispatched = true
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, env.campfireHex) //nolint:errcheck
	}()

	time.Sleep(50 * time.Millisecond)

	if _, err := env.clients[1].Send(protocol.SendRequest{
		CampfireID: env.campfireHex,
		Tags:       []string{"noop-chain-compat-test:ping"},
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
		t.Fatal("handler was never dispatched")
	}
	// Backward compat: Noop resolver → Chain is nil, TrustResolved is false.
	if capturedIdentity.TrustResolved {
		t.Error("expected TrustResolved=false with NoopIdentityResolver")
	}
	if capturedIdentity.Chain != nil {
		t.Errorf("expected Chain==nil with NoopIdentityResolver, got %v", capturedIdentity.Chain)
	}
	if capturedIdentity.Anchor != nil {
		t.Errorf("expected Anchor==nil with NoopIdentityResolver, got %x", capturedIdentity.Anchor)
	}
	// MachineKey should still be set.
	if capturedIdentity.MachineKey == nil {
		t.Error("expected MachineKey to be set even with NoopIdentityResolver")
	}
}

// --- Test 6: FromConfig honors configured trust anchors ---

func TestFromConfig_HonorsAnchors(t *testing.T) {
	env := newGrantChainTestEnv(t, 2)

	// ids[0] (anchor) grants ids[1] (sender).
	env.postGrant(t, 0, env.pubkey(1), futureGrantExpiry())

	// Build a protocol.Config with ids[0] as a trust anchor.
	cfg := &protocol.Config{
		Identity: protocol.IdentityConfig{
			TrustAnchors: []ed25519.PublicKey{env.pubkey(0)},
		},
	}

	resolver := convention.FromConfig(env.clients[1], env.campfireHex, cfg)
	info, err := resolver.Resolve(context.Background(), env.pubkey(1))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !info.TrustResolved {
		t.Error("expected TrustResolved=true: sender has a valid grant from configured anchor")
	}
	if info.Anchor == nil {
		t.Fatal("expected Anchor to be set")
	}
	if !info.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("Anchor mismatch: got %x, want %x", info.Anchor, env.pubkey(0))
	}
	if len(info.Chain) != 1 {
		t.Errorf("expected chain length 1, got %d", len(info.Chain))
	}
}

// --- Test 7: FromConfig with empty anchors prints warning and fail-closes ---

func TestFromConfig_EmptyAnchors_Warns(t *testing.T) {
	env := newGrantChainTestEnv(t, 2)

	// ids[0] grants ids[1] — but no anchors are configured, so trust can never resolve.
	env.postGrant(t, 0, env.pubkey(1), futureGrantExpiry())

	// Redirect stderr to capture the warning.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	cfg := &protocol.Config{} // TrustAnchors is nil/empty

	resolver := convention.FromConfig(env.clients[1], env.campfireHex, cfg)

	w.Close()
	os.Stderr = origStderr

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	r.Close()
	stderrOutput := string(buf[:n])

	if stderrOutput == "" {
		t.Error("expected a warning to stderr when no anchors are configured, got empty output")
	}

	// Resolver should fail-close: no valid resolution without anchors.
	info, err := resolver.Resolve(context.Background(), env.pubkey(1))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.TrustResolved {
		t.Error("expected TrustResolved=false when no trust anchors are configured")
	}
	if info.Chain != nil {
		t.Errorf("expected Chain==nil for fail-closed resolver, got %v", info.Chain)
	}
}

// --- Test 8: CompositeResolver Chain comes from TrustResolved setter (10d) ---

// fakeChainResolver is a stub IdentityResolver that returns a fixed IdentityInfo
// (including Chain and Anchor) regardless of the sender pubkey. Used to isolate
// CompositeResolver Chain/Anchor merge logic in 10d tests.
type fakeChainResolver struct {
	name string
	info convention.IdentityInfo
}

func (f *fakeChainResolver) Name() string { return f.name }
func (f *fakeChainResolver) Resolve(_ context.Context, senderPubkey ed25519.PublicKey) (convention.IdentityInfo, error) {
	out := f.info
	out.MachineKey = senderPubkey
	return out, nil
}

// TestCompositeResolver_ChainFromTrustResolver verifies that Chain and Anchor are
// adopted from the resolver that set TrustResolved=true, not from an earlier resolver
// that set Chain without TrustResolved. Regression for campfire-1dc.
func TestCompositeResolver_ChainFromTrustResolver(t *testing.T) {
	env := newGrantChainTestEnv(t, 3)

	// fake-a: sets Chain but TrustResolved=false.
	chainA := []delegation.GrantInfo{{MessageID: "grant-from-fake-a"}}
	anchorA := env.pubkey(2)
	fakeA := &fakeChainResolver{
		name: "fake-a",
		info: convention.IdentityInfo{
			Chain:         chainA,
			Anchor:        anchorA,
			TrustResolved: false,
		},
	}

	// fake-b: sets TrustResolved=true with a different Chain.
	chainB := []delegation.GrantInfo{{MessageID: "grant-from-fake-b"}}
	anchorB := env.pubkey(0)
	fakeB := &fakeChainResolver{
		name: "fake-b",
		info: convention.IdentityInfo{
			Chain:         chainB,
			Anchor:        anchorB,
			TrustResolved: true,
		},
	}

	composite := convention.NewCompositeResolver(fakeA, fakeB)
	info, err := composite.Resolve(context.Background(), env.pubkey(1))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !info.TrustResolved {
		t.Error("expected TrustResolved=true")
	}

	// Chain must come from fake-b (the TrustResolved setter), not from fake-a.
	if len(info.Chain) != 1 || info.Chain[0].MessageID != "grant-from-fake-b" {
		t.Errorf("expected Chain from fake-b (grant-from-fake-b), got %+v", info.Chain)
	}
	if info.Anchor == nil || !info.Anchor.Equal(anchorB) {
		t.Errorf("expected Anchor from fake-b (%x), got %x", anchorB, info.Anchor)
	}
	if info.Provenance["trust_resolved"] != "fake-b" {
		t.Errorf("Provenance[trust_resolved] = %q; want %q", info.Provenance["trust_resolved"], "fake-b")
	}
}

// TestFromConfig_NilConfig verifies that FromConfig handles a nil config
// gracefully without panicking, and returns a functional (fail-closed) resolver.
func TestFromConfig_NilConfig(t *testing.T) {
	env := newGrantChainTestEnv(t, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Call FromConfig with nil config.
	resolver := convention.FromConfig(env.clients[0], env.campfireHex, nil)
	if resolver == nil {
		t.Fatal("FromConfig should return a non-nil resolver even for nil config")
	}

	// Resolver should be functional but fail-closed (no trust anchors).
	// Attempting to resolve any sender should return untrusted (TrustResolved = false).
	info, err := resolver.Resolve(ctx, env.pubkey(1))
	if err != nil {
		t.Fatalf("Resolve should not error for nil config: %v", err)
	}
	if info.TrustResolved {
		t.Error("resolver with nil config should fail-closed (TrustResolved = false)")
	}
}
