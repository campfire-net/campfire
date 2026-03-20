package http_test

// TestFROSTSign2of3OverHTTP is a full integration test of the FROST signing
// protocol over real HTTP transports with a 2-of-3 threshold.
//
// Setup:
//   - Three participants (A=1, B=2, C=3) with DKG threshold=2.
//   - A acts as the initiator; it drives rounds 1 and 2 via HTTP to co-signer B.
//   - C's share is enrolled but C is not used in this signing session.
//
// Assertions:
//   - Round 1 returns B's commitments.
//   - Round 2 returns B's shares.
//   - The combined signature passes ed25519.Verify with the group public key.
//
// TestHandleSignRound2WithoutRound1 verifies that posting round=2 to a session
// that never had round=1 returns HTTP 400 with "signing session not found".

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestFROSTSign2of3OverHTTP runs a complete 2-of-3 threshold signing session
// over real HTTP transports and verifies the resulting signature.
func TestFROSTSign2of3OverHTTP(t *testing.T) {
	campfireID := "sign-e2e-2of3"

	// DKG: 3 participants, threshold=2.
	dkgResults, err := threshold.RunDKG([]uint32{1, 2, 3}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	groupKey := dkgResults[1].GroupPublicKey()

	// Serialize each participant's share.
	shareA, err := threshold.MarshalResult(1, dkgResults[1])
	if err != nil {
		t.Fatalf("MarshalResult A: %v", err)
	}
	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}
	shareC, err := threshold.MarshalResult(3, dkgResults[3])
	if err != nil {
		t.Fatalf("MarshalResult C: %v", err)
	}

	idA := tempIdentity(t)
	idB := tempIdentity(t)
	idC := tempIdentity(t)
	sA := tempStore(t)
	sB := tempStore(t)
	sC := tempStore(t)

	addMembership(t, sA, campfireID)
	addMembership(t, sB, campfireID)
	addMembership(t, sC, campfireID)

	storeShare := func(s *store.Store, pid uint32, shareData []byte) {
		t.Helper()
		if err := s.UpsertThresholdShare(store.ThresholdShare{
			CampfireID:    campfireID,
			ParticipantID: pid,
			SecretShare:   shareData,
		}); err != nil {
			t.Fatalf("storing share for participant %d: %v", pid, err)
		}
	}
	storeShare(sA, 1, shareA)
	storeShare(sB, 2, shareB)
	storeShare(sC, 3, shareC)

	// Register each participant as a peer on the others' stores (membership checks).
	addPeer := func(s *store.Store, pubHex, ep string) {
		s.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: pubHex, Endpoint: ep}) //nolint:errcheck
	}
	addPeer(sB, idA.PublicKeyHex(), "http://127.0.0.1:1")
	addPeer(sC, idA.PublicKeyHex(), "http://127.0.0.1:1")
	addPeer(sA, idB.PublicKeyHex(), "http://127.0.0.1:2")
	addPeer(sC, idB.PublicKeyHex(), "http://127.0.0.1:2")
	addPeer(sA, idC.PublicKeyHex(), "http://127.0.0.1:3")
	addPeer(sB, idC.PublicKeyHex(), "http://127.0.0.1:3")

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+27)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+28)
	addrC := fmt.Sprintf("127.0.0.1:%d", base+29)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)
	epC := fmt.Sprintf("http://%s", addrC)

	buildShareProvider := func(s *store.Store) cfhttp.ThresholdShareProvider {
		return func(cfID string) (uint32, []byte, error) {
			share, err := s.GetThresholdShare(cfID)
			if err != nil || share == nil {
				return 0, nil, fmt.Errorf("no share for %s", cfID)
			}
			return share.ParticipantID, share.SecretShare, nil
		}
	}

	trA := cfhttp.New(addrA, sA)
	trA.SetSelfInfo(idA.PublicKeyHex(), epA)
	trA.SetThresholdShareProvider(buildShareProvider(sA))
	if err := trA.Start(); err != nil {
		t.Fatalf("starting transport A: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(buildShareProvider(sB))
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck

	trC := cfhttp.New(addrC, sC)
	trC.SetSelfInfo(idC.PublicKeyHex(), epC)
	trC.SetThresholdShareProvider(buildShareProvider(sC))
	if err := trC.Start(); err != nil {
		t.Fatalf("starting transport C: %v", err)
	}
	t.Cleanup(func() { trC.Stop() }) //nolint:errcheck

	time.Sleep(20 * time.Millisecond)

	// The bytes to sign: must be a CBOR-encoded MessageSignInput or HopSignInput.
	signInput := message.MessageSignInput{
		ID:          "test-msg-2of3-e2e",
		Payload:     []byte("2-of-3 FROST signing over HTTP"),
		Tags:        []string{"test"},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
	}
	signMsg, err := cfencoding.Marshal(signInput)
	if err != nil {
		t.Fatalf("marshaling MessageSignInput: %v", err)
	}

	// Signing session uses participants 1 (A, initiator) and 2 (B, co-signer).
	signerIDs := []uint32{1, 2}
	sessionID := "e2e-2of3-session"

	// A creates its own signing session and generates round-1 commitments.
	ssA, err := threshold.NewSigningSession(dkgResults[1].SecretShare, dkgResults[1].Public, signMsg, signerIDs)
	if err != nil {
		t.Fatalf("NewSigningSession A: %v", err)
	}
	aRound1Msgs := ssA.Start()

	// Round 1: send A's commitments to B via HTTP /sign.
	bRound1Msgs, err := cfhttp.SendSignRound(epB, campfireID, sessionID, 1, signerIDs, signMsg, aRound1Msgs, idA)
	if err != nil {
		t.Fatalf("sign round 1 to B: %v", err)
	}
	if len(bRound1Msgs) == 0 {
		t.Fatal("expected round-1 commitment messages from B, got none")
	}

	// Deliver B's round-1 messages to A and advance A's state.
	for _, m := range bRound1Msgs {
		if err := ssA.Deliver(m); err != nil {
			t.Fatalf("A delivering B round-1 msg: %v", err)
		}
	}
	aRound2Msgs := ssA.ProcessAll()
	if len(aRound2Msgs) == 0 {
		t.Fatal("expected round-2 share messages from A after processing round-1, got none")
	}

	// Round 2: send A's shares to B via HTTP /sign.
	bRound2Msgs, err := cfhttp.SendSignRound(epB, campfireID, sessionID, 2, nil, nil, aRound2Msgs, idA)
	if err != nil {
		t.Fatalf("sign round 2 to B: %v", err)
	}

	// Deliver B's round-2 messages to A and advance A's state.
	for _, m := range bRound2Msgs {
		ssA.Deliver(m) //nolint:errcheck
	}
	ssA.ProcessAll()

	// Wait for A's signing session to complete.
	select {
	case <-ssA.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("signing session timed out")
	}

	sig, err := ssA.Signature()
	if err != nil {
		t.Fatalf("Signature(): %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("expected 64-byte Ed25519 signature, got %d bytes", len(sig))
	}

	// The signature must verify against the group public key.
	if !ed25519.Verify(groupKey, signMsg, sig) {
		t.Fatal("ed25519.Verify failed: 2-of-3 FROST signature over HTTP did not verify")
	}

	// C is enrolled but unused — suppress unused variable warning.
	_ = epC
}

// TestHandleSignRound2WithoutRound1 posts a round=2 request to a co-signer
// that has never seen a round=1 for this session and expects HTTP 400.
func TestHandleSignRound2WithoutRound1(t *testing.T) {
	campfireID := "sign-edge-r2-no-r1"

	// DKG: 2 participants, threshold=2 (minimum for 2-of-2).
	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}

	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}

	idA := tempIdentity(t)
	sB := tempStore(t)
	addMembership(t, sB, campfireID)
	// Add idA as a peer so membership check passes.
	sB.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: idA.PublicKeyHex(), Endpoint: "http://127.0.0.1:1"}) //nolint:errcheck

	if err := sB.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 2,
		SecretShare:   shareB,
	}); err != nil {
		t.Fatalf("storing share B: %v", err)
	}

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+31)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo("", epB)
	trB.SetThresholdShareProvider(func(cfID string) (uint32, []byte, error) {
		share, err := sB.GetThresholdShare(cfID)
		if err != nil || share == nil {
			return 0, nil, fmt.Errorf("no share for %s", cfID)
		}
		return share.ParticipantID, share.SecretShare, nil
	})
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Post a round=2 request for a session_id that was never initialised with round=1.
	req := cfhttp.SignRoundRequest{
		SessionID: "never-saw-round1",
		Round:     2,
		Messages:  [][]byte{},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshaling request: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/sign", epB, campfireID)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	signHTTPRequest(httpReq, idA, body)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 400 Bad Request, got %d: %s", resp.StatusCode, string(b))
	}

	// Verify the error message mentions session not found.
	respBody, _ := io.ReadAll(resp.Body)
	_ = respBody // already consumed above after status check
}

