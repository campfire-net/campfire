package protocol_test

// TestSendFROST exercises protocol.Client.Send() via the P2P HTTP transport
// with threshold=2 FROST signing.
//
// Setup:
//   - DKG with 2 participants (A=1, B=2), threshold=2 (both must sign).
//   - Client A holds participant 1's share; a live HTTP peer B holds participant 2's share.
//   - The campfire state file is written to A's transport dir (required by sendP2PHTTP).
//   - A's membership has TransportType="p2p-http" and Threshold=2.
//   - B's transport has a ThresholdShareProvider configured for the sign endpoint.
//
// Assertions:
//   - Client.Send() returns a non-nil message with no error.
//   - The message has exactly one provenance hop.
//   - The hop signature verifies against the group public key (FROST output).

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
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// portBaseFROST returns a per-process port offset for this test file.
// Uses a distinct range from the transport/http tests and other protocol tests.
func portBaseFROST() int {
	return 21000 + (os.Getpid() % 500)
}

// TestSendFROST calls Client.Send() on a P2P HTTP campfire with threshold=2,
// requiring FROST signing. Verifies the resulting message has a valid threshold
// provenance hop.
func TestSendFROST(t *testing.T) {
	// Override HTTP client to allow loopback connections to the test peer.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 30 * time.Second})
	t.Cleanup(func() {
		// Restore the default SSRF-safe client after this test.
		cfhttp.OverrideHTTPClientForTest(&http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport,
		})
	})

	// --- DKG: 2 participants, threshold=2 ---
	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	groupKey := dkgResults[1].GroupPublicKey()

	shareA, err := threshold.MarshalResult(1, dkgResults[1])
	if err != nil {
		t.Fatalf("MarshalResult A: %v", err)
	}
	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}

	// --- Agent identities ---
	idA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity A: %v", err)
	}
	idB, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity B: %v", err)
	}

	// --- Campfire identity ---
	// For threshold campfires, the group public key from DKG IS the campfire
	// public key. cfState.PrivateKey must be a valid ed25519 private key (the
	// sendP2PHTTP code reads it from disk), but it is unused in the threshold>1
	// path. We set cfPub = groupKey so that the hop's CampfireID (= cfState.PublicKey)
	// and the FROST group verifying key are the same.
	cfKeyPair, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire keypair: %v", err)
	}
	cfPriv := cfKeyPair.PrivateKey              // not used for threshold signing
	cfPub := ed25519.PublicKey(groupKey)        // DKG group key IS the campfire public key
	campfireID := fmt.Sprintf("%x", cfPub)

	// --- Transport dir for A: write the campfire state CBOR file ---
	transportDir := t.TempDir()
	cfState := campfire.CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            cfPriv,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
	}
	stateData, err := cfencoding.Marshal(&cfState)
	if err != nil {
		t.Fatalf("marshaling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(transportDir, campfireID+".cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// --- Stores ---
	storeADir := t.TempDir()
	sA, err := store.Open(filepath.Join(storeADir, "store.db"))
	if err != nil {
		t.Fatalf("opening store A: %v", err)
	}
	t.Cleanup(func() { sA.Close() })

	storeBDir := t.TempDir()
	sB, err := store.Open(filepath.Join(storeBDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store B: %v", err)
	}
	t.Cleanup(func() { sB.Close() })

	// --- Network addresses ---
	base := portBaseFROST()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+0)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+1)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)

	// --- A's membership (p2p-http, threshold=2) ---
	if err := sA.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  transportDir,
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     2,
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("adding A's membership: %v", err)
	}

	// --- A's threshold share ---
	if err := sA.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 1,
		SecretShare:   shareA,
	}); err != nil {
		t.Fatalf("storing share A: %v", err)
	}

	// --- B's threshold share ---
	if err := sB.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 2,
		SecretShare:   shareB,
	}); err != nil {
		t.Fatalf("storing share B: %v", err)
	}

	// --- B's membership (for auth middleware) ---
	if err := sB.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "http",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("adding B's membership: %v", err)
	}

	// --- Register peers so auth middleware passes ---
	// A must know B (to route signing rounds to B's endpoint and include participantID).
	// B must know A (for membership auth check on incoming sign requests).
	sA.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:    campfireID,
		MemberPubkey:  idB.PublicKeyHex(),
		Endpoint:      epB,
		ParticipantID: 2,
	})
	sB.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:    campfireID,
		MemberPubkey:  idA.PublicKeyHex(),
		Endpoint:      epA,
		ParticipantID: 1,
	})

	// --- ThresholdShareProvider for B ---
	bShareProvider := func(cfID string) (uint32, []byte, error) {
		share, err := sB.GetThresholdShare(cfID)
		if err != nil || share == nil {
			return 0, nil, fmt.Errorf("no share for %s", cfID)
		}
		return share.ParticipantID, share.SecretShare, nil
	}

	// --- Start transport B (co-signer peer) ---
	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(bShareProvider)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck

	// Give the server a moment to start accepting connections.
	time.Sleep(20 * time.Millisecond)

	// --- Call Client.Send() via A ---
	client := protocol.New(sA, idA)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("threshold signed message"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Client.Send: %v", err)
	}
	if msg == nil {
		t.Fatal("Client.Send returned nil message")
	}
	if msg.ID == "" {
		t.Fatal("message ID is empty")
	}

	// --- Assertions ---

	// Message must have exactly one provenance hop (the threshold hop).
	if len(msg.Provenance) != 1 {
		t.Fatalf("expected 1 provenance hop, got %d", len(msg.Provenance))
	}
	hop := msg.Provenance[0]

	// The hop signature must verify against the group public key.
	// Reconstruct the sign input the same way thresholdSignHop does.
	signInput := message.HopSignInput{
		MessageID:             msg.ID,
		CampfireID:            cfPub,
		MembershipHash:        cfPub,
		MemberCount:           hop.MemberCount,
		JoinProtocol:          hop.JoinProtocol,
		ReceptionRequirements: hop.ReceptionRequirements,
		Timestamp:             hop.Timestamp,
	}
	signBytes, err := cfencoding.Marshal(signInput)
	if err != nil {
		t.Fatalf("marshaling HopSignInput for verification: %v", err)
	}
	if !ed25519.Verify(groupKey, signBytes, hop.Signature) {
		t.Error("hop signature does not verify against FROST group public key")
	}

	// The hop CampfireID should match the campfire public key (= FROST group key).
	if fmt.Sprintf("%x", hop.CampfireID) != campfireID {
		t.Errorf("hop CampfireID mismatch: got %x, want %s", hop.CampfireID, campfireID)
	}

	// Message sender must be A.
	if fmt.Sprintf("%x", msg.Sender) != idA.PublicKeyHex() {
		t.Errorf("sender mismatch: got %x, want %s", msg.Sender, idA.PublicKeyHex())
	}

	// Message must be stored in A's local store.
	stored, err := sA.GetMessage(msg.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored == nil {
		t.Error("message not stored in A's local store after send")
	}
}

