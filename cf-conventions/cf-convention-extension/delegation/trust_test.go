package delegation_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extension/delegation"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// storeSeq is a process-wide counter for generating unique in-memory store names.
// Required because -count=N runs the same test N times in the same process; each
// run must use distinct SQLite in-memory database names to remain isolated.
var storeSeq atomic.Int64

// listErrAfterNStore wraps a real store.Store and injects an error on the
// Nth ListMessages call (1-based). Used to test ReadError outcomes without
// requiring an external mock framework.
type listErrAfterNStore struct {
	store.Store
	n       int32 // fail starting at this call number (1-based)
	callNum atomic.Int32
	err     error
}

func (s *listErrAfterNStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	n := int(s.callNum.Add(1))
	if n >= int(s.n) {
		return nil, s.err
	}
	return s.Store.ListMessages(campfireID, afterTimestamp, filter...)
}

// limitingStore wraps a real store.Store and caps the number of messages
// returned by ListMessages to at most maxRecords. Combined with the store's
// Reverse ordering, this simulates MaxGrantReadLimit truncation at the store
// layer — used to prove that Reverse: true in findValidGrant preserves
// newest-first ordering under truncation (campfire-213 / fix 9d).
type limitingStore struct {
	store.Store
	maxRecords int
}

func (s *limitingStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	msgs, err := s.Store.ListMessages(campfireID, afterTimestamp, filter...)
	if err != nil || s.maxRecords <= 0 || len(msgs) <= s.maxRecords {
		return msgs, err
	}
	return msgs[:s.maxRecords], nil
}

// trustTestEnv is the scaffold for trust resolution integration tests.
type trustTestEnv struct {
	campfireIDRaw []byte // raw 32-byte ed25519 pubkey
	campfireHex   string // hex form of campfireIDRaw
	transportDir  string
	fsTransport   *fs.Transport
	identities    []*identity.Identity
	clients       []*protocol.Client
	stores        []store.Store
}

// newTrustTestEnv sets up a filesystem campfire with n member identities.
// Each identity gets its own SQLite in-memory store and protocol.Client.
// In-memory stores avoid disk I/O, making each test run ~10× faster and
// preventing TestResolve_DepthExceeded (12 stores) from taking 4+ seconds
// under -race, which caused the full delegation suite to time out at high
// -count values (root cause of the reported hang — campfireagent-90f).
// Index 0 is conventionally the "root" (anchor) identity.
func newTrustTestEnv(t *testing.T, n int) *trustTestEnv {
	t.Helper()

	transportDir := t.TempDir()

	// Generate campfire identity.
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireIDRaw := cfID.PublicKey
	campfireHex := cfID.PublicKeyHex()

	// Set up transport directory structure.
	cfDir := filepath.Join(transportDir, campfireHex)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	// Write campfire state (campfire.cbor).
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

	// Generate identities and register them as campfire members.
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

	// Shared membership record (same transport dir for all clients).
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
		// Use a unique in-memory store name to avoid both intra-run collisions
		// (N stores per test) and inter-run collisions (-count=N runs same test N
		// times in the same process). The storeSeq counter is process-wide and
		// monotonically increasing, so each store gets a unique name.
		seq := storeSeq.Add(1)
		storeName := fmt.Sprintf("trust_test_%d_%s", seq, ids[i].PublicKeyHex()[:16])
		s, err := store.OpenMemory(storeName)
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

	env := &trustTestEnv{
		campfireIDRaw: campfireIDRaw,
		campfireHex:   campfireHex,
		transportDir:  transportDir,
		fsTransport:   tr,
		identities:    ids,
		clients:       clients,
		stores:        stores,
	}
	return env
}

