package http_test

// TestSignRound1ConcurrentRequests verifies that concurrent round-1 requests for the
// same session_id do not race on the SigningSession state machine.
//
// Run with: go test -race ./pkg/transport/http/... -run TestSignRound1ConcurrentRequests
//
// This test was written to cover workspace-17qu.5: Start() and Deliver() in round 1
// must be protected by a per-session lock to prevent goroutine-unsafe concurrent access.
//
// TestSignRound2DoesNotBlockPeers verifies that a round-2 request does not hold the
// global transport write lock during FROST crypto operations. If the lock were held
// for the full round-2 duration, concurrent AddPeer calls would deadlock under test.
//
// This test was written to cover workspace-uwf4: handleSign round 2 held the global
// transport write lock across ProcessAll()/Deliver() — blocking all other handlers.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// makeCBORSignMsg returns a CBOR-encoded MessageSignInput for use in signing tests.
// handleSign validates that MessageToSign is a canonical campfire sign input type.
func makeCBORSignMsg(t *testing.T, id string, payload []byte) []byte {
	t.Helper()
	si := message.MessageSignInput{
		ID:          id,
		Payload:     payload,
		Tags:        []string{"test"},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
	}
	b, err := cfencoding.Marshal(si)
	if err != nil {
		t.Fatalf("marshaling MessageSignInput: %v", err)
	}
	return b
}

// TestSignRound1ConcurrentRequests launches 3 goroutines that each send a round-1
// sign request for the same session_id to the same transport. The -race detector
// must not report a data race.
func TestSignRound1ConcurrentRequests(t *testing.T) {
	campfireID := "sign-race-campfire"

	// Run DKG for 2 participants with threshold 2.
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
	sB.UpsertThresholdShare(store.ThresholdShare{CampfireID: campfireID, ParticipantID: 2, SecretShare: shareB}) //nolint:errcheck

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+40)
	epB := fmt.Sprintf("http://%s", addrB)

	buildShareProvider := func(s *store.Store) cfhttp.ThresholdShareProvider {
		return func(cfID string) (uint32, []byte, error) {
			share, err := s.GetThresholdShare(cfID)
			if err != nil || share == nil {
				return 0, nil, fmt.Errorf("no share for %s", cfID)
			}
			return share.ParticipantID, share.SecretShare, nil
		}
	}

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idA.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(buildShareProvider(sB))
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	signMsg := makeCBORSignMsg(t, "race-test-msg-1", []byte("concurrent round-1 race test message"))
	signerIDs := []uint32{1, 2}
	sessionID := "race-test-session-1"

	// Build A's round-1 messages once (reused by all goroutines as input).
	ssA, err := threshold.NewSigningSession(dkgResults[1].SecretShare, dkgResults[1].Public, signMsg, signerIDs)
	if err != nil {
		t.Fatalf("NewSigningSession A: %v", err)
	}
	aRound1Msgs := ssA.Start()

	// Launch 3 concurrent round-1 requests for the same session_id.
	// All will race to call Start()+Deliver() on the same signingSessionState.
	const concurrency = 3
	var wg sync.WaitGroup
	errs := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cfhttp.SendSignRound(epB, campfireID, sessionID, 1, signerIDs, signMsg, aRound1Msgs, idA)
			errs[i] = err
		}()
	}
	wg.Wait()

	// At least one request should succeed (the one that won the race to create the session).
	// Subsequent ones will re-use the same session — they may fail or succeed depending on
	// FROST state machine; what matters is NO DATA RACE detected by -race.
	successCount := 0
	for _, e := range errs {
		if e == nil {
			successCount++
		}
	}
	if successCount == 0 {
		t.Errorf("all %d concurrent round-1 requests failed; expected at least one to succeed: %v", concurrency, errs)
	}
}

