package http

// Tests for pruneSignSessions, pruneRekeySessions, and removeSignSession.
//
// These are white-box tests (package http) because they need access to the
// unexported signingSessionState, rekeySessionState, and prune methods.
//
// Coverage rationale (workspace-nqlv):
//   - Without tests, a regression that causes premature pruning would silently
//     drop in-progress signing sessions mid-protocol, failing with an opaque
//     "signing session not found" error.
//   - A failure to prune would allow unbounded memory growth — a DoS vector
//     in long-running servers.

import (
	"crypto/ecdh"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
)

// newTransportForTest creates a minimal Transport for unit tests.
// No listener is started; we test the in-memory session maps directly.
func newTransportForTest(t *testing.T) *Transport {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return &Transport{
		store:               s,
		peers:               make(map[string][]PeerInfo),
		signSessions:        make(map[string]*signingSessionState),
		signSessionCampfire: make(map[string]string),
		signSessionCounts:   make(map[string]int),
		rekeySessions:       make(map[string]*rekeySessionState),
		pollBroker: &PollBroker{
			subs:           make(map[string][]chan struct{}),
			limits:         make(map[string]int),
			maxPerCampfire: 64,
		},
	}
}

// TestPruneSignSessions_YoungSessionSurvives verifies that a signing session
// created less than 5 minutes ago is NOT removed by pruneSignSessions.
func TestPruneSignSessions_YoungSessionSurvives(t *testing.T) {
	tr := newTransportForTest(t)

	tr.mu.Lock()
	tr.signSessions["young"] = &signingSessionState{
		createdAt: time.Now().Add(-1 * time.Minute), // 1 min old — within the 5 min window
	}
	tr.pruneSignSessions()
	tr.mu.Unlock()

	tr.mu.RLock()
	_, ok := tr.signSessions["young"]
	tr.mu.RUnlock()

	if !ok {
		t.Error("pruneSignSessions removed a session that is only 1 minute old; expected it to survive")
	}
}

// TestPruneSignSessions_StaleSessionRemoved verifies that a signing session
// created more than 5 minutes ago IS removed by pruneSignSessions.
func TestPruneSignSessions_StaleSessionRemoved(t *testing.T) {
	tr := newTransportForTest(t)

	tr.mu.Lock()
	tr.signSessions["stale"] = &signingSessionState{
		createdAt: time.Now().Add(-6 * time.Minute), // 6 min old — past the 5 min cutoff
	}
	tr.pruneSignSessions()
	tr.mu.Unlock()

	tr.mu.RLock()
	_, ok := tr.signSessions["stale"]
	tr.mu.RUnlock()

	if ok {
		t.Error("pruneSignSessions kept a session that is 6 minutes old; expected it to be removed")
	}
}

// TestPruneSignSessions_MixedSessions verifies that pruneSignSessions selectively
// removes only stale sessions while preserving young ones.
func TestPruneSignSessions_MixedSessions(t *testing.T) {
	tr := newTransportForTest(t)

	tr.mu.Lock()
	tr.signSessions["stale-a"] = &signingSessionState{createdAt: time.Now().Add(-10 * time.Minute)}
	tr.signSessions["stale-b"] = &signingSessionState{createdAt: time.Now().Add(-5*time.Minute - time.Second)}
	tr.signSessions["young-a"] = &signingSessionState{createdAt: time.Now().Add(-4 * time.Minute)}
	tr.signSessions["young-b"] = &signingSessionState{createdAt: time.Now()}
	tr.pruneSignSessions()
	tr.mu.Unlock()

	tr.mu.RLock()
	defer tr.mu.RUnlock()

	for _, id := range []string{"stale-a", "stale-b"} {
		if _, ok := tr.signSessions[id]; ok {
			t.Errorf("pruneSignSessions kept stale session %q; expected removal", id)
		}
	}
	for _, id := range []string{"young-a", "young-b"} {
		if _, ok := tr.signSessions[id]; !ok {
			t.Errorf("pruneSignSessions removed young session %q; expected survival", id)
		}
	}
}