// postGrant sends an identity:granted message from identity parentIdx delegating to child.
func (e *trustTestEnv) postGrant(t *testing.T, parentIdx int, child ed25519.PublicKey, expiresAt int64) {
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

// postRevoke sends an identity:revoked message from identity parentIdx for child.
func (e *trustTestEnv) postRevoke(t *testing.T, parentIdx int, child ed25519.PublicKey) {
	t.Helper()
	payload := map[string]interface{}{
		"child_pubkey": hex.EncodeToString(child),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling revoke payload: %v", err)
	}
	if _, err := e.clients[parentIdx].Send(protocol.SendRequest{
		CampfireID: e.campfireHex,
		Payload:    b,
		Tags:       []string{delegation.RevokedTag},
	}); err != nil {
		t.Fatalf("posting revoke from %d: %v", parentIdx, err)
	}
	e.syncAll(t)
}

// syncAll pulls all filesystem messages into every store so every client
// can see messages posted by every other client.
func (e *trustTestEnv) syncAll(t *testing.T) {
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

// futureExpiry returns a unix timestamp 1 day from now.
func futureExpiry() int64 { return time.Now().Unix() + 86400 }

// pastExpiry returns an expired unix timestamp (well past the 60-second skew slack).
func pastExpiry() int64 { return time.Now().Unix() - 120 }

// pubkey returns the ed25519.PublicKey for identity index i.
func (e *trustTestEnv) pubkey(i int) ed25519.PublicKey { return e.identities[i].PublicKey }

// anchors returns a slice of pubkeys for the given identity indices.
func (e *trustTestEnv) anchors(indices ...int) []ed25519.PublicKey {
	result := make([]ed25519.PublicKey, len(indices))
	for i, idx := range indices {
		result[i] = e.identities[idx].PublicKey
	}
	return result
}

// ---- Test cases ----

// Test 1: Sender is a trust anchor → Resolved with empty chain.
func TestResolve_SenderIsAnchor(t *testing.T) {
	env := newTrustTestEnv(t, 1)
	ctx := context.Background()

	out := delegation.Resolve(ctx, env.clients[0], env.campfireIDRaw, env.pubkey(0), env.anchors(0))

	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved, got %T: %+v", out, out)
	}
	if len(r.Chain) != 0 {
		t.Errorf("expected empty chain for anchor sender, got len=%d", len(r.Chain))
	}
	if !r.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("expected anchor == sender")
	}
}

// Test 2: Sender has valid grant from anchor → Resolved{chain:[grant], anchor:parent}.
func TestResolve_ValidGrantFromAnchor(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// ids[0] (anchor) grants ids[1] (sender).
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved, got %T: %+v", out, out)
	}
	if len(r.Chain) != 1 {
		t.Errorf("expected chain length 1, got %d", len(r.Chain))
	}
	if !r.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("expected anchor to be ids[0]")
	}
}

// Test 3: Sender has valid 3-hop chain → Resolved{chain:[g1,g2], anchor:root}.
// Chain: ids[0] (anchor) → ids[1] → ids[2] (sender).
func TestResolve_ThreeHopChain(t *testing.T) {
	env := newTrustTestEnv(t, 3)
	ctx := context.Background()

	env.postGrant(t, 0, env.pubkey(1), futureExpiry()) // root → mid
	env.postGrant(t, 1, env.pubkey(2), futureExpiry()) // mid → leaf

	out := delegation.Resolve(ctx, env.clients[2], env.campfireIDRaw, env.pubkey(2), env.anchors(0))

	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved, got %T: %+v", out, out)
	}
	if len(r.Chain) != 2 {
		t.Errorf("expected chain length 2, got %d", len(r.Chain))
	}
	if !r.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("expected anchor to be ids[0]")
	}
	// Chain[0] is ids[1]→ids[2] grant, Chain[1] is ids[0]→ids[1] grant.
	if !r.Chain[0].ParentPubkey.Equal(env.pubkey(1)) {
		t.Errorf("chain[0].Parent should be ids[1]")
	}
}

// Test 4: Sender has no grant → DeadEnd.
func TestResolve_NoGrant(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	out := delegation.Resolve(ctx, env.clients[0], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	d, ok := out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd, got %T: %+v", out, out)
	}
	if !d.LastResolved.Equal(env.pubkey(1)) {
		t.Errorf("expected LastResolved == sender")
	}
	if len(d.Chain) != 0 {
		t.Errorf("expected empty chain, got %d", len(d.Chain))
	}
}

