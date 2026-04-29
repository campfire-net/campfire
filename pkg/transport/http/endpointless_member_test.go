package http_test

// Regression test for campfireagent-373:
// An agent that joins a relay campfire with cf join --via (no endpoint / empty
// JoinerEndpoint) must be recognized as a member by checkMembership. Without
// the fix, handler_join.go only calls UpsertPeerEndpoint when JoinerEndpoint is
// non-empty, so the joiner has no record in peer_endpoints and checkMembership
// returns 403 for all subsequent requests.
//
// Ports: OS-assigned (127.0.0.1:0).

import (
	"crypto/ed25519"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// setupRelayServer starts a transport that simulates a relay node: an open
// campfire with pull delivery mode only. Returns campfireID, endpoint, store.
func setupRelayServer(t *testing.T) (campfireID, ep string, sHost store.Store) {
	t.Helper()

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID = fmt.Sprintf("%x", cfPub)

	selfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating host identity: %v", err)
	}

	// Write campfire state: pull-only delivery modes (relay scenario).
	stateDir := t.TempDir()
	state := campfire.CampfireState{
		PublicKey:    cfPub,
		PrivateKey:   cfPriv,
		JoinProtocol: "open",
		Threshold:    1,
		// Pull-only: agents join with no endpoint, poll for messages.
		DeliveryModes: []string{campfire.DeliveryModePull},
	}
	stateData, encErr := cfencoding.Marshal(state)
	if encErr != nil {
		t.Fatalf("encoding campfire state: %v", encErr)
	}
	if writeErr := os.WriteFile(filepath.Join(stateDir, "campfire.cbor"), stateData, 0600); writeErr != nil {
		t.Fatalf("writing campfire state: %v", writeErr)
	}

	sHost = tempStore(t)
	if addErr := sHost.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: stateDir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); addErr != nil {
		t.Fatalf("adding membership: %v", addErr)
	}

	tr := cfhttp.New("127.0.0.1:0", sHost)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	ep = epOf(tr)
	tr.SetSelfInfo(selfID.PublicKeyHex(), ep)
	time.Sleep(20 * time.Millisecond)

	return campfireID, ep, sHost
}

// TestEndpointlessMemberAccepted verifies that an agent joining with an empty
// endpoint (relay/pull-only mode) is admitted and can subsequently perform all
// member operations: sync, deliver, poll, and appear in the peer list.
//
// This is the integration regression test for campfireagent-373. Before the fix,
// the joiner's pubkey was never written to peer_endpoints, so checkMembership
// returned 403 on every subsequent request.
func TestEndpointlessMemberAccepted(t *testing.T) {
	campfireID, ep, sHost := setupRelayServer(t)

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	// Step 1: Join with empty endpoint (simulating cf join --via with no callback URL).
	joinReq := buildJoinRequest(t, ep, campfireID, joiner, "")
	joinResp, err := http.DefaultClient.Do(joinReq)
	if err != nil {
		t.Fatalf("join request failed: %v", err)
	}
	joinResp.Body.Close()
	if joinResp.StatusCode != http.StatusOK {
		t.Fatalf("join expected 200, got %d", joinResp.StatusCode)
	}

	// Step 2: Verify the joiner appears in the membership list (peer_endpoints).
	peers, err := sHost.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("listing peers: %v", err)
	}
	found := false
	for _, p := range peers {
		if p.MemberPubkey == joiner.PublicKeyHex() {
			found = true
			// Endpoint should be empty (no address — pull-only).
			if p.Endpoint != "" {
				t.Errorf("endpointless joiner stored with non-empty endpoint: %q", p.Endpoint)
			}
			break
		}
	}
	if !found {
		t.Error("endpointless joiner not found in peer_endpoints after join — bug: UpsertPeerEndpoint skipped for empty endpoint")
	}

	// Step 3: Verify the joiner can sync for messages (200, not 403).
	// This exercises checkMembership via authMiddleware on GET /campfire/{id}/sync.
	_, syncErr := cfhttp.Sync(ep, campfireID, 0, joiner)
	if syncErr != nil {
		t.Errorf("sync failed for endpointless member (expected success, got: %v)", syncErr)
	}

	// Step 4: Verify the joiner can deliver messages (200, not 403).
	// This exercises checkMembership via authMiddleware on POST /campfire/{id}/deliver.
	msg := newTestMessage(t, joiner)
	if deliverErr := cfhttp.Deliver(ep, campfireID, msg, joiner); deliverErr != nil {
		t.Errorf("deliver failed for endpointless member (expected success, got: %v)", deliverErr)
	}

	// Step 5: Verify the joiner can poll for messages (200, not 403).
	// This exercises checkMembership via authMiddleware on GET /campfire/{id}/poll.
	pollResp, pollErr := doPoll(ep, campfireID, 0, 0, joiner)
	if pollErr != nil {
		t.Fatalf("poll request failed: %v", pollErr)
	}
	pollResp.Body.Close()
	if pollResp.StatusCode != http.StatusOK {
		t.Errorf("poll expected 200 for endpointless member, got %d", pollResp.StatusCode)
	}
}

// TestEndpointlessMemberNotAdmittedForFakeKey verifies that the fix does not
// weaken the security model: an unauthenticated entity cannot appear as a member.
// The join handler requires a valid Ed25519 signature before calling UpsertPeerEndpoint.
// A request with a mismatched signature is rejected before any peer record is written.
func TestEndpointlessMemberNotAdmittedForFakeKey(t *testing.T) {
	campfireID, ep, sHost := setupRelayServer(t)

	legitimate, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating legitimate identity: %v", err)
	}
	attacker, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating attacker identity: %v", err)
	}

	// Build a join request claiming legitimate's pubkey but signed by attacker.
	// buildJoinRequest signs with signerID's key and sets JoinerPubkey = signerID.
	// To simulate the attack, we build a request for legitimate but then override
	// the signature to be from attacker (invalid signature → 401 before admission).
	joinReq := buildJoinRequest(t, ep, campfireID, attacker, "")
	// Override the sender header to claim we are the legitimate user.
	joinReq.Header.Set("X-Campfire-Sender", legitimate.PublicKeyHex())

	joinResp, err := http.DefaultClient.Do(joinReq)
	if err != nil {
		t.Fatalf("join request failed: %v", err)
	}
	joinResp.Body.Close()

	// Must be rejected before admission (401 Unauthorized — signature mismatch).
	if joinResp.StatusCode == http.StatusOK {
		t.Fatal("attacker was incorrectly admitted: signature-mismatch join must not succeed")
	}

	// The legitimate user must NOT appear in peer_endpoints.
	peers, err := sHost.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("listing peers: %v", err)
	}
	for _, p := range peers {
		if p.MemberPubkey == legitimate.PublicKeyHex() {
			t.Errorf("legitimate user appeared in peer_endpoints via attacker's spoofed join: security violation")
		}
	}
}