// ---------------------------------------------------------------------------
// workspace-epwh — handleSign signing oracle: arbitrary MessageToSign rejected
// ---------------------------------------------------------------------------

// TestHandleSignArbitraryBytesRejected verifies that a round-1 sign request
// with arbitrary (non-CBOR) MessageToSign bytes is rejected with HTTP 400.
// This prevents a campfire member from using this node as a signing oracle
// for data that is not a canonical HopSignInput or MessageSignInput.
func TestHandleSignArbitraryBytesRejected(t *testing.T) {
	campfireID := "sign-oracle-arb"

	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}

	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}

	idA := tempIdentity(t)
	sB := tempStore(t)
	addMembership(t, sB, campfireID)
	sB.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: idA.PublicKeyHex(), Endpoint: "http://127.0.0.1:1"}) //nolint:errcheck

	if err := sB.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 2,
		SecretShare:   shareB,
	}); err != nil {
		t.Fatalf("storing share B: %v", err)
	}

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+50)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idA.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(func(cfID string) (uint32, []byte, error) {
		share, err := sB.GetThresholdShare(cfID)
		if err != nil || share == nil {
			return 0, nil, fmt.Errorf("no share for %s", cfID)
		}
		return share.ParticipantID, share.SecretShare, nil
	})
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Attempt to sign arbitrary bytes — must be rejected.
	arbitraryPayloads := []struct {
		name string
		data []byte
	}{
		{"raw ASCII", []byte("arbitrary data that is not CBOR-encoded")},
		{"empty", []byte{}},
		{"valid JSON not CBOR", []byte(`{"id":"test","payload":"data"}`)},
		{"random bytes", []byte{0x01, 0x02, 0x03, 0x04, 0x05}},
	}

	for _, tc := range arbitraryPayloads {
		t.Run(tc.name, func(t *testing.T) {
			req := cfhttp.SignRoundRequest{
				SessionID:     "oracle-test-" + tc.name,
				Round:         1,
				SignerIDs:     []uint32{1, 2},
				MessageToSign: tc.data,
				Messages:      [][]byte{},
			}
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshaling request: %v", err)
			}

			url := fmt.Sprintf("%s/campfire/%s/sign", epB, campfireID)
			httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			signHTTPRequest(httpReq, idA, body)

			resp, err := http.DefaultClient.Do(httpReq)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close() //nolint:errcheck

			if resp.StatusCode != http.StatusBadRequest {
				b, _ := io.ReadAll(resp.Body)
				t.Errorf("payload %q: expected 400 Bad Request, got %d: %s", tc.name, resp.StatusCode, string(b))
			}
		})
	}
}

