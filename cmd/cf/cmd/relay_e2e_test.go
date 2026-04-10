package cmd

// relay_e2e_test.go — Full relay round-trip integration test.
//
// Done conditions (campfireagent-4d2):
//   - Agent A creates campfire on in-process relay
//   - Agent A sends a message with tag "request"
//   - Agent B joins via the relay, reads the request message
//   - Agent B sends a response message
//   - Agent A reads the response message
//   - Verify cf ls and members work for both agents

import (
	"crypto/ed25519"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestRelayE2E exercises the full relay round-trip:
//
//  1. Spin up an in-process relay (httptest.Server with Transport handler) via
//     the shared newTestRelay helper defined in create_relay_test.go.
//  2. Agent A: creates campfire on relay (registerOnRelay).
//  3. Agent A: sends a "request" message via cfhttp.Deliver to relay endpoint.
//  4. Agent B: joins via the relay (cfhttp.Join), gets campfire private key.
//  5. Agent B: reads A's "request" message via cfhttp.Sync.
//  6. Agent B: sends a "response" message via cfhttp.Deliver.
//  7. Agent A: reads B's "response" message via cfhttp.Sync.
//  8. Verify both agents see each other in members (via store peer_endpoints).
//  9. Verify cf ls shows the p2p-http campfire in A's store.
func TestRelayE2E(t *testing.T) {
	// Spin up the in-process relay.
	relay := newTestRelay(t)

	// Override HTTP client so loopback connections work in tests.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)
	defer func() {
		cfhttp.OverrideHTTPClientForTest(&http.Client{Transport: http.DefaultTransport})
		cfhttp.OverridePollTransportForTest(http.DefaultTransport)
	}()

	// ── Agent A setup ──────────────────────────────────────────────────────────

	agentA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent A identity: %v", err)
	}
	storeA, err := store.Open(filepath.Join(t.TempDir(), "agentA.db"))
	if err != nil {
		t.Fatalf("opening store A: %v", err)
	}
	t.Cleanup(func() { storeA.Close() })

	// Create campfire and register on relay.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	_, relayEndpoint, _, err := registerOnRelay(cf, agentA, storeA, t.TempDir(), relay.URL, "e2e relay test")
	if err != nil {
		t.Fatalf("registerOnRelay: %v", err)
	}
	campfireID := cf.PublicKeyHex()

	if relayEndpoint == "" {
		t.Fatal("expected non-empty relay endpoint from registerOnRelay")
	}

	// ── Step 3: Agent A sends "request" message ────────────────────────────────

	requestMsg, err := message.NewMessage(
		message.MustNewEd25519Signer(agentA.PrivateKey, agentA.PublicKey),
		[]byte("ping from agent A"),
		[]string{"request"},
		nil,
	)
	if err != nil {
		t.Fatalf("creating request message: %v", err)
	}

	// Add a provenance hop signed by the campfire private key.
	cfPub := ed25519.PublicKey(cf.PublicKey)
	cfPriv := ed25519.PrivateKey(cf.PrivateKey)
	if err := requestMsg.AddHop(cfPriv, cfPub, cfPub, 1, "open", []string{}, campfire.RoleFull); err != nil {
		t.Fatalf("adding hop to request: %v", err)
	}

	// Deliver to relay endpoint.
	if err := cfhttp.Deliver(relayEndpoint, campfireID, requestMsg, agentA); err != nil {
		t.Fatalf("Agent A deliver request: %v", err)
	}

	// ── Agent B setup ──────────────────────────────────────────────────────────

	agentB, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent B identity: %v", err)
	}
	storeB, err := store.Open(filepath.Join(t.TempDir(), "agentB.db"))
	if err != nil {
		t.Fatalf("opening store B: %v", err)
	}
	t.Cleanup(func() { storeB.Close() })

	// ── Step 4: Agent B joins via relay ────────────────────────────────────────

	joinResult, err := cfhttp.Join(relayEndpoint, campfireID, agentB, "")
	if err != nil {
		t.Fatalf("Agent B join via relay: %v", err)
	}

	if len(joinResult.CampfirePrivKey) == 0 {
		t.Fatal("Agent B did not receive campfire private key in join result")
	}

	// Derive campfire keys from join result.
	bCfPriv := ed25519.PrivateKey(joinResult.CampfirePrivKey)
	bCfPub := ed25519.PublicKey(joinResult.CampfirePrivKey[32:])

	// Record membership in B's store.
	if err := storeB.AddMembership(store.Membership{
		CampfireID:      campfireID,
		TransportDir:    "",
		TransportType:   "p2p-http",
		JoinProtocol:    "open",
		Role:            "member",
		JoinedAt:        store.NowNano(),
		Threshold:       1,
		CampfirePrivKey: campfireID, // store pub hex as reference
	}); err != nil {
		t.Fatalf("Agent B AddMembership: %v", err)
	}
	storeB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: agentA.PublicKeyHex(),
		Endpoint:     relayEndpoint,
	})

	// ── Step 5: Agent B reads Agent A's "request" message ─────────────────────

	msgsForB, err := cfhttp.Sync(relayEndpoint, campfireID, 0, agentB)
	if err != nil {
		t.Fatalf("Agent B sync: %v", err)
	}

	// Find the request message.
	var foundRequest bool
	for _, m := range msgsForB {
		for _, tag := range m.Tags {
			if tag == "request" {
				foundRequest = true
				break
			}
		}
	}
	if !foundRequest {
		t.Errorf("Agent B did not find a message with tag 'request'; got %d messages", len(msgsForB))
	}

	// ── Step 6: Agent B sends "response" message ───────────────────────────────

	responseMsg, err := message.NewMessage(
		message.MustNewEd25519Signer(agentB.PrivateKey, agentB.PublicKey),
		[]byte("pong from agent B"),
		[]string{"response"},
		nil,
	)
	if err != nil {
		t.Fatalf("creating response message: %v", err)
	}

	if err := responseMsg.AddHop(bCfPriv, bCfPub, bCfPub, 1, "open", []string{}, campfire.RoleFull); err != nil {
		t.Fatalf("adding hop to response: %v", err)
	}

	if err := cfhttp.Deliver(relayEndpoint, campfireID, responseMsg, agentB); err != nil {
		t.Fatalf("Agent B deliver response: %v", err)
	}

	// ── Step 7: Agent A reads Agent B's "response" message ────────────────────

	// Agent A re-syncs from after the request message timestamp.
	msgsForA, err := cfhttp.Sync(relayEndpoint, campfireID, requestMsg.Timestamp+1, agentA)
	if err != nil {
		t.Fatalf("Agent A sync for response: %v", err)
	}

	var foundResponse bool
	for _, m := range msgsForA {
		for _, tag := range m.Tags {
			if tag == "response" {
				foundResponse = true
				break
			}
		}
	}
	if !foundResponse {
		t.Errorf("Agent A did not find a message with tag 'response'; got %d messages", len(msgsForA))
	}

	// ── Step 8: Verify members (cf members equivalent) ────────────────────────

	// Agent A's store should have the relay as a peer endpoint (recorded by
	// registerOnRelay). Verify via listMembersFromStore (the p2p-http members helper).
	membersA, err := listMembersFromStore(campfireID, storeA)
	if err != nil {
		t.Fatalf("listMembersFromStore (A): %v", err)
	}
	// At minimum, the relay/campfire pubkey should appear.
	if len(membersA) == 0 {
		t.Error("Agent A members list is empty; expected at least one peer (relay)")
	}

	// Agent B's store has agent A's endpoint as a peer.
	membersB, err := listMembersFromStore(campfireID, storeB)
	if err != nil {
		t.Fatalf("listMembersFromStore (B): %v", err)
	}
	var foundAInB bool
	for _, m := range membersB {
		if len(m.PublicKey) > 0 && agentA.PublicKeyHex() == ed25519PubHex(m.PublicKey) {
			foundAInB = true
			break
		}
	}
	if !foundAInB {
		t.Errorf("Agent A not found in Agent B's members list; got %d members", len(membersB))
	}

	// ── Step 9: Verify cf ls equivalent (ListMemberships) ─────────────────────

	memberships, err := storeA.ListMemberships()
	if err != nil {
		t.Fatalf("ListMemberships (A): %v", err)
	}

	var foundCampfire bool
	for _, ms := range memberships {
		if ms.CampfireID == campfireID && ms.TransportType == "p2p-http" {
			foundCampfire = true
			break
		}
	}
	if !foundCampfire {
		t.Errorf("campfire %s not found with p2p-http in Agent A's memberships", campfireID[:12])
	}
}

// ed25519PubHex returns the hex-encoded Ed25519 public key bytes.
func ed25519PubHex(pub ed25519.PublicKey) string {
	return fmt.Sprintf("%x", []byte(pub))
}