// Test 5: Sender's grant has bad signature → DeadEnd (invalid grants are skipped per spec §4).
// We directly inject a tampered record into all stores.
func TestResolve_BadSignatureGrant(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	childHex := hex.EncodeToString(env.pubkey(1))
	parentHex := hex.EncodeToString(env.pubkey(0))

	// Inject a grant record with an invalid signature into all stores.
	tamperedRec := store.MessageRecord{
		ID:          "tampered-sig-grant",
		CampfireID:  env.campfireHex,
		Sender:      parentHex,
		Payload:     []byte(`{"child_pubkey":"` + childHex + `","campfire_id":"` + env.campfireHex + `","expires_at":9999999999}`),
		Tags:        []string{delegation.GrantedTag},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Signature:   []byte("invalidsig"),
		Provenance:  []message.ProvenanceHop{},
	}
	for _, s := range env.stores {
		s.AddMessage(tamperedRec) //nolint:errcheck
	}

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	// Per spec §4, invalid grants are skipped. With no valid grant remaining, the result is DeadEnd.
	_, ok := out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd (invalid grant skipped per spec), got %T: %+v", out, out)
	}
}

// Test 6: Sender's grant has wrong campfire_id → DeadEnd (invalid grants are skipped per spec §4).
func TestResolve_WrongCampfireID(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	const wrongCampfire = "1111111111111111111111111111111111111111111111111111111111111111"
	payload := map[string]interface{}{
		"child_pubkey": hex.EncodeToString(env.pubkey(1)),
		"campfire_id":  wrongCampfire,
		"expires_at":   futureExpiry(),
	}
	b, _ := json.Marshal(payload)
	if _, err := env.clients[0].Send(protocol.SendRequest{
		CampfireID: env.campfireHex,
		Payload:    b,
		Tags:       []string{delegation.GrantedTag},
	}); err != nil {
		t.Fatalf("posting grant: %v", err)
	}
	env.syncAll(t)

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	// Per spec §4, invalid grants are skipped. With no valid grant remaining, the result is DeadEnd.
	_, ok := out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd (invalid grant skipped per spec), got %T: %+v", out, out)
	}
}

// Test 7: Sender's grant is expired → DeadEnd (invalid grants are skipped per spec §4).
func TestResolve_ExpiredGrant(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	env.postGrant(t, 0, env.pubkey(1), pastExpiry())

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	// Per spec §4, expired grants are skipped (continue). With no valid grant, DeadEnd is returned.
	_, ok := out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd (expired grant skipped per spec), got %T: %+v", out, out)
	}
}

// Test 8: Chain depth exceeds 10 → DepthExceeded.
// Build a 12-identity chain where each identity grants the next.
func TestResolve_DepthExceeded(t *testing.T) {
	const chainLen = 12
	env := newTrustTestEnv(t, chainLen)
	ctx := context.Background()

	// ids[0] (anchor) → ids[1] → ... → ids[11] (sender).
	for i := 0; i < chainLen-1; i++ {
		env.postGrant(t, i, env.pubkey(i+1), futureExpiry())
	}

	// Resolve ids[11] with ids[0] as anchor. The walk terminates at depth 10 (MaxChainDepth).
	out := delegation.Resolve(ctx, env.clients[chainLen-1], env.campfireIDRaw, env.pubkey(chainLen-1), env.anchors(0))

	_, ok := out.(delegation.DepthExceeded)
	if !ok {
		t.Fatalf("expected DepthExceeded for 12-hop chain, got %T: %+v", out, out)
	}
}

// Test 9: Sender's grant is revoked → DeadEnd.
func TestResolve_RevokedGrant(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	env.postGrant(t, 0, env.pubkey(1), futureExpiry())
	env.postRevoke(t, 0, env.pubkey(1))

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	_, ok := out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd for revoked grant, got %T: %+v", out, out)
	}
}

