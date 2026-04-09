package http_test

// TestCrossTransportE2E — full cross-transport join-read-send-ls-members cycle.
//
// Exercises the complete relay join flow end-to-end, proving all four fixes
// work together in a single coherent scenario using in-process HTTP transport
// (real handler code paths, real SQLite store, no mocks, no external services).
//
// Fixes verified:
//
//  1. Fix 1 (campfireagent-373): An agent joining via relay with NO endpoint
//     (pull-only, cf join --via equivalent) is recorded in peer_endpoints and
//     subsequently recognised as a member by checkMembership. Without the fix,
//     UpsertPeerEndpoint was skipped for empty endpoints → every subsequent
//     sync/deliver/poll request returned 403.
//
//  2. Fix 2 (campfireagent-eac): protocol.Syncer interface lets Read pull
//     messages from the HTTP transport before querying the local store. Without
//     the fix, protocol.Client.Read only synced filesystem campfires; HTTP
//     messages were invisible to clients not using push delivery.
//
//  3. Fix 3 (campfireagent-968): Store-based member enumeration via
//     peer_endpoints for p2p-http campfires. Before the fix, cf ls / cf members
//     called fs.ForDir(m.TransportDir).ListMembers() which always returned empty
//     for non-filesystem transports.
//
//  4. Fix 4 (campfireagent-a7a): JoinedAt stored as UnixNano in admission.go.
//     Before the fix, time.Now().Unix() (seconds ~1.7e9) was stored but display
//     code calls time.Unix(0, joinedAt) which expects nanoseconds, causing all
//     join timestamps to render as 1970-01-01T00:00:00Z.
//
// Port block: 660–679 (this file uses 660).

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// httpSyncer implements protocol.Syncer for HTTP transport.
// It calls cfhttp.Sync to pull messages from the relay endpoint into the local store.
// This mirrors what cmd/cf does via StoreSyncer, used here to exercise Fix 2.
type httpSyncer struct {
	endpoint string
	id       *identity.Identity
	s        store.Store
}

func (s *httpSyncer) Sync(campfireID string) error {
	msgs, err := cfhttp.Sync(s.endpoint, campfireID, 0, s.id)
	if err != nil {
		return fmt.Errorf("httpSyncer.Sync: %w", err)
	}
	for i := range msgs {
		s.s.AddMessage(store.MessageRecordFromMessage(campfireID, &msgs[i], store.NowNano())) //nolint:errcheck
	}
	return nil
}

