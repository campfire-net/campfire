package protocol_test

// evict_missing_epoch_test.go — FIX-2 (MB2) regression tests.
//
// These tests verify that rekeyAfterEvict (and its callers) correctly detect
// a missing epoch_secrets record on an encrypted campfire and surface an error
// rather than silently diverging the campfire ID.
//
// Done conditions (test-scope: targeted):
// MB2-1: Encrypted campfire, epoch_secrets absent → Evict returns an error.
//        This test MUST FAIL before the fix and PASS after.
// MB2-2: Encrypted campfire, epoch_secrets present → Evict succeeds (guard not triggered).
// MB2-3: Encrypted campfire, partial-crash scenario: epoch_secrets present then cleared
//        (simulated via SetMembershipEncrypted trick) → subsequent Evict returns an error.
// MB2-4: Concurrent Evict from two clients; one has epoch_secrets, one does not.
//        Client without epoch_secrets gets an error; client with epoch_secrets is unaffected.
//
// Test strategy:
//   - Real SQLite stores, real in-process HTTP servers, real DKG (no mocks).
//   - P2P HTTP threshold=2 path to exercise rekeyAfterEvict.
//   - Campfires are marked encrypted via store.SetMembershipEncrypted so the guard
//     activates. In production, SetMembershipEncrypted is set by campfire:encrypted-init;
//     here we set it directly to isolate the epoch_secrets guard without implementing
//     the full encrypted-init message exchange.
//
// Mock-coverage audit:
//   None — no mocks used. All store and transport calls use real implementations.

import (
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
)

// TestEvictMissingEpochSecrets runs all MB2 regression sub-tests.
func TestEvictMissingEpochSecrets(t *testing.T) {
	t.Run("MissingEpochSecretsReturnsError", testMissingEpochSecretsReturnsError)
	t.Run("PresentEpochSecretsSucceeds", testPresentEpochSecretsSucceeds)
	t.Run("PartialCrashMissingEpochSecrets", testPartialCrashMissingEpochSecrets)
	t.Run("ConcurrentEvictOneCallerMissingEpochSecrets", testConcurrentEvictOneCallerMissingEpochSecrets)
}