// Test 10: Sender's grant revoked then re-granted → Resolved.
func TestResolve_RevokedThenRegranted(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	env.postGrant(t, 0, env.pubkey(1), futureExpiry())
	env.postRevoke(t, 0, env.pubkey(1))
	// Small pause to ensure the re-grant has a later timestamp than the revoke.
	time.Sleep(2 * time.Millisecond)
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved after re-grant, got %T: %+v", out, out)
	}
	if !r.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("expected anchor to be ids[0]")
	}
}

// Test 11: Subtree cascade — revoke mid-node → grandchild DeadEnd.
// Chain: ids[0] (anchor) → ids[1] (mid) → ids[2] (leaf).
// Revoke ids[0]→ids[1] → ids[2] resolution should DeadEnd at ids[1].
func TestResolve_SubtreeCascade(t *testing.T) {
	env := newTrustTestEnv(t, 3)
	ctx := context.Background()

	env.postGrant(t, 0, env.pubkey(1), futureExpiry()) // root → mid
	env.postGrant(t, 1, env.pubkey(2), futureExpiry()) // mid → leaf
	env.postRevoke(t, 0, env.pubkey(1))                // revoke root → mid

	out := delegation.Resolve(ctx, env.clients[2], env.campfireIDRaw, env.pubkey(2), env.anchors(0))

	_, ok := out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd for grandchild after subtree revoke, got %T: %+v", out, out)
	}
}

// Test 12a: Newest grant is expired, older valid grant exists → Resolved (regression for spec §4).
// Before the fix, findValidGrant would return InvalidGrant on the first (expired) candidate
// instead of continuing to the older valid grant.
func TestResolve_ExpiredNewerGrantFallsBackToValidOlder(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// Post an older valid grant first, then a newer expired one.
	// syncAll ensures both are in every store before resolving.
	env.postGrant(t, 0, env.pubkey(1), futureExpiry()) // older, valid
	time.Sleep(2 * time.Millisecond)
	env.postGrant(t, 0, env.pubkey(1), pastExpiry()) // newer, expired

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	// The expired (newest) grant must be skipped and the older valid grant used.
	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved (valid older grant), got %T: %+v", out, out)
	}
	if !r.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("expected anchor to be ids[0]")
	}
}

// Test 12: Multiple grants from different parents; revoke one → still Resolved via other.
// ids[0] (anchor-A) grants ids[2]; ids[1] (anchor-B) grants ids[2].
// Revoke ids[0]→ids[2]; ids[2] should still resolve via ids[1].
func TestResolve_MultipleGrantsRevokeOne(t *testing.T) {
	env := newTrustTestEnv(t, 3)
	ctx := context.Background()

	env.postGrant(t, 0, env.pubkey(2), futureExpiry()) // anchor-A → leaf
	env.postGrant(t, 1, env.pubkey(2), futureExpiry()) // anchor-B → leaf
	env.postRevoke(t, 0, env.pubkey(2))                // revoke anchor-A → leaf

	// Both ids[0] and ids[1] are anchors.
	out := delegation.Resolve(ctx, env.clients[2], env.campfireIDRaw, env.pubkey(2), env.anchors(0, 1))

	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved via second parent, got %T: %+v", out, out)
	}
	if !r.Anchor.Equal(env.pubkey(1)) {
		t.Errorf("expected anchor to be ids[1], got %x", r.Anchor)
	}
}