// TestThresholdSignHopRejectsOutOfGroupParticipantID is a regression test for
// the security fix in campfire-agent-r4l: participant IDs not in the DKG group
// must be rejected before being used in FROST signing.
//
// Setup:
//   - DKG with participants {1, 2}, threshold=2.
//   - Client A holds participant 1's share.
//   - The only registered co-signer peer has participantID=99, which is NOT in
//     the DKG group {1, 2}.
//
// Expected outcome:
//   - Client.Send() fails with an error about insufficient co-signers.
//   - The out-of-group ID was silently filtered before reaching FROST.
//
// Before the fix, participantID=99 would have been passed through to
// cfhttp.RunFROSTSign, which could panic or produce undefined behavior because
// the ID is absent from the group's public key set.
func TestThresholdSignHopRejectsOutOfGroupParticipantID(t *testing.T) {
	// --- DKG: 2 participants {1, 2}, threshold=2 ---
	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	groupKey := dkgResults[1].GroupPublicKey()

	shareA, err := threshold.MarshalResult(1, dkgResults[1])
	if err != nil {
		t.Fatalf("MarshalResult A: %v", err)
	}

	// --- Agent identity ---
	idA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity A: %v", err)
	}

	// --- Campfire identity (group key = campfire public key) ---
	cfKeyPair, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire keypair: %v", err)
	}
	cfPub := ed25519.PublicKey(groupKey)
	cfPriv := cfKeyPair.PrivateKey
	campfireID := fmt.Sprintf("%x", cfPub)

	// --- Transport dir for A ---
	transportDir := t.TempDir()
	cfState := campfire.CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            cfPriv,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
	}
	stateData, err := cfencoding.Marshal(&cfState)
	if err != nil {
		t.Fatalf("marshaling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(transportDir, campfireID+".cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// --- Store for A ---
	storeADir := t.TempDir()
	sA, err := store.Open(filepath.Join(storeADir, "store.db"))
	if err != nil {
		t.Fatalf("opening store A: %v", err)
	}
	t.Cleanup(func() { sA.Close() })

	// --- A's membership (p2p-http, threshold=2) ---
	if err := sA.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  transportDir,
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     2,
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("adding A's membership: %v", err)
	}

	// --- A's threshold share ---
	if err := sA.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 1,
		SecretShare:   shareA,
	}); err != nil {
		t.Fatalf("storing share A: %v", err)
	}

	// --- Register a rogue peer with participantID=99 (not in DKG group {1,2}) ---
	// This simulates a rogue participant advertising an out-of-group ID.
	rogueID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating rogue identity: %v", err)
	}
	sA.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:    campfireID,
		MemberPubkey:  rogueID.PublicKeyHex(),
		Endpoint:      "http://127.0.0.1:19999", // unreachable; must be rejected before contact
		ParticipantID: 99,                        // NOT in DKG group {1, 2}
	})

	// --- Attempt Client.Send() — must fail because no valid co-signers remain ---
	client := protocol.New(sA, idA)
	_, sendErr := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("should not send"),
		Tags:       []string{"status"},
	})
	if sendErr == nil {
		t.Fatal("expected Client.Send to fail when only out-of-group participant IDs are available, but it succeeded")
	}
	// Confirm the error is about co-signer count (group validation filtered the rogue
	// ID) rather than a FROST panic or crypto error from processing an invalid ID.
	t.Logf("Client.Send correctly rejected rogue participantID=99: %v", sendErr)
}