// TestPruneRekeySessions_YoungSessionSurvives verifies that a rekey session
// created less than 5 minutes ago is NOT removed by pruneRekeySessions.
func TestPruneRekeySessions_YoungSessionSurvives(t *testing.T) {
	tr := newTransportForTest(t)

	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating X25519 key: %v", err)
	}

	tr.mu.Lock()
	tr.rekeySessions["young-key"] = &rekeySessionState{
		myPrivKey: priv,
		createdAt: time.Now().Add(-2 * time.Minute),
	}
	tr.pruneRekeySessions()
	tr.mu.Unlock()

	tr.mu.RLock()
	_, ok := tr.rekeySessions["young-key"]
	tr.mu.RUnlock()

	if !ok {
		t.Error("pruneRekeySessions removed a rekey session that is only 2 minutes old; expected survival")
	}
}

// TestPruneRekeySessions_StaleSessionRemoved verifies that a rekey session
// created more than 5 minutes ago IS removed by pruneRekeySessions.
func TestPruneRekeySessions_StaleSessionRemoved(t *testing.T) {
	tr := newTransportForTest(t)

	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating X25519 key: %v", err)
	}

	tr.mu.Lock()
	tr.rekeySessions["stale-key"] = &rekeySessionState{
		myPrivKey: priv,
		createdAt: time.Now().Add(-7 * time.Minute),
	}
	tr.pruneRekeySessions()
	tr.mu.Unlock()

	tr.mu.RLock()
	_, ok := tr.rekeySessions["stale-key"]
	tr.mu.RUnlock()

	if ok {
		t.Error("pruneRekeySessions kept a rekey session that is 7 minutes old; expected removal")
	}
}

// TestPruneRekeySessions_MixedSessions verifies that pruneRekeySessions selectively
// removes only stale sessions while preserving young ones.
func TestPruneRekeySessions_MixedSessions(t *testing.T) {
	tr := newTransportForTest(t)

	newPriv := func(t *testing.T) *ecdh.PrivateKey {
		t.Helper()
		priv, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generating X25519 key: %v", err)
		}
		return priv
	}

	tr.mu.Lock()
	tr.rekeySessions["stale-r1"] = &rekeySessionState{myPrivKey: newPriv(t), createdAt: time.Now().Add(-8 * time.Minute)}
	tr.rekeySessions["stale-r2"] = &rekeySessionState{myPrivKey: newPriv(t), createdAt: time.Now().Add(-5*time.Minute - time.Second)}
	tr.rekeySessions["young-r1"] = &rekeySessionState{myPrivKey: newPriv(t), createdAt: time.Now().Add(-3 * time.Minute)}
	tr.rekeySessions["young-r2"] = &rekeySessionState{myPrivKey: newPriv(t), createdAt: time.Now()}
	tr.pruneRekeySessions()
	tr.mu.Unlock()

	tr.mu.RLock()
	defer tr.mu.RUnlock()

	for _, k := range []string{"stale-r1", "stale-r2"} {
		if _, ok := tr.rekeySessions[k]; ok {
			t.Errorf("pruneRekeySessions kept stale rekey session %q; expected removal", k)
		}
	}
	for _, k := range []string{"young-r1", "young-r2"} {
		if _, ok := tr.rekeySessions[k]; !ok {
			t.Errorf("pruneRekeySessions removed young rekey session %q; expected survival", k)
		}
	}
}

// TestRemoveSignSession verifies that removeSignSession deletes the session
// map entry identified by sessionID.
func TestRemoveSignSession(t *testing.T) {
	tr := newTransportForTest(t)

	// Insert a session directly.
	tr.mu.Lock()
	tr.signSessions["to-remove"] = &signingSessionState{createdAt: time.Now()}
	tr.signSessions["to-keep"] = &signingSessionState{createdAt: time.Now()}
	tr.mu.Unlock()

	// removeSignSession acquires its own lock, so call it without holding mu.
	tr.removeSignSession("to-remove")

	tr.mu.RLock()
	defer tr.mu.RUnlock()

	if _, ok := tr.signSessions["to-remove"]; ok {
		t.Error("removeSignSession: session 'to-remove' still present; expected deletion")
	}
	if _, ok := tr.signSessions["to-keep"]; !ok {
		t.Error("removeSignSession: session 'to-keep' was incorrectly removed")
	}
}