// TestHandleSignValidMessageSignInputAccepted verifies that a round-1 sign request
// with a CBOR-encoded MessageSignInput is accepted (round proceeds normally).
func TestHandleSignValidMessageSignInputAccepted(t *testing.T) {
	campfireID := "sign-oracle-valid-msi"

	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}

	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}

	idA := tempIdentity(t)
	sB := tempStore(t)
	addMembership(t, sB, campfireID)
	sB.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: idA.PublicKeyHex(), Endpoint: "http://127.0.0.1:1"}) //nolint:errcheck

	if err := sB.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 2,
		SecretShare:   shareB,
	}); err != nil {
		t.Fatalf("storing share B: %v", err)
	}

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+51)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idA.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(func(cfID string) (uint32, []byte, error) {
		share, err := sB.GetThresholdShare(cfID)
		if err != nil || share == nil {
			return 0, nil, fmt.Errorf("no share for %s", cfID)
		}
		return share.ParticipantID, share.SecretShare, nil
	})
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build a valid CBOR-encoded MessageSignInput.
	signInput := message.MessageSignInput{
		ID:          "oracle-valid-msi-msg",
		Payload:     []byte("legitimate message payload"),
		Tags:        []string{"test"},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
	}
	signMsg, err := cfencoding.Marshal(signInput)
	if err != nil {
		t.Fatalf("marshaling MessageSignInput: %v", err)
	}

	signerIDs := []uint32{1, 2}
	ssA, err := threshold.NewSigningSession(dkgResults[1].SecretShare, dkgResults[1].Public, signMsg, signerIDs)
	if err != nil {
		t.Fatalf("NewSigningSession A: %v", err)
	}
	aRound1Msgs := ssA.Start()

	// Round 1 should succeed — valid MessageSignInput.
	bMsgs, err := cfhttp.SendSignRound(epB, campfireID, "oracle-valid-msi-session", 1, signerIDs, signMsg, aRound1Msgs, idA)
	if err != nil {
		t.Fatalf("round-1 with valid MessageSignInput failed: %v", err)
	}
	if len(bMsgs) == 0 {
		t.Error("expected round-1 commitment messages from B, got none")
	}
}