// TestResolve_StoreReadError_ReturnsReadError verifies that when client.Read
// fails while looking up identity:granted messages, Resolve returns ReadError
// (not DeadEnd, which was the silent pre-campfire-188 behaviour).
func TestResolve_StoreReadError_ReturnsReadError(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// Wrap the store for ids[1] so that ListMessages always returns an error,
	// simulating a store I/O failure on the grant read.
	storeErr := errors.New("injected read error")
	errStore := &listErrAfterNStore{
		Store: env.stores[1],
		n:     1, // fail on the very first ListMessages call (grant lookup)
		err:   storeErr,
	}
	faultyClient := protocol.New(errStore, env.identities[1])

	out := delegation.Resolve(ctx, faultyClient, env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	re, ok := out.(delegation.ReadError)
	if !ok {
		t.Fatalf("expected ReadError when grant read fails, got %T: %+v", out, out)
	}
	if !errors.Is(re.Err, delegation.ErrStoreRead) {
		t.Errorf("expected Err to wrap ErrStoreRead, got %v", re.Err)
	}
	if !re.Target.Equal(env.pubkey(1)) {
		t.Errorf("expected Target == sender pubkey, got %x", re.Target)
	}
}

// TestResolve_RevokeStoreReadError_ReturnsReadError verifies that when
// client.Read fails while checking identity:revoked messages (after a valid
// grant has been found), Resolve returns ReadError rather than silently
// treating the unreadable revocation log as "not revoked" (campfire-188).
func TestResolve_RevokeStoreReadError_ReturnsReadError(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// Post a valid grant so there is a candidate for the trust walker to find.
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())

	// Wrap the store for ids[1] so that the second ListMessages call (revoke
	// lookup) returns an error. The first call (grant lookup) succeeds, finding
	// the grant posted above.
	storeErr := errors.New("injected revoke read error")
	errStore := &listErrAfterNStore{
		Store: env.stores[1],
		n:     2, // fail on the second ListMessages call (revoke lookup)
		err:   storeErr,
	}
	faultyClient := protocol.New(errStore, env.identities[1])

	out := delegation.Resolve(ctx, faultyClient, env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	re, ok := out.(delegation.ReadError)
	if !ok {
		t.Fatalf("expected ReadError when revoke read fails, got %T: %+v", out, out)
	}
	if !errors.Is(re.Err, delegation.ErrStoreRead) {
		t.Errorf("expected Err to wrap ErrStoreRead, got %v", re.Err)
	}
}

// ---- Security hardening regression tests (campfire-ed7) ----

// postGrantWithChildHex sends an identity:granted message with a caller-specified
// child_pubkey hex string (may be uppercase or otherwise non-canonical).
// This is the test-only escape hatch for 9a regression tests.
func (e *trustTestEnv) postGrantWithChildHex(t *testing.T, parentIdx int, childHex string, expiresAt int64) {
	t.Helper()
	payload := map[string]interface{}{
		"child_pubkey": childHex,
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

// postRevokeWithChildHex sends an identity:revoked message with a caller-specified
// child_pubkey hex string. Used to test uppercase-hex revocation (9a).
func (e *trustTestEnv) postRevokeWithChildHex(t *testing.T, parentIdx int, childHex string) {
	t.Helper()
	payload := map[string]interface{}{
		"child_pubkey": childHex,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling revoke payload: %v", err)
	}
	if _, err := e.clients[parentIdx].Send(protocol.SendRequest{
		CampfireID: e.campfireHex,
		Payload:    b,
		Tags:       []string{delegation.RevokedTag},
	}); err != nil {
		t.Fatalf("posting revoke from %d: %v", parentIdx, err)
	}
	e.syncAll(t)
}

// TestResolve_UppercaseHexRevoke verifies that a revocation message whose
// child_pubkey is uppercase hex is correctly matched against the lowercase childHex
// produced by hex.EncodeToString. Before fix 9a, the case mismatch caused the
// revocation to be silently ignored and the grant to resolve as Resolved instead
// of DeadEnd (campfire-f37).
func TestResolve_UppercaseHexRevoke(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	childHexLower := hex.EncodeToString(env.pubkey(1))
	childHexUpper := strings.ToUpper(childHexLower)

	// Post grant with lowercase child_pubkey (normal path).
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())

	// Post revocation with UPPERCASE child_pubkey — must still match.
	env.postRevokeWithChildHex(t, 0, childHexUpper)

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	_, ok := out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd (uppercase revoke applied), got %T: %+v", out, out)
	}
	_ = childHexLower
}

// TestResolve_UppercaseHexGrant verifies that a grant message whose child_pubkey
// is uppercase hex is correctly matched when resolving (9a: lowercase normalization
// in the grant filtering path, campfire-f37).
func TestResolve_UppercaseHexGrant(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	childHexUpper := strings.ToUpper(hex.EncodeToString(env.pubkey(1)))

	// Post grant with UPPERCASE child_pubkey.
	env.postGrantWithChildHex(t, 0, childHexUpper, futureExpiry())

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved (uppercase grant matched), got %T: %+v", out, out)
	}
	if !r.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("expected anchor to be ids[0]")
	}
}