// TestRemoveSignSession_Idempotent verifies that calling removeSignSession on a
// non-existent session ID does not panic.
func TestRemoveSignSession_Idempotent(t *testing.T) {
	tr := newTransportForTest(t)
	// Should not panic — map delete on a missing key is a no-op.
	tr.removeSignSession("does-not-exist")
}

// TestGetOrCreateSignSession_Idempotent verifies that calling getOrCreateSignSession
// twice with the same session_id returns the same *signingSessionState pointer.
//
// This guards against a regression where a second round-1 request for an in-progress
// session would overwrite the existing state, losing committed FROST state and causing
// the protocol to silently produce an invalid signature or fail on round 2.
func TestGetOrCreateSignSession_Idempotent(t *testing.T) {
	tr := newTransportForTest(t)

	// Run DKG for 2 participants with threshold 2.
	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	myResult := dkgResults[1]

	campfireID := "test-campfire-idempotent"
	sessionID := "idempotent-session"
	signerIDs := []uint32{1, 2}
	message := []byte("idempotency test message")

	campfireID := "test-campfire-idempotent"

	// First call — should create a new session.
	tr.mu.Lock()
	first, err := tr.getOrCreateSignSession(campfireID, sessionID, signerIDs, message, myResult, 1)
	tr.mu.Unlock()
	if err != nil {
		t.Fatalf("first getOrCreateSignSession: %v", err)
	}
	if first == nil {
		t.Fatal("first getOrCreateSignSession returned nil session state")
	}

	// Second call with the same session_id — must return the exact same pointer.
	tr.mu.Lock()
	second, err := tr.getOrCreateSignSession(campfireID, sessionID, signerIDs, message, myResult, 1)
	tr.mu.Unlock()
	if err != nil {
		t.Fatalf("second getOrCreateSignSession: %v", err)
	}
	if second != first {
		t.Errorf("getOrCreateSignSession is not idempotent: second call returned a different *signingSessionState (%p != %p)", second, first)
	}

	// Confirm only one entry exists in the map.
	tr.mu.RLock()
	count := len(tr.signSessions)
	tr.mu.RUnlock()
	if count != 1 {
		t.Errorf("expected 1 sign session in map after two calls with same id, got %d", count)
	}
}

// TestPruneSignSessions_ConcurrentCreateAndPrune exercises the prune path under
// concurrent session creation, verifying that mu protects the map from corruption.
// Run with: go test -race ./pkg/transport/http/... -run TestPruneSignSessions_ConcurrentCreateAndPrune
func TestPruneSignSessions_ConcurrentCreateAndPrune(t *testing.T) {
	tr := newTransportForTest(t)

	const goroutines = 20
	var wg sync.WaitGroup

	// Half the goroutines create young sessions; half create stale sessions.
	// All of them call pruneSignSessions while holding the write lock, as the
	// real code does via getOrCreateSignSession.
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			age := time.Duration(i) * time.Minute // 0–19 minutes
			sessionID := "concurrent-session-" + string(rune('a'+i))

			tr.mu.Lock()
			tr.signSessions[sessionID] = &signingSessionState{
				createdAt: time.Now().Add(-age),
			}
			tr.pruneSignSessions()
			tr.mu.Unlock()
		}()
	}

	wg.Wait()

	// After all goroutines complete, no session should be older than 5 minutes.
	tr.mu.RLock()
	defer tr.mu.RUnlock()

	cutoff := time.Now().Add(-5 * time.Minute)
	for id, s := range tr.signSessions {
		if s.createdAt.Before(cutoff) {
			t.Errorf("session %q survived pruning but is older than 5 minutes (createdAt=%v)", id, s.createdAt)
		}
	}
}