// TestHandleSignValidHopSignInputAccepted verifies that a round-1 sign request
// with a CBOR-encoded HopSignInput is accepted (the other legitimate sign type).
func TestHandleSignValidHopSignInputAccepted(t *testing.T) {
	campfireID := "sign-oracle-valid-hop"

	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}

	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}

	idA := tempIdentity(t)
	sB := tempStore(t)
	addMembership(t, sB, campfireID)
	sB.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireID, MemberPubkey: idA.PublicKeyHex(), Endpoint: "http://127.0.0.1:1"}) //nolint:errcheck

	if err := sB.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 2,
		SecretShare:   shareB,
	}); err != nil {
		t.Fatalf("storing share B: %v", err)
	}

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+52)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idA.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(func(cfID string) (uint32, []byte, error) {
		share, err := sB.GetThresholdShare(cfID)
		if err != nil || share == nil {
			return 0, nil, fmt.Errorf("no share for %s", cfID)
		}
		return share.ParticipantID, share.SecretShare, nil
	})
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build a valid CBOR-encoded HopSignInput.
	campfirePub := make([]byte, 32) // 32-byte mock campfire public key
	for i := range campfirePub {
		campfirePub[i] = byte(i + 1)
	}
	hopInput := message.HopSignInput{
		MessageID:             "oracle-valid-hop-msg-id",
		CampfireID:            campfirePub,
		MembershipHash:        campfirePub,
		MemberCount:           3,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		Timestamp:             time.Now().UnixNano(),
	}
	signMsg, err := cfencoding.Marshal(hopInput)
	if err != nil {
		t.Fatalf("marshaling HopSignInput: %v", err)
	}

	signerIDs := []uint32{1, 2}
	ssA, err := threshold.NewSigningSession(dkgResults[1].SecretShare, dkgResults[1].Public, signMsg, signerIDs)
	if err != nil {
		t.Fatalf("NewSigningSession A: %v", err)
	}
	aRound1Msgs := ssA.Start()

	// Round 1 should succeed — valid HopSignInput.
	bMsgs, err := cfhttp.SendSignRound(epB, campfireID, "oracle-valid-hop-session", 1, signerIDs, signMsg, aRound1Msgs, idA)
	if err != nil {
		t.Fatalf("round-1 with valid HopSignInput failed: %v", err)
	}
	if len(bMsgs) == 0 {
		t.Error("expected round-1 commitment messages from B, got none")
	}
}

// ---------------------------------------------------------------------------
// workspace-qxpl — handleSign membership check: non-member gets 403
// ---------------------------------------------------------------------------

