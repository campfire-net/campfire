package http_test

// TestP2PSendReadThreshold1 verifies:
// - Two agents over P2P HTTP (A sends, B reads) with threshold=1.
// - The provenance hop is signed with the campfire private key.
// - ed25519.Verify confirms the hop signature.
// - Filesystem transport is unaffected (existing tests cover it).

import (
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	cfencoding "github.com/3dl-dev/campfire/pkg/encoding"
	"github.com/3dl-dev/campfire/pkg/message"
	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/threshold"
	cfhttp "github.com/3dl-dev/campfire/pkg/transport/http"
)

// TestSendReadP2PThreshold1 tests the basic send→deliver→sync flow:
// - Agent A sends a message (delivered via HTTP to B).
// - Agent B syncs from A and verifies the provenance hop signature.
func TestSendReadP2PThreshold1(t *testing.T) {
	campfireID := "p2p-send-read-t1"

	// Generate campfire keypair (threshold=1).
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	idA := tempIdentity(t)
	idB := tempIdentity(t)
	sA := tempStore(t)
	sB := tempStore(t)
	addMembership(t, sA, campfireID)
	addMembership(t, sB, campfireID)

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+20)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+21)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)

	trA := cfhttp.New(addrA, sA)
	trA.SetSelfInfo(idA.PublicKeyHex(), epA)
	trA.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	})
	if err := trA.Start(); err != nil {
		t.Fatalf("starting transport A: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build a message with a signed provenance hop (threshold=1).
	msg, err := message.NewMessage(idA.PrivateKey, idA.PublicKey, []byte("hello P2P"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	if err := msg.AddHop(
		ed25519.PrivateKey(cfPriv),
		ed25519.PublicKey(cfPub),
		cfPub, // membership hash = pub key for simplicity
		2,
		"open",
		[]string{},
	); err != nil {
		t.Fatalf("adding hop: %v", err)
	}

	// A delivers to B.
	if err := cfhttp.Deliver(epB, campfireID, msg, idA); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	// B syncs from A (via B's store — actually querying B's local store which has the delivered msg).
	msgs, err := cfhttp.Sync(epB, campfireID, 0, idB)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	got := msgs[0]

	// Verify message signature.
	if !got.VerifySignature() {
		t.Error("message signature invalid")
	}

	// Verify provenance hop signature.
	if len(got.Provenance) != 1 {
		t.Fatalf("expected 1 provenance hop, got %d", len(got.Provenance))
	}
	if !message.VerifyHop(got.ID, got.Provenance[0]) {
		t.Error("provenance hop signature invalid")
	}

	// Double-check with ed25519.Verify directly.
	hop := got.Provenance[0]
	hopSignInput := message.HopSignInput{
		MessageID:             got.ID,
		CampfireID:            hop.CampfireID,
		MembershipHash:        hop.MembershipHash,
		MemberCount:           hop.MemberCount,
		JoinProtocol:          hop.JoinProtocol,
		ReceptionRequirements: hop.ReceptionRequirements,
		Timestamp:             hop.Timestamp,
	}
	signBytes, err := cfencoding.Marshal(hopSignInput)
	if err != nil {
		t.Fatalf("marshaling hop sign input: %v", err)
	}
	if !ed25519.Verify(cfPub, signBytes, hop.Signature) {
		t.Error("ed25519.Verify failed for provenance hop")
	}
}

// TestSendReadP2PThreshold2 verifies the threshold=2 FROST signing flow:
// - Three agents (A, B, C) with a shared DKG run (threshold=2 of 3).
// - A sends a message: initiates FROST rounds with B as co-signer.
// - The provenance hop is signed by FROST: ed25519.Verify confirms.
// - B and C both receive the message via HTTP deliver.
func TestSendReadP2PThreshold2(t *testing.T) {
	campfireID := "p2p-send-read-t2"

	// Run DKG for 3 participants with threshold 2.
	dkgResults, err := threshold.RunDKG([]uint32{1, 2, 3}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	groupKey := dkgResults[1].GroupPublicKey()

	// Serialize DKG results for storage.
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

	// Store DKG shares in each agent's store.
	storeShare := func(s *store.Store, id string, pid uint32, shareData []byte) {
		t.Helper()
		err := s.UpsertThresholdShare(store.ThresholdShare{
			CampfireID:    id,
			ParticipantID: pid,
			SecretShare:   shareData,
		})
		if err != nil {
			t.Fatalf("storing share for participant %d: %v", pid, err)
		}
	}
	storeShare(sA, campfireID, 1, shareA)
	storeShare(sB, campfireID, 2, shareB)
	storeShare(sC, campfireID, 3, shareC)

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+22)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+23)
	addrC := fmt.Sprintf("127.0.0.1:%d", base+24)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)
	epC := fmt.Sprintf("http://%s", addrC)

	buildShareProvider := func(s *store.Store) cfhttp.ThresholdShareProvider {
		return func(cfID string) (uint32, []byte, error) {
			share, err := s.GetThresholdShare(cfID)
			if err != nil {
				return 0, nil, err
			}
			if share == nil {
				return 0, nil, fmt.Errorf("no share for campfire %s", cfID)
			}
			return share.ParticipantID, share.SecretShare, nil
		}
	}

	// Start transports.
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

	// Build the hop sign input and run FROST signing in-process for the test.
	// In production this would go through the HTTP /sign endpoint.
	// Here we verify the signature is correct when produced by threshold.Sign.
	msgPayload := []byte("threshold signing test")
	msg, err := message.NewMessage(idA.PrivateKey, idA.PublicKey, msgPayload, []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	hopTimestamp := time.Now().UnixNano()
	hopSignInput := message.HopSignInput{
		MessageID:             msg.ID,
		CampfireID:            groupKey,
		MembershipHash:        groupKey,
		MemberCount:           3,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		Timestamp:             hopTimestamp,
	}
	signBytes, err := cfencoding.Marshal(hopSignInput)
	if err != nil {
		t.Fatalf("marshaling hop sign input: %v", err)
	}

	// Sign with participants 1 and 2 (A and B).
	sig, err := threshold.Sign(dkgResults, []uint32{1, 2}, signBytes)
	if err != nil {
		t.Fatalf("threshold.Sign: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("expected 64-byte signature, got %d", len(sig))
	}

	// Verify with group public key.
	if !ed25519.Verify(groupKey, signBytes, sig) {
		t.Fatal("ed25519.Verify failed for FROST threshold signature")
	}

	// Attach the signed hop.
	hop := message.ProvenanceHop{
		CampfireID:            groupKey,
		MembershipHash:        groupKey,
		MemberCount:           3,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		Timestamp:             hopTimestamp,
		Signature:             sig,
	}
	msg.Provenance = append(msg.Provenance, hop)

	// Deliver to B and C.
	if err := cfhttp.Deliver(epB, campfireID, msg, idA); err != nil {
		t.Fatalf("deliver to B: %v", err)
	}
	if err := cfhttp.Deliver(epC, campfireID, msg, idA); err != nil {
		t.Fatalf("deliver to C: %v", err)
	}

	// B and C sync and verify.
	for name, ep := range map[string]string{"B": epB, "C": epC} {
		msgs, err := cfhttp.Sync(ep, campfireID, 0, idA)
		if err != nil {
			t.Errorf("sync from %s: %v", name, err)
			continue
		}
		if len(msgs) != 1 {
			t.Errorf("%s: expected 1 message, got %d", name, len(msgs))
			continue
		}
		got := msgs[0]
		if len(got.Provenance) != 1 {
			t.Errorf("%s: expected 1 provenance hop, got %d", name, len(got.Provenance))
			continue
		}

		// Verify provenance hop via ed25519.Verify directly.
		gotHop := got.Provenance[0]
		gotSignInput := message.HopSignInput{
			MessageID:             got.ID,
			CampfireID:            gotHop.CampfireID,
			MembershipHash:        gotHop.MembershipHash,
			MemberCount:           gotHop.MemberCount,
			JoinProtocol:          gotHop.JoinProtocol,
			ReceptionRequirements: gotHop.ReceptionRequirements,
			Timestamp:             gotHop.Timestamp,
		}
		gotSignBytes, err := cfencoding.Marshal(gotSignInput)
		if err != nil {
			t.Errorf("%s: marshaling hop sign input: %v", name, err)
			continue
		}
		if !ed25519.Verify(groupKey, gotSignBytes, gotHop.Signature) {
			t.Errorf("%s: ed25519.Verify failed for FROST threshold hop signature", name)
		}
	}
}

// TestSignEndpointRoundTrip verifies that the /sign HTTP endpoint correctly
// routes FROST signing round messages for threshold=2 signing.
// Initiator: participant 1 (A). Co-signer: participant 2 (B).
func TestSignEndpointRoundTrip(t *testing.T) {
	campfireID := "p2p-sign-roundtrip"

	// Run DKG for 2 participants with threshold 2.
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

	idA := tempIdentity(t)
	idB := tempIdentity(t)
	sA := tempStore(t)
	sB := tempStore(t)
	addMembership(t, sA, campfireID)
	addMembership(t, sB, campfireID)

	// Store DKG shares.
	sA.UpsertThresholdShare(store.ThresholdShare{CampfireID: campfireID, ParticipantID: 1, SecretShare: shareA}) //nolint:errcheck
	sB.UpsertThresholdShare(store.ThresholdShare{CampfireID: campfireID, ParticipantID: 2, SecretShare: shareB}) //nolint:errcheck

	base := portBase()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+25)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+26)
	epA := fmt.Sprintf("http://%s", addrA)
	epB := fmt.Sprintf("http://%s", addrB)

	buildShareProvider := func(s *store.Store) cfhttp.ThresholdShareProvider {
		return func(cfID string) (uint32, []byte, error) {
			share, err := s.GetThresholdShare(cfID)
			if err != nil || share == nil {
				return 0, nil, fmt.Errorf("no share")
			}
			return share.ParticipantID, share.SecretShare, nil
		}
	}

	trA := cfhttp.New(addrA, sA)
	trA.SetSelfInfo(idA.PublicKeyHex(), epA)
	trA.SetThresholdShareProvider(buildShareProvider(sA))
	if err := trA.Start(); err != nil {
		t.Fatalf("starting A: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idB.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(buildShareProvider(sB))
	if err := trB.Start(); err != nil {
		t.Fatalf("starting B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// The message to threshold-sign.
	signMsg := []byte("test message for threshold signing over HTTP")

	signerIDs := []uint32{1, 2}
	sessionID := "test-session-roundtrip"

	// Participant A (initiator) creates its signing session.
	ssA, err := threshold.NewSigningSession(dkgResults[1].SecretShare, dkgResults[1].Public, signMsg, signerIDs)
	if err != nil {
		t.Fatalf("NewSigningSession A: %v", err)
	}
	aRound1Msgs := ssA.Start()

	// Round 1: send A's commitments to B's /sign endpoint.
	bRound1Msgs, err := cfhttp.SendSignRound(epB, campfireID, sessionID, 1, signerIDs, signMsg, aRound1Msgs, idA)
	if err != nil {
		t.Fatalf("sign round 1: %v", err)
	}
	if len(bRound1Msgs) == 0 {
		t.Fatal("expected round-1 commitments from B, got none")
	}

	// Deliver B's round-1 to A.
	for _, m := range bRound1Msgs {
		ssA.Deliver(m) //nolint:errcheck
	}
	aRound2Msgs := ssA.ProcessAll()

	// Round 2: send A's shares to B.
	bRound2Msgs, err := cfhttp.SendSignRound(epB, campfireID, sessionID, 2, nil, nil, aRound2Msgs, idA)
	if err != nil {
		t.Fatalf("sign round 2: %v", err)
	}

	// Deliver B's round-2 to A.
	for _, m := range bRound2Msgs {
		ssA.Deliver(m) //nolint:errcheck
	}
	ssA.ProcessAll()

	// Wait for completion.
	select {
	case <-ssA.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("signing timed out")
	}

	sig, err := ssA.Signature()
	if err != nil {
		t.Fatalf("getting signature: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("expected 64-byte signature, got %d", len(sig))
	}
	if !ed25519.Verify(groupKey, signMsg, sig) {
		t.Fatal("ed25519.Verify failed for threshold signature produced via HTTP sign endpoint")
	}
}