// TestSignRound1SingleRequest verifies that a single round-1 request still
// returns valid commitments after the locking change.
func TestSignRound1SingleRequest(t *testing.T) {
	campfireID := "sign-single-round1"

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
	sB.UpsertThresholdShare(store.ThresholdShare{CampfireID: campfireID, ParticipantID: 2, SecretShare: shareB}) //nolint:errcheck

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+41)
	epB := fmt.Sprintf("http://%s", addrB)

	buildShareProvider := func(s *store.Store) cfhttp.ThresholdShareProvider {
		return func(cfID string) (uint32, []byte, error) {
			share, err := s.GetThresholdShare(cfID)
			if err != nil || share == nil {
				return 0, nil, fmt.Errorf("no share for %s", cfID)
			}
			return share.ParticipantID, share.SecretShare, nil
		}
	}

	trB := cfhttp.New(addrB, sB)
	trB.SetSelfInfo(idA.PublicKeyHex(), epB)
	trB.SetThresholdShareProvider(buildShareProvider(sB))
	if err := trB.Start(); err != nil {
		t.Fatalf("starting transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	signMsg := makeCBORSignMsg(t, "single-test-msg-1", []byte("single round-1 correctness test"))
	signerIDs := []uint32{1, 2}
	sessionID := "single-test-session-1"

	ssA, err := threshold.NewSigningSession(dkgResults[1].SecretShare, dkgResults[1].Public, signMsg, signerIDs)
	if err != nil {
		t.Fatalf("NewSigningSession A: %v", err)
	}
	aRound1Msgs := ssA.Start()

	bRound1Msgs, err := cfhttp.SendSignRound(epB, campfireID, sessionID, 1, signerIDs, signMsg, aRound1Msgs, idA)
	if err != nil {
		t.Fatalf("round-1 request failed: %v", err)
	}
	if len(bRound1Msgs) == 0 {
		t.Fatal("expected round-1 commitments from B, got none")
	}
}

// TestSignRound2DoesNotBlockPeers verifies that a round-2 sign request does not hold
// the global transport write lock during FROST crypto operations. It does this by:
//  1. Completing round 1 to establish a signing session on B.
//  2. Launching a round-2 request in a goroutine.
//  3. Concurrently calling AddPeer (which acquires the global write lock) and verifying
//     it completes within a short deadline — not deadlocked behind round-2 FROST crypto.
//
// Run with: go test -race ./pkg/transport/http/... -run TestSignRound2DoesNotBlockPeers
func TestSignRound2DoesNotBlockPeers(t *testing.T) {
	campfireID := "sign-round2-no-block"

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
	sB.UpsertThresholdShare(store.ThresholdShare{CampfireID: campfireID, ParticipantID: 2, SecretShare: shareB})                        //nolint:errcheck

	base := portBase()
	addrB := fmt.Sprintf("127.0.0.1:%d", base+42)
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

	signMsg := makeCBORSignMsg(t, "round2-no-block-msg", []byte("round2-no-block test message"))
	signerIDs := []uint32{1, 2}
	sessionID := "round2-no-block-session"

	// Build A's signing session and send round 1 to B.
	ssA, err := threshold.NewSigningSession(dkgResults[1].SecretShare, dkgResults[1].Public, signMsg, signerIDs)
	if err != nil {
		t.Fatalf("NewSigningSession A: %v", err)
	}
	aRound1Msgs := ssA.Start()

	bRound1Msgs, err := cfhttp.SendSignRound(epB, campfireID, sessionID, 1, signerIDs, signMsg, aRound1Msgs, idA)
	if err != nil {
		t.Fatalf("round-1 to B failed: %v", err)
	}
	for _, m := range bRound1Msgs {
		if err := ssA.Deliver(m); err != nil {
			t.Fatalf("A delivering B round-1 msg: %v", err)
		}
	}
	aRound2Msgs := ssA.ProcessAll()

	// Launch round 2 in a goroutine.
	round2Done := make(chan error, 1)
	go func() {
		_, err := cfhttp.SendSignRound(epB, campfireID, sessionID, 2, nil, nil, aRound2Msgs, idA)
		round2Done <- err
	}()

	// Concurrently call AddPeer, which must acquire the global write lock.
	// If round-2 were holding the global lock during FROST crypto, this would
	// deadlock (or take > 500ms). The deadline is intentionally tight.
	peerDone := make(chan struct{}, 1)
	go func() {
		trB.AddPeer(campfireID, "deadbeef", "http://127.0.0.1:9999")
		peerDone <- struct{}{}
	}()

	select {
	case <-peerDone:
		// AddPeer completed quickly — global lock was not held by round-2 FROST ops.
	case <-time.After(500 * time.Millisecond):
		t.Error("AddPeer blocked for >500ms: round-2 may still be holding the global write lock during FROST crypto")
	}

	// Wait for round 2 to finish too.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-round2Done:
		case <-time.After(5 * time.Second):
			t.Error("round-2 request timed out")
		}
	}()
	wg.Wait()
}