// TestSignSessionPerCampfireCapEnforced verifies that getOrCreateSignSession rejects
// new sessions once the per-campfire cap (maxSignSessionsPerCampfire) is reached,
// while still accepting sessions for a different campfire.
//
// This guards against workspace-2uo: a verified member sending POST /campfire/{id}/sign
// round=1 with unique session IDs to exhaust server memory.
func TestSignSessionPerCampfireCapEnforced(t *testing.T) {
	tr := newTransportForTest(t)

	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	myResult := dkgResults[1]
	signerIDs := []uint32{1, 2}
	msg := []byte("cap enforcement test")

	campfireA := "campfire-cap-a"
	campfireB := "campfire-cap-b"

	// Fill campfireA up to the cap.
	tr.mu.Lock()
	for i := 0; i < maxSignSessionsPerCampfire; i++ {
		sessionID := fmt.Sprintf("session-%d", i)
		if _, err := tr.getOrCreateSignSession(campfireA, sessionID, signerIDs, msg, myResult, 1); err != nil {
			tr.mu.Unlock()
			t.Fatalf("creating session %d: unexpected error: %v", i, err)
		}
	}
	tr.mu.Unlock()

	// One more session for campfireA must be rejected.
	tr.mu.Lock()
	_, err = tr.getOrCreateSignSession(campfireA, "session-overflow", signerIDs, msg, myResult, 1)
	tr.mu.Unlock()
	if err == nil {
		t.Error("expected errSignSessionCapExceeded when cap is exceeded, got nil")
	}
	if err != errSignSessionCapExceeded {
		t.Errorf("expected errSignSessionCapExceeded, got: %v", err)
	}

	// campfireB must still accept sessions — cap is per-campfire.
	tr.mu.Lock()
	_, err = tr.getOrCreateSignSession(campfireB, "session-b-0", signerIDs, msg, myResult, 1)
	tr.mu.Unlock()
	if err != nil {
		t.Errorf("campfireB session rejected unexpectedly: %v", err)
	}
}

// TestSignSessionCountDecrementedOnRemove verifies that removeSignSession decrements
// the per-campfire count, allowing new sessions to be created after old ones complete.
func TestSignSessionCountDecrementedOnRemove(t *testing.T) {
	tr := newTransportForTest(t)

	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	myResult := dkgResults[1]
	signerIDs := []uint32{1, 2}
	msg := []byte("count decrement test")
	campfireID := "campfire-count-decrement"

	// Fill to cap.
	tr.mu.Lock()
	for i := 0; i < maxSignSessionsPerCampfire; i++ {
		sessionID := fmt.Sprintf("decrement-session-%d", i)
		if _, err := tr.getOrCreateSignSession(campfireID, sessionID, signerIDs, msg, myResult, 1); err != nil {
			tr.mu.Unlock()
			t.Fatalf("creating session %d: %v", i, err)
		}
	}
	tr.mu.Unlock()

	// Remove one session (simulates round-2 completion).
	tr.removeSignSession("decrement-session-0")

	// Now a new session must succeed.
	tr.mu.Lock()
	_, err = tr.getOrCreateSignSession(campfireID, "decrement-session-new", signerIDs, msg, myResult, 1)
	tr.mu.Unlock()
	if err != nil {
		t.Errorf("expected new session to succeed after removal, got: %v", err)
	}
}

// TestSignSessionCountDecrementedOnPrune verifies that pruneSignSessions decrements
// the per-campfire count for expired sessions, freeing capacity.
func TestSignSessionCountDecrementedOnPrune(t *testing.T) {
	tr := newTransportForTest(t)

	dkgResults, err := threshold.RunDKG([]uint32{1, 2}, 2)
	if err != nil {
		t.Fatalf("RunDKG: %v", err)
	}
	myResult := dkgResults[1]
	signerIDs := []uint32{1, 2}
	msg := []byte("prune decrement test")
	campfireID := "campfire-prune-decrement"

	// Fill to cap using the API (so counts are tracked correctly).
	tr.mu.Lock()
	for i := 0; i < maxSignSessionsPerCampfire; i++ {
		sessionID := fmt.Sprintf("prune-session-%d", i)
		if _, err := tr.getOrCreateSignSession(campfireID, sessionID, signerIDs, msg, myResult, 1); err != nil {
			tr.mu.Unlock()
			t.Fatalf("creating session %d: %v", i, err)
		}
	}
	// Age all sessions past the 5-minute cutoff.
	for _, s := range tr.signSessions {
		s.createdAt = time.Now().Add(-10 * time.Minute)
	}
	tr.pruneSignSessions()
	tr.mu.Unlock()

	// All sessions pruned — count should be zero. New session must succeed.
	tr.mu.Lock()
	_, err = tr.getOrCreateSignSession(campfireID, "prune-session-new", signerIDs, msg, myResult, 1)
	tr.mu.Unlock()
	if err != nil {
		t.Errorf("expected new session after prune, got: %v", err)
	}
}