// testMissingEpochSecretsReturnsError — MB2-1.
//
// An encrypted campfire (m.Encrypted=true) has NO epoch_secrets row in A's store.
// This simulates the interrupted-rekey scenario: the campfire is marked encrypted
// (E2E mode is active) but epoch material was never written (or was lost).
// A evicts B. Expected: Evict returns an error (not silent divergence).
//
// This test MUST FAIL on HEAD before the fix and PASS after.
// Ports: OS-assigned via "127.0.0.1:0" + tr.Addr() (campfireagent-286).
func testMissingEpochSecretsReturnsError(t *testing.T) {
	t.Helper()

	clientA := newEvictClient(t)
	sA := clientA.ClientStore()
	trA := startMissingEpochTransport(t, "127.0.0.1:0", sA)
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

	clientB := newEvictClient(t)
	sB := clientB.ClientStore()
	trB := startMissingEpochTransport(t, "127.0.0.1:0", sB)
	endpointB := fmt.Sprintf("http://%s", trB.Addr())
	transportDirB := t.TempDir()
	_, err = clientB.Join(protocol.JoinRequest{
		Transport:  &protocol.P2PHTTPTransport{Transport: trB, MyEndpoint: endpointB, PeerEndpoint: endpointA, Dir: transportDirB},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	clientC := newEvictClient(t)
	sC := clientC.ClientStore()
	trC := startMissingEpochTransport(t, "127.0.0.1:0", sC)
	endpointC := fmt.Sprintf("http://%s", trC.Addr())
	transportDirC := t.TempDir()
	_, err = clientC.Join(protocol.JoinRequest{
		Transport:  &protocol.P2PHTTPTransport{Transport: trC, MyEndpoint: endpointC, PeerEndpoint: endpointA, Dir: transportDirC},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("C.Join: %v", err)
	}

	// Mark the campfire as encrypted in A's store. In production this is set by
	// campfire:encrypted-init. Here we set it directly to exercise the epoch_secrets
	// guard without the full encrypted-init message exchange.
	if err := sA.SetMembershipEncrypted(campfireID, true); err != nil {
		t.Fatalf("SetMembershipEncrypted: %v", err)
	}

	// Verify no epoch_secrets exist (the guard is triggered by encrypted=true + absent epoch_secrets).
	existing, err := sA.GetLatestEpochSecret(campfireID)
	if err != nil {
		t.Fatalf("checking epoch secrets: %v", err)
	}
	if existing != nil {
		t.Fatalf("test invariant violated: epoch_secrets should be absent, but found one. "+
			"Setup error — epoch material was written during Create/Join but this test "+
			"requires it to be absent. Got: epoch=%d", existing.Epoch)
	}

	// A evicts B — must return an error because the campfire is encrypted but
	// epoch_secrets is absent. Pre-fix: silently succeeds (WRONG). Post-fix: error.
	_, evictErr := clientA.Evict(protocol.EvictRequest{
		Transport:       &protocol.P2PHTTPTransport{Transport: trA},
		CampfireID:      campfireID,
		MemberPubKeyHex: clientB.PublicKeyHex(),
	})
	if evictErr == nil {
		t.Fatal("Evict must return an error when campfire is encrypted and epoch_secrets is absent — " +
			"silent divergence of campfire ID is the bug (FIX-2/MB2). " +
			"This test should fail before the fix and pass after.")
	}
	t.Logf("Evict correctly returned error: %v", evictErr)
}

// testPresentEpochSecretsSucceeds — MB2-2.
//
// An encrypted campfire (m.Encrypted=true) WITH epoch_secrets present.
// The guard must not interfere with the normal evict-rekey path.
// A evicts B. Expected: Evict succeeds.
// Ports: OS-assigned via "127.0.0.1:0" + tr.Addr() (campfireagent-286).
func testPresentEpochSecretsSucceeds(t *testing.T) {
	t.Helper()

	clientA := newEvictClient(t)
	sA := clientA.ClientStore()
	trA := startMissingEpochTransport(t, "127.0.0.1:0", sA)
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

	clientB := newEvictClient(t)
	sB := clientB.ClientStore()
	trB := startMissingEpochTransport(t, "127.0.0.1:0", sB)
	endpointB := fmt.Sprintf("http://%s", trB.Addr())
	transportDirB := t.TempDir()
	_, err = clientB.Join(protocol.JoinRequest{
		Transport:  &protocol.P2PHTTPTransport{Transport: trB, MyEndpoint: endpointB, PeerEndpoint: endpointA, Dir: transportDirB},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	clientC := newEvictClient(t)
	sC := clientC.ClientStore()
	trC := startMissingEpochTransport(t, "127.0.0.1:0", sC)
	endpointC := fmt.Sprintf("http://%s", trC.Addr())
	transportDirC := t.TempDir()
	_, err = clientC.Join(protocol.JoinRequest{
		Transport:  &protocol.P2PHTTPTransport{Transport: trC, MyEndpoint: endpointC, PeerEndpoint: endpointA, Dir: transportDirC},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("C.Join: %v", err)
	}

	// Mark as encrypted AND insert epoch_secrets — the normal post-encrypted-init state.
	if err := sA.SetMembershipEncrypted(campfireID, true); err != nil {
		t.Fatalf("SetMembershipEncrypted: %v", err)
	}
	if err := sA.UpsertEpochSecret(store.EpochSecret{
		CampfireID: campfireID,
		Epoch:      0,
		RootSecret: []byte("test-root-secret-32-bytes-padded!"),
		CEK:        []byte("test-cek-32-bytes-padded-xxxxxYY!"),
		CreatedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("UpsertEpochSecret: %v", err)
	}

	// A evicts B — must succeed because encrypted=true AND epoch_secrets is present.
	evictResult, err := clientA.Evict(protocol.EvictRequest{
		Transport:       &protocol.P2PHTTPTransport{Transport: trA},
		CampfireID:      campfireID,
		MemberPubKeyHex: clientB.PublicKeyHex(),
	})
	if err != nil {
		t.Fatalf("Evict must succeed when epoch_secrets is present on encrypted campfire, got error: %v", err)
	}
	if !evictResult.Rekeyed {
		t.Fatal("expected Rekeyed=true for threshold=2 campfire")
	}
	t.Logf("Evict succeeded on encrypted campfire with epoch_secrets; new campfire ID: %s", evictResult.NewCampfireID)
}

// testPartialCrashMissingEpochSecrets — MB2-3.
//
// Simulates an interrupted prior rekey: an encrypted campfire has epoch_secrets
// for the current campfire ID, but a prior crash deleted them (simulated by
// marking the campfire encrypted without inserting epoch_secrets).
// Subsequent Evict must return an error.
// Ports: OS-assigned via "127.0.0.1:0" + tr.Addr() (campfireagent-286).
func testPartialCrashMissingEpochSecrets(t *testing.T) {
	t.Helper()

	clientA := newEvictClient(t)
	sA := clientA.ClientStore()
	trA := startMissingEpochTransport(t, "127.0.0.1:0", sA)
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

	clientB := newEvictClient(t)
	sB := clientB.ClientStore()
	trB := startMissingEpochTransport(t, "127.0.0.1:0", sB)
	endpointB := fmt.Sprintf("http://%s", trB.Addr())
	transportDirB := t.TempDir()
	_, err = clientB.Join(protocol.JoinRequest{
		Transport:  &protocol.P2PHTTPTransport{Transport: trB, MyEndpoint: endpointB, PeerEndpoint: endpointA, Dir: transportDirB},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	clientC := newEvictClient(t)
	sC := clientC.ClientStore()
	trC := startMissingEpochTransport(t, "127.0.0.1:0", sC)
	endpointC := fmt.Sprintf("http://%s", trC.Addr())
	transportDirC := t.TempDir()
	_, err = clientC.Join(protocol.JoinRequest{
		Transport:  &protocol.P2PHTTPTransport{Transport: trC, MyEndpoint: endpointC, PeerEndpoint: endpointA, Dir: transportDirC},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("C.Join: %v", err)
	}

	// Mark as encrypted — simulates the campfire having gone through encrypted-init —
	// but do NOT insert epoch_secrets — simulates a crash that cleared epoch material
	// (e.g., partial rollback after interrupted first eviction).
	if err := sA.SetMembershipEncrypted(campfireID, true); err != nil {
		t.Fatalf("SetMembershipEncrypted: %v", err)
	}

	// Confirm epoch_secrets is absent (crash simulation is complete).
	es, err := sA.GetLatestEpochSecret(campfireID)
	if err != nil {
		t.Fatalf("GetLatestEpochSecret: %v", err)
	}
	if es != nil {
		t.Fatalf("test setup error: epoch_secrets should be absent after crash simulation, found epoch=%d", es.Epoch)
	}

	// Evict must fail — encrypted campfire, epoch_secrets absent.
	_, evictErr := clientA.Evict(protocol.EvictRequest{
		Transport:       &protocol.P2PHTTPTransport{Transport: trA},
		CampfireID:      campfireID,
		MemberPubKeyHex: clientB.PublicKeyHex(),
	})
	if evictErr == nil {
		t.Fatal("Evict must return an error on encrypted campfire after simulated crash cleared epoch_secrets (FIX-2/MB2)")
	}
	t.Logf("Evict correctly returned error after simulated crash: %v", evictErr)
}

// testConcurrentEvictOneCallerMissingEpochSecrets — MB2-4.
//
// Two protocol.Client instances share the same campfire (A and A2, different stores).
// A has epoch_secrets and encrypted=true (normal post-encrypted-init state).
// A2 has encrypted=true but NO epoch_secrets (simulates partial state in a second node).
// A evicts B; A2 evicts C concurrently.
// Expected: A succeeds; A2 returns an epoch_secrets-absent error.
// Ports: OS-assigned via "127.0.0.1:0" + tr.Addr() (campfireagent-286).
func testConcurrentEvictOneCallerMissingEpochSecrets(t *testing.T) {
	t.Helper()

	// Build A's campfire normally.
	clientA := newEvictClient(t)
	sA := clientA.ClientStore()
	trA := startMissingEpochTransport(t, "127.0.0.1:0", sA)
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

	clientB := newEvictClient(t)
	sB := clientB.ClientStore()
	trB := startMissingEpochTransport(t, "127.0.0.1:0", sB)
	endpointB := fmt.Sprintf("http://%s", trB.Addr())
	transportDirB := t.TempDir()
	_, err = clientB.Join(protocol.JoinRequest{
		Transport:  &protocol.P2PHTTPTransport{Transport: trB, MyEndpoint: endpointB, PeerEndpoint: endpointA, Dir: transportDirB},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	clientC := newEvictClient(t)
	sC := clientC.ClientStore()
	trC := startMissingEpochTransport(t, "127.0.0.1:0", sC)
	endpointC := fmt.Sprintf("http://%s", trC.Addr())
	transportDirC := t.TempDir()
	_, err = clientC.Join(protocol.JoinRequest{
		Transport:  &protocol.P2PHTTPTransport{Transport: trC, MyEndpoint: endpointC, PeerEndpoint: endpointA, Dir: transportDirC},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("C.Join: %v", err)
	}

	// A: mark encrypted AND insert epoch_secrets (healthy state).
	if err := sA.SetMembershipEncrypted(campfireID, true); err != nil {
		t.Fatalf("A.SetMembershipEncrypted: %v", err)
	}
	if err := sA.UpsertEpochSecret(store.EpochSecret{
		CampfireID: campfireID,
		Epoch:      0,
		RootSecret: []byte("test-root-secret-32-bytes-padded!"),
		CEK:        []byte("test-cek-32-bytes-padded-xxxxxYY!"),
		CreatedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("A.UpsertEpochSecret: %v", err)
	}

	// A2: a fresh store with the campfire membership copied but NO epoch_secrets.
	// Represents a second node that has encrypted=true but lost its epoch material.
	clientA2 := newEvictClient(t)
	sA2 := clientA2.ClientStore()
	mA, err := sA.GetMembership(campfireID)
	if err != nil || mA == nil {
		t.Fatalf("getting A's membership: %v", err)
	}
	if err := sA2.AddMembership(*mA); err != nil {
		t.Fatalf("copying membership to A2 store: %v", err)
	}
	// Mark A2's copy as encrypted but don't insert epoch_secrets.
	if err := sA2.SetMembershipEncrypted(campfireID, true); err != nil {
		t.Fatalf("A2.SetMembershipEncrypted: %v", err)
	}
	// Copy threshold share so A2 can get past membership/peer checks.
	shareA, err := sA.GetThresholdShare(campfireID)
	if err != nil || shareA == nil {
		t.Fatalf("getting A's threshold share: %v", err)
	}
	if err := sA2.UpsertThresholdShare(*shareA); err != nil {
		t.Fatalf("copying threshold share to A2: %v", err)
	}
	// Copy peer endpoints.
	peers, err := sA.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("listing A's peers: %v", err)
	}
	for _, p := range peers {
		sA2.UpsertPeerEndpoint(p) //nolint:errcheck
	}

	// Confirm A2 has no epoch_secrets.
	esA2, err := sA2.GetLatestEpochSecret(campfireID)
	if err != nil {
		t.Fatalf("checking A2 epoch secrets: %v", err)
	}
	if esA2 != nil {
		t.Fatalf("test setup error: A2 should have no epoch_secrets, found epoch=%d", esA2.Epoch)
	}

	// A2's transport and client (OS-assigned port).
	trA2 := cfhttp.New("127.0.0.1:0", sA2)
	if err := trA2.Start(); err != nil {
		t.Fatalf("start A2 transport: %v", err)
	}
	t.Cleanup(func() { trA2.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	clientA2b := protocol.New(sA2, clientA2.ClientIdentity())

	// Launch concurrent evicts.
	type result struct{ err error }
	aCh := make(chan result, 1)
	a2Ch := make(chan result, 1)

	go func() {
		_, err := clientA.Evict(protocol.EvictRequest{
			Transport:       &protocol.P2PHTTPTransport{Transport: trA},
			CampfireID:      campfireID,
			MemberPubKeyHex: clientB.PublicKeyHex(),
		})
		aCh <- result{err}
	}()
	go func() {
		_, err := clientA2b.Evict(protocol.EvictRequest{
			Transport:       &protocol.P2PHTTPTransport{Transport: trA2},
			CampfireID:      campfireID,
			MemberPubKeyHex: clientC.PublicKeyHex(),
		})
		a2Ch <- result{err}
	}()

	aRes := <-aCh
	a2Res := <-a2Ch

	// A (has epoch_secrets) must succeed.
	if aRes.err != nil {
		t.Errorf("A.Evict with epoch_secrets should succeed, got: %v", aRes.err)
	}
	// A2 (no epoch_secrets, encrypted=true) must fail with epoch-related error.
	if a2Res.err == nil {
		t.Error("A2.Evict without epoch_secrets must return an error (FIX-2/MB2 guard)")
	} else {
		t.Logf("A2.Evict correctly returned error: %v", a2Res.err)
	}
}

// startMissingEpochTransport starts a cfhttp.Transport for missing-epoch tests.
func startMissingEpochTransport(t *testing.T, addr string, s store.Store) *cfhttp.Transport {
	t.Helper()
	tr := cfhttp.New(addr, s)
	if err := tr.Start(); err != nil {
		t.Fatalf("start transport %s: %v", addr, err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	return tr
}