// countRevokeReadStore counts how many times ListMessages is called with the
// RevokedTag filter. Used to assert O(N) revoke reads in 9b.
type countRevokeReadStore struct {
	store.Store
	count int32 // atomic read count
}

func (s *countRevokeReadStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	if len(filter) > 0 {
		for _, tag := range filter[0].Tags {
			if tag == delegation.RevokedTag {
				atomic.AddInt32(&s.count, 1)
				break
			}
		}
	}
	return s.Store.ListMessages(campfireID, afterTimestamp, filter...)
}

// TestFindValidGrant_SingleRevokeRead asserts that findValidGrant (exercised via
// Resolve) reads revocations exactly once regardless of how many grant candidates
// exist for the child. Before fix 9b, each candidate triggered a separate
// client.Read for revocations — O(N*M) reads enabling a DoS via grant flood
// (campfire-75f). After fix 9b, revocations are pre-fetched once.
func TestFindValidGrant_SingleRevokeRead(t *testing.T) {
	const numGrants = 5
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// Post numGrants valid grants for ids[1] from ids[0] with small time offsets.
	for i := 0; i < numGrants; i++ {
		env.postGrant(t, 0, env.pubkey(1), futureExpiry())
		time.Sleep(time.Millisecond) // ensure distinct timestamps
	}

	// Wrap ids[1]'s store to count revoke reads.
	counting := &countRevokeReadStore{Store: env.stores[1]}
	countingClient := protocol.New(counting, env.identities[1])

	out := delegation.Resolve(ctx, countingClient, env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	_, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved, got %T: %+v", out, out)
	}

	// With pre-fetched revocations (9b), there must be exactly one revoke read
	// regardless of how many grant candidates existed.
	if got := atomic.LoadInt32(&counting.count); got != 1 {
		t.Errorf("expected exactly 1 revoke read, got %d (O(N) violation)", got)
	}
}

// TestResolve_EmptyCampfireID verifies that Resolve rejects an empty campfireID
// as InvalidInput (9c: campfireID length validation, campfire-13b; campfireagent-8a7).
func TestResolve_EmptyCampfireID(t *testing.T) {
	env := newTrustTestEnv(t, 1)
	ctx := context.Background()

	out := delegation.Resolve(ctx, env.clients[0], []byte{}, env.pubkey(0), env.anchors(0))

	ii, ok := out.(delegation.InvalidInput)
	if !ok {
		t.Fatalf("expected InvalidInput for empty campfireID, got %T: %+v", out, out)
	}
	if ii.Err == nil {
		t.Error("expected non-nil Err in InvalidInput")
	}
}

// TestResolve_ShortCampfireID verifies that Resolve rejects a campfireID that is
// shorter than 32 bytes as InvalidInput (9c: campfireID length validation,
// campfire-13b; campfireagent-8a7).
func TestResolve_ShortCampfireID(t *testing.T) {
	env := newTrustTestEnv(t, 1)
	ctx := context.Background()

	shortID := make([]byte, 16) // only 16 bytes — wrong length

	out := delegation.Resolve(ctx, env.clients[0], shortID, env.pubkey(0), env.anchors(0))

	ii, ok := out.(delegation.InvalidInput)
	if !ok {
		t.Fatalf("expected InvalidInput for short campfireID, got %T: %+v", out, out)
	}
	if ii.Err == nil {
		t.Error("expected non-nil Err in InvalidInput")
	}
}