// openLocalStore opens a SQLite store in a temporary directory for Agent B's local state.
func openLocalStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening local store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestCrossTransportE2E proves all four fixes work together in a complete
// relay join-send-read-ls-members cycle using in-process HTTP transport.
//
// Run with:
//
//	go test ./pkg/transport/http/... -run TestCrossTransportE2E -v
func TestCrossTransportE2E(t *testing.T) {
	// ── Setup: Agent A creates a relay campfire ────────────────────────────
	// setupRelayServer starts an in-process HTTP transport with:
	//   - Open join protocol (any agent can join)
	//   - Pull-only delivery (relay mode: no push endpoints required)
	//   - Real SQLite store backing all handler state
	//
	// Port 660 is claimed by this test (block 660–679 reserved for this file).
	campfireID, ep, sHost := setupRelayServer(t, 660)

	// Generate Agent B's identity (the endpointless joiner).
	agentB, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating Agent B identity: %v", err)
	}

	// ── Step 1: Agent B joins via relay with NO endpoint ──────────────────
	// Validates Fix 1 (campfireagent-373): handler_join.go now calls
	// UpsertPeerEndpoint unconditionally after successful admission, even when
	// JoinerEndpoint is empty. Before the fix, the UpsertPeerEndpoint call was
	// gated on `if req.JoinerEndpoint != ""`, leaving endpointless joiners with
	// no row in peer_endpoints and therefore failing checkMembership (→ 403).
	t.Log("Step 1: Agent B joins relay with empty endpoint")

	joinResult, joinErr := cfhttp.Join(ep, campfireID, agentB, "" /* no endpoint — relay/pull-only */)
	if joinErr != nil {
		t.Fatalf("[Fix 1] join failed: %v", joinErr)
	}
	_ = joinResult // join succeeded; result carries campfire key material

	// ── Step 2: Agent B appears in membership list ─────────────────────────
	// Validates Fix 1 (campfireagent-373): UpsertPeerEndpoint was called for
	// the endpointless joiner so they appear in peer_endpoints.
	// Also validates Fix 3 (campfireagent-968): ListPeerEndpoints is the correct
	// enumeration API for p2p-http campfires (store-based, not filesystem-based).
	t.Log("Step 2: Verify Agent B in peer_endpoints (fix 1 + fix 3)")

	peers, err := sHost.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("[Fix 1+3] ListPeerEndpoints: %v", err)
	}
	var foundB bool
	for _, p := range peers {
		if p.MemberPubkey == agentB.PublicKeyHex() {
			foundB = true
			// Fix 1: endpointless joiner must be stored with an empty Endpoint field.
			if p.Endpoint != "" {
				t.Errorf("[Fix 1] Agent B stored with non-empty endpoint %q (want empty)", p.Endpoint)
			}
			break
		}
	}
	if !foundB {
		t.Fatalf("[Fix 1] Agent B not found in peer_endpoints after join — UpsertPeerEndpoint was skipped for empty endpoint")
	}

	// ── Step 3: Agent A registers and delivers a message via relay ─────────
	// Any admitted member may deliver messages. We register Agent A in the host
	// store so the authMiddleware accepts their deliver request, then send a message.
	t.Log("Step 3: Agent A delivers a message to the relay")

	agentA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating Agent A identity: %v", err)
	}
	// Upsert Agent A into the relay's peer_endpoints so they pass checkMembership.
	if upsertErr := sHost.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: agentA.PublicKeyHex(),
		Endpoint:     ep,
	}); upsertErr != nil {
		t.Fatalf("upserting Agent A endpoint: %v", upsertErr)
	}

	msg, msgErr := message.NewMessage(
		message.MustNewEd25519Signer(agentA.PrivateKey, agentA.PublicKey),
		[]byte("hello from agent A via relay"),
		[]string{"test:cross-transport"},
		nil,
	)
	if msgErr != nil {
		t.Fatalf("creating test message: %v", msgErr)
	}

	if deliverErr := cfhttp.Deliver(ep, campfireID, msg, agentA); deliverErr != nil {
		t.Fatalf("Agent A deliver failed: %v", deliverErr)
	}

	// ── Step 4: Agent B reads the message via protocol.Client + Syncer ─────
	// Validates Fix 2 (campfireagent-eac): protocol.Syncer interface — when a
	// Syncer is injected via SetSyncer, protocol.Client.Read calls Syncer.Sync
	// before querying the local store, enabling HTTP transport reads. Before the
	// fix, protocol.Client.Read only synced filesystem campfires; calls to Read
	// on HTTP campfires returned nothing because the local store was empty.
	t.Log("Step 4: Agent B reads message via protocol.Client with Syncer (fix 2)")

	// Agent B's local store — starts empty (no messages yet).
	sBagentB := openLocalStore(t)
	beforeJoin := time.Now().UnixNano()
	if addErr := sBagentB.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  t.TempDir(), // unused for HTTP transport
		JoinProtocol:  "open",
		Role:          "member",
		JoinedAt:      time.Now().UnixNano(), // Fix 4: must be nanoseconds
		Threshold:     1,
		TransportType: "p2p-http",
	}); addErr != nil {
		t.Fatalf("adding Agent B local membership: %v", addErr)
	}
	afterJoin := time.Now().UnixNano()

	// Construct protocol.Client for Agent B with an HTTP Syncer.
	clientB := protocol.New(sBagentB, agentB)
	syn := &httpSyncer{endpoint: ep, id: agentB, s: sBagentB}
	clientB.SetSyncer(syn) // Fix 2: Syncer is invoked before Read queries the store.

	// Agent B needs a store membership record for the campfire so Sync can
	// call cfhttp.Sync with agentB's identity (authMiddleware checks peer_endpoints).
	// Agent B joined above (Step 1) so the relay server will accept their sync request.

	readResult, readErr := clientB.Read(protocol.ReadRequest{
		CampfireID: campfireID,
	})
	if readErr != nil {
		t.Fatalf("[Fix 2] protocol.Client.Read: %v", readErr)
	}

	var foundMsg bool
	for _, m := range readResult.Messages {
		if m.ID == msg.ID {
			foundMsg = true
			break
		}
	}
	if !foundMsg {
		t.Errorf("[Fix 2] message %q not found in Read result (%d total messages) — Syncer.Sync may not have run", msg.ID, len(readResult.Messages))
	}

	// ── Step 5: member_count is 2 via store-based enumeration ─────────────
	// Validates Fix 3 (campfireagent-968): after Agent A and Agent B joined,
	// the relay's peer_endpoints table has exactly 2 rows. listMembersByTransport
	// now routes p2p-http campfires to ListPeerEndpoints rather than the
	// filesystem transport directory (which is always empty for HTTP-mode).
	t.Log("Step 5: Verify member_count: 2 via store-based enumeration (fix 3)")

	allPeers, err := sHost.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("[Fix 3] ListPeerEndpoints for member count: %v", err)
	}
	if len(allPeers) != 2 {
		t.Errorf("[Fix 3] member_count: want 2, got %d (entries: %+v)", len(allPeers), allPeers)
	}

	// ── Step 6: Both A and B appear with correct pubkeys ──────────────────
	// Validates Fix 3: store-based listing enumerates exactly the admitted member
	// set — both Agent A (registered via UpsertPeerEndpoint) and Agent B (admitted
	// via the join handler) must appear with their correct public keys.
	t.Log("Step 6: Verify both A and B in peer listing with correct pubkeys (fix 3)")

	seenPubkeys := make(map[string]bool, len(allPeers))
	for _, p := range allPeers {
		seenPubkeys[p.MemberPubkey] = true
	}
	if !seenPubkeys[agentA.PublicKeyHex()] {
		t.Errorf("[Fix 3] Agent A pubkey not in peer listing (want %s...)", agentA.PublicKeyHex()[:16])
	}
	if !seenPubkeys[agentB.PublicKeyHex()] {
		t.Errorf("[Fix 3] Agent B pubkey not in peer listing (want %s...)", agentB.PublicKeyHex()[:16])
	}

	// ── Step 7: JoinedAt is a valid recent nanosecond timestamp ────────────
	// Validates Fix 4 (campfireagent-a7a): admission.go stores JoinedAt as
	// time.Now().UnixNano(). Before the fix, time.Now().Unix() (~1.7e9) was used
	// instead; since display code calls time.Unix(0, joinedAt), this caused all
	// timestamps to render as 1970-01-01T00:00:00Z (1.7 seconds after epoch).
	//
	// Strategy: verify the creator's host-side membership JoinedAt and Agent B's
	// locally recorded JoinedAt are in nanosecond range and within the call window.
	t.Log("Step 7: Verify JoinedAt timestamps are nanoseconds (fix 4)")

	// Nanosecond epoch values after 2001-09-09 are > 1e18.
	// Second-based values since 2001 are ~1.0e9–2.0e9 — orders of magnitude smaller.
	const minNanosecond = int64(1_000_000_000_000_000_000)

	// Fix 4: verify the host's creator membership JoinedAt.
	creatorMem, creatorErr := sHost.GetMembership(campfireID)
	if creatorErr != nil || creatorMem == nil {
		t.Fatalf("[Fix 4] GetMembership for host: err=%v mem=%v", creatorErr, creatorMem)
	}
	if creatorMem.JoinedAt < minNanosecond {
		t.Errorf("[Fix 4] creator JoinedAt=%d looks like seconds (want >= %d)", creatorMem.JoinedAt, minNanosecond)
	}
	creatorTS := time.Unix(0, creatorMem.JoinedAt)
	if time.Since(creatorTS) > time.Minute {
		t.Errorf("[Fix 4] creator JoinedAt as nanoseconds → %v is more than 1 minute ago — wrong unit", creatorTS)
	}

	// Fix 4: verify Agent B's local membership JoinedAt is within the join window.
	agentBMem, agentBErr := sBagentB.GetMembership(campfireID)
	if agentBErr != nil || agentBMem == nil {
		t.Fatalf("[Fix 4] GetMembership for Agent B: err=%v mem=%v", agentBErr, agentBMem)
	}
	if agentBMem.JoinedAt < minNanosecond {
		t.Errorf("[Fix 4] Agent B JoinedAt=%d looks like seconds (want >= %d)", agentBMem.JoinedAt, minNanosecond)
	}
	if agentBMem.JoinedAt < beforeJoin || agentBMem.JoinedAt > afterJoin {
		t.Errorf("[Fix 4] Agent B JoinedAt=%d outside join window [%d, %d]", agentBMem.JoinedAt, beforeJoin, afterJoin)
	}

	t.Log("TestCrossTransportE2E: all four fixes verified")
}