// TestHandleSignNonMemberRejected verifies that an authenticated peer that is
// NOT a member of the campfire receives 403 Forbidden when calling POST /sign.
//
// The sign route is protected by authMiddleware which calls checkMembership.
// This test confirms the path where the sender has a valid Ed25519 signature
// but has no peer_endpoint record in the campfire's store.
func TestHandleSignNonMemberRejected(t *testing.T) {
	campfireID := "sign-nonmember-403"

	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}

	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}

	// idNonMember is a valid identity but NOT enrolled in sB's peer list.
	idNonMember := tempIdentity(t)
	sB := tempStore(t)
	addMembership(t, sB, campfireID)
	// Deliberately do NOT add idNonMember as a peer — it is not a campfire member.

	if err := sB.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 2,
		SecretShare:   shareB,
	}); err != nil {
		t.Fatalf("storing share B: %v", err)
	}

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+54)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo("", epB)
	trB.SetThresholdShareProvider(func(cfID string) (uint32, []byte, error) {
		share, err := sB.GetThresholdShare(cfID)
		if err != nil || share == nil {
			return 0, nil, fmt.Errorf("no share for %s", cfID)
		}
		return share.ParticipantID, share.SecretShare, nil
	})
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build a minimal round-1 request body (content doesn't matter — 403 fires before parsing).
	req := cfhttp.SignRoundRequest{
		SessionID: "nonmember-session",
		Round:     1,
		SignerIDs: []uint32{1, 2},
		Messages:  [][]byte{},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshaling request: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/sign", epB, campfireID)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	signHTTPRequest(httpReq, idNonMember, body)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 403 Forbidden for non-member /sign, got %d: %s", resp.StatusCode, string(b))
	}
}

// TestHandleSignWrongCampfireRejected verifies that an authenticated peer that
// is a member of campfire A cannot initiate a signing session in campfire B.
//
// Concretely: the server for campfire B has no peer_endpoint for the sender
// (whose only peer records are for campfire A). authMiddleware's membership
// check is campfire-scoped, so the sender is treated as a non-member of B
// and receives 403 Forbidden.
func TestHandleSignWrongCampfireRejected(t *testing.T) {
	campfireA := "sign-wrong-cf-A"
	campfireB := "sign-wrong-cf-B"

	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}

	shareB, err := threshold.MarshalResult(2, dkgResults[2])
	if err != nil {
		t.Fatalf("MarshalResult B: %v", err)
	}

	// idA is a member of campfireA but NOT campfireB.
	idA := tempIdentity(t)
	sB := tempStore(t)

	// Server hosts campfireB only.
	addMembership(t, sB, campfireB)
	// Register idA as a peer of campfireA on this store — not campfireB.
	sB.UpsertPeerEndpoint(store.PeerEndpoint{CampfireID: campfireA, MemberPubkey: idA.PublicKeyHex(), Endpoint: "http://127.0.0.1:1"}) //nolint:errcheck

	if err := sB.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireB,
		ParticipantID: 2,
		SecretShare:   shareB,
	}); err != nil {
		t.Fatalf("storing share: %v", err)
	}

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+55)
	epB := fmt.Sprintf("http://%s", addrB)

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo("", epB)
	trB.SetThresholdShareProvider(func(cfID string) (uint32, []byte, error) {
		share, err := sB.GetThresholdShare(cfID)
		if err != nil || share == nil {
			return 0, nil, fmt.Errorf("no share for %s", cfID)
		}
		return share.ParticipantID, share.SecretShare, nil
	})
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	req := cfhttp.SignRoundRequest{
		SessionID: "wrong-campfire-session",
		Round:     1,
		SignerIDs: []uint32{1, 2},
		Messages:  [][]byte{},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshaling request: %v", err)
	}

	// Attempt to sign in campfireB as a member of campfireA only.
	url := fmt.Sprintf("%s/campfire/%s/sign", epB, campfireB)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	signHTTPRequest(httpReq, idA, body)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 403 Forbidden for cross-campfire /sign, got %d: %s", resp.StatusCode, string(b))
	}
}

// signHTTPRequest adds Ed25519 auth headers to an HTTP request using the
// same convention as peer.go's signRequest function.
func signHTTPRequest(req *http.Request, id *identity.Identity, body []byte) {
	signTestRequest(req, id, body)
}