// TestResolve_NewestGrantFoundDespiteTruncation verifies that fix 9d (Reverse:
// true on the grant Read) is load-bearing: when the store returns only the
// newest N grants due to truncation, Resolve finds a valid grant even if all
// older grants are expired.
//
// Setup: 3 grants in ascending timestamp order —
//   - grant 1 (oldest):  expired
//   - grant 2 (middle):  expired
//   - grant 3 (newest):  valid (future expiry)
//
// The store is wrapped with limitingStore{maxRecords: 2} so only 2 messages are
// returned from ListMessages.
//
//   - With Reverse: true  → store returns [grant3, grant2]; both visible; grant3 is
//     valid → Resolve returns Resolved. ✓
//   - Without Reverse: true → store returns [grant1, grant2]; both expired →
//     Resolve returns DeadEnd. ✗
//
// The test confirms Resolved. To prove it would fail without Reverse: true,
// temporarily remove that flag from findValidGrant and re-run — the test fails
// with DeadEnd. (campfire-213 / fix 9d)
func TestResolve_NewestGrantFoundDespiteTruncation(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// Post 3 grants in timestamp order: two expired, then one valid.
	env.postGrant(t, 0, env.pubkey(1), pastExpiry()) // grant 1 — oldest, expired
	time.Sleep(2 * time.Millisecond)
	env.postGrant(t, 0, env.pubkey(1), pastExpiry()) // grant 2 — middle, expired
	time.Sleep(2 * time.Millisecond)
	env.postGrant(t, 0, env.pubkey(1), futureExpiry()) // grant 3 — newest, valid

	// Wrap ids[1]'s store to return at most 2 messages per ListMessages call.
	// This forces store-level truncation: only 2 of the 3 grants are visible.
	//
	// With Reverse: true (the fix): the store sorts DESC before truncating, so it
	// returns [grant3, grant2]. grant3 is valid → Resolved.
	//
	// Without Reverse: true (the bug): the store sorts ASC, returns [grant1,
	// grant2]. Both expired → DeadEnd.
	limitedStore := &limitingStore{Store: env.stores[1], maxRecords: 2}
	limitedClient := protocol.New(limitedStore, env.identities[1])

	out := delegation.Resolve(ctx, limitedClient, env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved (newest grant visible after truncation), got %T: %+v", out, out)
	}
	if !r.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("expected anchor to be ids[0], got %x", r.Anchor)
	}
}

// TestFindValidGrant_AtLimit verifies that findValidGrant correctly reads grants
// even when the store truncates near the read limit. This simulates the behavior
// when MaxGrantReadLimit causes store reads to return fewer grants than would
// normally be posted.
func TestFindValidGrant_AtLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	env := newTrustTestEnv(t, 2)

	// Post a valid grant from ids[0] to ids[1].
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())

	// Create a store that truncates to 1 result, forcing the read to hit the limit.
	limitedStore := &limitingStore{Store: env.stores[0], maxRecords: 1}
	limitedClient := protocol.New(limitedStore, env.identities[0])

	// Call Resolve with the limited client. Even though the store truncates,
	// the valid grant should be found and a Resolved outcome returned.
	out := delegation.Resolve(ctx, limitedClient, env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved even with store limit, got %T: %+v", out, out)
	}
	if !r.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("expected anchor to be ids[0], got %x", r.Anchor)
	}
}

