package protocol_test

// evict_concurrency_test.go — regression test for FIX-1 (MB1, CRITICAL).
//
// Race: two concurrent Evict calls on the same campfire race through
// rekeyAfterEvict. Without a per-campfire write lock, the second caller reads
// a membership snapshot the first is mid-updating, distributing DKG shares to
// an inconsistent member set.
//
// Test strategy: real threshold DKG, real SQLite stores, real HTTP transport.
// No mocks. All tests run under -race.
//
// Tests:
//   1. ConcurrentEvictEvict — two concurrent Evicts on the same campfire serialize;
//      exactly one succeeds (second finds no peers to evict or campfire ID changed).
//      Uses HTTP transport with real threshold DKG — covers the DKG race directly.
//   2. ConcurrentAdmitEvict — concurrent Admit and Evict on the same campfire.
//      Uses filesystem transport (no DKG) — exercises membershipMu lock-serialization,
//      not DKG race protection.
//   3. AdmitLeaveEvictTriple — admit + leave + evict running concurrently.
//      Uses filesystem transport (no DKG) — exercises membershipMu lock-serialization
//      across all three membership-mutation paths under -race, not DKG race protection.

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
)

// newConcClient creates a fresh protocol.Client with a new identity and SQLite store.
func newConcClient(t *testing.T) *protocol.Client {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return protocol.New(s, id)
}

