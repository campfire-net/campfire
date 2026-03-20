package http_test

// TestRunFROSTSignEndToEnd exercises RunFROSTSign with two live HTTP transports.
//
// Setup:
//   - Two participants (A=1, B=2) with DKG threshold=2 (2-of-2).
//   - A acts as initiator and calls RunFROSTSign, pointing at B's /sign endpoint.
//   - B runs as a live HTTP transport with a threshold share provider.
//
// Assertions:
//   - RunFROSTSign returns a 64-byte Ed25519 signature without error.
//   - The signature verifies with ed25519.Verify against the group public key.
//
// This test covers the end-to-end path through RunFROSTSign's round-1/round-2
// message routing, session state management, and signature extraction — none of
// which are exercised by the existing TestFROSTSign2of3OverHTTP (which manually
// drives rounds via SendSignRound).

import (
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestRunFROSTSignEndToEnd calls RunFROSTSign with two live transports and
// verifies the returned signature passes ed25519.Verify.
func TestRunFROSTSignEndToEnd(t *testing.T) {
	campfireID := "frost-sign-e2e"

	// DKG: 2 participants, threshold=2 (both must sign).
	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	groupKey := dkgResults[1].GroupPublicKey()

	// Serialize each participant's share for storage.
	shareA, err := threshold.MarshalResult(1, dkgResults[1])
	if err != nil {
		t.Fatalf("MarshalResult A: %v", err)
	}
	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}

	idA := tempIdentity(t)
	idB := tempIdentity(t)
	sA := tempStore(t)
	sB := tempStore(t)

	addMembership(t, sA, campfireID)
	addMembership(t, sB, campfireID)

	// Store each participant's threshold share.
	if err := sA.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 1,
		SecretShare:   shareA,
	}); err != nil {
		t.Fatalf("storing share A: %v", err)
	}
	if err := sB.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 2,
		SecretShare:   shareB,
	}); err != nil {
		t.Fatalf("storing share B: %v", err)
	}

	// Register each participant as a known peer on the other's store
	// so the auth middleware's membership check passes.
	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+35)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+36)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)

	sA.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: idB.PublicKeyHex(), Endpoint: epB}) //nolint:errcheck
	sB.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: idA.PublicKeyHex(), Endpoint: epA}) //nolint:errcheck

	buildShareProvider := func(s *store.Store) cfhttp.ThresholdShareProvider {
		return func(cfID string) (uint32, []byte, error) {
			share, err := s.GetThresholdShare(cfID)
			if err != nil || share == nil {
				return 0, nil, fmt.Errorf("no share for %s", cfID)
			}
			return share.ParticipantID, share.SecretShare, nil
		}
	}

	// Start transport A (initiator).
	trA := cfhttp.New(addrA, sA)
	trA.SetSelfInfo(idA.PublicKeyHex(), epA)
	trA.SetThresholdShareProvider(buildShareProvider(sA))
	if err := trA.Start(); err != nil {
		t.Fatalf("starting transport A: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck

	// Start transport B (co-signer).
	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(buildShareProvider(sB))
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck

	time.Sleep(20 * time.Millisecond)

	// Load A's DKG result for use by RunFROSTSign.
	_, dkgResultA, err := threshold.UnmarshalResult(shareA)
	if err != nil {
		t.Fatalf("UnmarshalResult A: %v", err)
	}

	// MessageToSign must be CBOR-encoded MessageSignInput or HopSignInput.
	signInput := message.MessageSignInput{
		ID:          "test-msg-runsign-e2e",
		Payload:     []byte("RunFROSTSign end-to-end integration test"),
		Tags:        []string{"test"},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
	}
	signMsg, err := cfencoding.Marshal(signInput)
	if err != nil {
		t.Fatalf("marshaling MessageSignInput: %v", err)
	}
	sessionID := "e2e-runsign-session"

	// A calls RunFROSTSign targeting B as the sole co-signer.
	coSigners := []cfhttp.CoSigner{
		{Endpoint: epB, ParticipantID: 2},
	}

	sig, err := cfhttp.RunFROSTSign(
		dkgResultA,
		1, // A's participant ID
		signMsg,
		coSigners,
		campfireID,
		sessionID,
		idA,
	)
	if err != nil {
		t.Fatalf("RunFROSTSign: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("expected 64-byte Ed25519 signature, got %d bytes", len(sig))
	}

	// The signature must verify against the group public key.
	if !ed25519.Verify(groupKey, signMsg, sig) {
		t.Fatal("ed25519.Verify failed: RunFROSTSign signature did not verify against group public key")
	}
}