// TestResolve_NewestRevokeFoundDespiteTruncation is a regression test for
// campfire-5eb: findValidGrant must use Reverse:true when reading revocations
// so that, under store-level truncation, the newest revocation is preserved.
//
// Setup:
//   - ids[0] (anchor) grants ids[1] (valid, future expiry).
//   - 3 old "noise" revocations for ids[1] are injected directly into the store
//     with timestamps older than the real grant.
//   - A real revocation is posted AFTER the grant (newer timestamp).
//   - The store is wrapped with limitingStore{maxRecords: 2} so only 2 messages
//     are returned per ListMessages call.
//
//   - With Reverse:true (the fix): DESC ordering preserves [real_revoke, noise1];
//     real_revoke.Timestamp > grant.Timestamp → revoked → DeadEnd. ✓
//   - Without Reverse:true (the bug): ASC ordering returns [noise1, noise2]; both
//     precede the grant timestamp → revokedSet returns false → Resolved (wrong). ✗
func TestResolve_NewestRevokeFoundDespiteTruncation(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// Post the valid grant.
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())
	time.Sleep(2 * time.Millisecond)

	// Record the grant timestamp so we can use it to anchor old noise revokes.
	// The grant is the most recent message in the store at this point.
	// We need old noise revokes with timestamps BEFORE the grant so they don't
	// themselves trigger revocation (revokedSet only fires when ts > grantTimestamp).
	// We'll inject them as store records directly — postRevoke would give them
	// current timestamps (after the grant), which would also work but would make
	// the test less precise. Instead, inject 3 old records with small timestamps.
	childHex := hex.EncodeToString(env.pubkey(1))
	parentHex := hex.EncodeToString(env.pubkey(0))
	noisePayload, err := json.Marshal(map[string]string{"child_pubkey": childHex})
	if err != nil {
		t.Fatalf("marshal noise payload: %v", err)
	}
	for i := 0; i < 3; i++ {
		noiseRec := store.MessageRecord{
			ID:          fmt.Sprintf("noise-revoke-%d", i),
			CampfireID:  env.campfireHex,
			Sender:      parentHex,
			Payload:     noisePayload,
			Tags:        []string{delegation.RevokedTag},
			Antecedents: []string{},
			Timestamp:   int64(i + 1), // tiny timestamps — well before the grant
			Signature:   []byte("placeholder"), // not verified by buildRevokedSet
			Provenance:  []message.ProvenanceHop{},
		}
		for _, s := range env.stores {
			s.AddMessage(noiseRec) //nolint:errcheck
		}
	}

	// Post the real revocation (newest timestamp — must be preserved under truncation).
	time.Sleep(2 * time.Millisecond)
	env.postRevoke(t, 0, env.pubkey(1))

	// Wrap ids[1]'s store to return at most 2 messages per ListMessages call.
	// With Reverse:true (fix): DESC order → [real_revoke, noise2]; real_revoke has
	// timestamp > grant → revoked → DeadEnd (correct).
	// Without Reverse:true (bug): ASC order → [noise0, noise1]; both have tiny
	// timestamps < grant → not revoked → Resolved (wrong).
	limitedStore := &limitingStore{Store: env.stores[1], maxRecords: 2}
	limitedClient := protocol.New(limitedStore, env.identities[1])

	out := delegation.Resolve(ctx, limitedClient, env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	_, ok := out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd (real revoke visible after truncation with Reverse:true), got %T: %+v", out, out)
	}
}

// TestOutcome_InterfaceSatisfied verifies that each Outcome type implements
// the Outcome interface and can be type-asserted and called.
func TestOutcome_InterfaceSatisfied(t *testing.T) {
	// Test Resolved.
	resolved := delegation.Resolved{
		Anchor: make([]byte, 32),
	}
	var o delegation.Outcome = resolved
	if o == nil {
		t.Error("Resolved should satisfy Outcome interface")
	}

	// Test DeadEnd.
	deadEnd := delegation.DeadEnd{}
	o = deadEnd
	if o == nil {
		t.Error("DeadEnd should satisfy Outcome interface")
	}

	// Test InvalidGrant.
	invalidGrant := delegation.InvalidGrant{}
	o = invalidGrant
	if o == nil {
		t.Error("InvalidGrant should satisfy Outcome interface")
	}

	// Test InvalidInput.
	invalidInput := delegation.InvalidInput{}
	o = invalidInput
	if o == nil {
		t.Error("InvalidInput should satisfy Outcome interface")
	}

	// Test DepthExceeded.
	depthExceeded := delegation.DepthExceeded{}
	o = depthExceeded
	if o == nil {
		t.Error("DepthExceeded should satisfy Outcome interface")
	}

	// Test ReadError.
	readError := delegation.ReadError{Err: delegation.ErrStoreRead}
	o = readError
	if o == nil {
		t.Error("ReadError should satisfy Outcome interface")
	}
}