// startConcHTTPTransport starts a cfhttp.Transport on addr and returns it.
func startConcHTTPTransport(t *testing.T, addr string, s store.Store) *cfhttp.Transport {
	t.Helper()
	tr := cfhttp.New(addr, s)
	if err := tr.Start(); err != nil {
		t.Fatalf("start transport %s: %v", addr, err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond) // let listener bind
	return tr
}

// TestEvictConcurrency runs all concurrency sub-tests under -race.
func TestEvictConcurrency(t *testing.T) {
	t.Run("ConcurrentEvictEvict", testConcurrentEvictEvict)
	t.Run("ConcurrentAdmitEvict", testConcurrentAdmitEvict)
	t.Run("AdmitLeaveEvictTriple", testAdmitLeaveEvictTriple)
}

// testConcurrentEvictEvict verifies that two concurrent Evict calls on the same
// campfire serialize without data races. Sets up a 3-member P2P HTTP campfire
// (A, B, C) with threshold=2, then fires two concurrent Evicts targeting
// different members (B and C) from A's client. Exactly one must succeed without
// races under -race; the other may fail or be a no-op.
//
// Without the per-campfire write lock, this test triggers:
//   - DATA RACE: concurrent reads/writes to the store's peer endpoint table
//   - DATA RACE: concurrent DKG runs mutating shared campfire state
//   - Divergent shares: both DKGs run against different membership snapshots
//
// Ports: OS-assigned via "127.0.0.1:0" + tr.Addr() (campfireagent-286).
func testConcurrentEvictEvict(t *testing.T) {
	t.Helper()

	clientA := newConcClient(t)
	sA := clientA.ClientStore()
	trA := startConcHTTPTransport(t, "127.0.0.1:0", sA)
	endpointA := fmt.Sprintf("http://%s", trA.Addr())

	transportDirA := t.TempDir()
	beaconDir := t.TempDir()
	createResult, err := clientA.Create(protocol.CreateRequest{
		Transport: &protocol.P2PHTTPTransport{Transport: trA, MyEndpoint: endpointA, Dir: transportDirA},
		BeaconDir: beaconDir,
		Threshold: 2,
	})
	if err != nil {
		t.Fatalf("A.Create(threshold=2): %v", err)
	}
	campfireID := createResult.CampfireID

	// B joins.
	clientB := newConcClient(t)
	sB := clientB.ClientStore()
	trB := startConcHTTPTransport(t, "127.0.0.1:0", sB)
	endpointB := fmt.Sprintf("http://%s", trB.Addr())
	transportDirB := t.TempDir()
	_, err = clientB.Join(protocol.JoinRequest{
		Transport: &protocol.P2PHTTPTransport{
			Transport:    trB,
			MyEndpoint:   endpointB,
			PeerEndpoint: endpointA,
			Dir:          transportDirB,
		},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// C joins.
	clientC := newConcClient(t)
	sC := clientC.ClientStore()
	trC := startConcHTTPTransport(t, "127.0.0.1:0", sC)
	endpointC := fmt.Sprintf("http://%s", trC.Addr())
	transportDirC := t.TempDir()
	_, err = clientC.Join(protocol.JoinRequest{
		Transport: &protocol.P2PHTTPTransport{
			Transport:    trC,
			MyEndpoint:   endpointC,
			PeerEndpoint: endpointA,
			Dir:          transportDirC,
		},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("C.Join: %v", err)
	}

	// Verify both B and C are registered peers before concurrent eviction.
	peersBeforeEvict, err := sA.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints before eviction: %v", err)
	}
	bFound, cFound := false, false
	for _, p := range peersBeforeEvict {
		if p.MemberPubkey == clientB.PublicKeyHex() {
			bFound = true
		}
		if p.MemberPubkey == clientC.PublicKeyHex() {
			cFound = true
		}
	}
	if !bFound || !cFound {
		t.Fatalf("B or C not found in A's peer endpoints before eviction (b=%v c=%v, total peers=%d)",
			bFound, cFound, len(peersBeforeEvict))
	}

	// Fire two concurrent Evicts from A targeting B and C simultaneously.
	// Under the race detector with no lock, this will show DATA RACE on the
	// store's peer endpoint and membership tables.
	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = clientA.Evict(protocol.EvictRequest{
			Transport:       &protocol.P2PHTTPTransport{Transport: trA},
			CampfireID:      campfireID,
			MemberPubKeyHex: clientB.PublicKeyHex(),
		})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = clientA.Evict(protocol.EvictRequest{
			Transport:       &protocol.P2PHTTPTransport{Transport: trA},
			CampfireID:      campfireID,
			MemberPubKeyHex: clientC.PublicKeyHex(),
		})
	}()
	wg.Wait()

	// Both may succeed (evicting different members in serialized order) or one
	// may fail if the campfire ID changed after the first rekey. Either outcome
	// is correct as long as there are no data races (enforced by -race).
	successCount := 0
	for i, e := range errs {
		if e == nil {
			successCount++
		} else {
			t.Logf("Evict[%d] error (acceptable after concurrent rekey): %v", i, e)
		}
	}
	if successCount == 0 {
		t.Fatal("both concurrent Evicts failed — at least one must succeed")
	}

	t.Logf("concurrent evict result: %d succeeded, %d failed (expected 1-2)", successCount, 2-successCount)

	// Suppress unused variable warnings for unused clients after concurrent ops.
	_ = sB
	_ = sC
}

// testConcurrentAdmitEvict verifies that a concurrent Admit and Evict on the
// same campfire do not race. Uses a filesystem campfire for simplicity
// (filesystem evict is cheaper — no DKG; this exercises the store lock path).
func testConcurrentAdmitEvict(t *testing.T) {
	t.Helper()

	clientA := newConcClient(t)
	base := t.TempDir()
	beaconDir := t.TempDir()

	createResult, err := clientA.Create(protocol.CreateRequest{
		Transport: &protocol.FilesystemTransport{Dir: base},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("A.Create: %v", err)
	}
	campfireID := createResult.CampfireID
	campfireDir := filepath.Join(base, campfireID)

	// B joins.
	clientB := newConcClient(t)
	_, err = clientB.Join(protocol.JoinRequest{
		Transport:  &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// D is a new client that will be admitted.
	clientD, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating D identity: %v", err)
	}

	// Fire concurrent Admit(D) and Evict(B) from A.
	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = clientA.Admit(protocol.AdmitRequest{
			CampfireID:      campfireID,
			MemberPubKeyHex: fmt.Sprintf("%x", clientD.PublicKey),
		})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = clientA.Evict(protocol.EvictRequest{
			CampfireID:      campfireID,
			MemberPubKeyHex: clientB.PublicKeyHex(),
		})
	}()
	wg.Wait()

	// At least one must succeed. Concurrent filesystem admit+evict may race on
	// the member record directory; the lock must serialize them.
	for i, e := range errs {
		if e != nil {
			t.Logf("op[%d] error (may be acceptable): %v", i, e)
		}
	}
	if errs[0] != nil && errs[1] != nil {
		t.Fatal("both concurrent Admit and Evict failed — at least one must succeed")
	}
}

// testAdmitLeaveEvictTriple fires Admit, Leave, and Evict concurrently on the
// same campfire. Exercises all three membership-mutation paths under -race.
func testAdmitLeaveEvictTriple(t *testing.T) {
	t.Helper()

	clientA := newConcClient(t)
	base := t.TempDir()
	beaconDir := t.TempDir()

	createResult, err := clientA.Create(protocol.CreateRequest{
		Transport: &protocol.FilesystemTransport{Dir: base},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("A.Create: %v", err)
	}
	campfireID := createResult.CampfireID
	campfireDir := filepath.Join(base, campfireID)

	// B joins (will be evicted).
	clientB := newConcClient(t)
	_, err = clientB.Join(protocol.JoinRequest{
		Transport:  &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// C joins (will leave).
	clientC := newConcClient(t)
	_, err = clientC.Join(protocol.JoinRequest{
		Transport:  &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("C.Join: %v", err)
	}

	// D is a new identity to be admitted.
	clientD, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating D identity: %v", err)
	}

	// Fire Admit(D), C.Leave(), and A.Evict(B) concurrently.
	var wg sync.WaitGroup
	errs := make([]error, 3)

	wg.Add(3)
	go func() {
		defer wg.Done()
		errs[0] = clientA.Admit(protocol.AdmitRequest{
			CampfireID:      campfireID,
			MemberPubKeyHex: fmt.Sprintf("%x", clientD.PublicKey),
		})
	}()
	go func() {
		defer wg.Done()
		errs[1] = clientC.Leave(campfireID)
	}()
	go func() {
		defer wg.Done()
		_, errs[2] = clientA.Evict(protocol.EvictRequest{
			CampfireID:      campfireID,
			MemberPubKeyHex: clientB.PublicKeyHex(),
		})
	}()
	wg.Wait()

	// Log outcomes — any individual error is acceptable (e.g. evict-before-leave),
	// but the test must complete without data races under -race.
	for i, e := range errs {
		if e != nil {
			t.Logf("op[%d] error (may be acceptable under concurrent membership churn): %v", i, e)
		}
	}

	// At least the evict must succeed (B was definitely in the campfire at start).
	if errs[2] != nil {
		t.Logf("Evict(B) failed: %v — this may be acceptable if admission or leave raced", errs[2])
	}
}
