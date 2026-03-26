package main

// concurrent_join_test.go — Tests for per-campfireID mutex in handleRemoteJoin.
//
// Verifies that concurrent calls to handleRemoteJoin for the same campfireID
// are serialized so the losing call's cleanup does not delete the winner's
// successfully-written state.
//
// Also verifies that concurrent joins for DIFFERENT campfireIDs are not
// over-serialized — both succeed independently.
//
// Bead: campfire-agent-w9y

import (
	"errors"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// delayThenFailStore: wraps a store, delays AddMembership then returns error.
// Used to force the TOCTOU window open: the slow call sees dir exists but
// then fails at AddMembership, triggering cleanup of the shared dir.
// ---------------------------------------------------------------------------

type delayThenFailStore struct {
	store.Store
	delay            time.Duration
	addMembershipErr error
	mu               sync.Mutex
	callCount        int
}

func (d *delayThenFailStore) AddMembership(m store.Membership) error {
	d.mu.Lock()
	d.callCount++
	count := d.callCount
	d.mu.Unlock()

	// Only delay and fail the SECOND concurrent call to force the race window.
	if count > 1 {
		time.Sleep(d.delay)
		return d.addMembershipErr
	}
	// First call delegates normally — succeeds and writes state.
	return d.Store.AddMembership(m)
}

// ---------------------------------------------------------------------------
// Test 1: Concurrent joins for the SAME campfireID — one wins, dir survives.
//
// Setup: srvB shares a store where the 2nd AddMembership call is delayed and
// then fails. Two goroutines dispatch campfire_join for the same campfireID
// concurrently. Without the mutex:
//   - Both goroutines see dirExistedBefore=false (dir not yet created)
//   - Goroutine 1 creates dir, writes campfire.cbor, calls AddMembership (succeeds, success=true)
//   - Goroutine 2 (delayed at AddMembership) gets count=2, fails, success=false → defer
//     runs RemoveAll(campfireDir) — deletes goroutine 1's state.
//
// With the mutex, goroutines are serialized: goroutine 2 acquires lock only
// after goroutine 1 finishes. When goroutine 2 runs, dirExistedBefore=true
// (dir was created by goroutine 1), so cleanup is suppressed.
// ---------------------------------------------------------------------------

// TestConcurrentJoin_SameCampfire launches two goroutines both calling
// handleRemoteJoin for the same campfireID on the same server. Verifies that
// the campfire directory and campfire.cbor survive after both complete.
func TestConcurrentJoin_SameCampfire(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	origValidate := ssrfValidateEndpoint
	ssrfValidateEndpoint = func(string) error { return nil }
	t.Cleanup(func() { ssrfValidateEndpoint = origValidate })
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})

	// Control the fs transport directory so we can verify filesystem state.
	transportDir := newCleanupTransportDir(t)

	// Server A: owns the campfire.
	campfireID, tsURL := setupServerAWithCampfire(t)

	// Determine the campfire dir that handleRemoteJoin would create.
	transport := fs.New(transportDir)
	campfireDir := transport.CampfireDir(campfireID)

	// Server B: single instance (both goroutines call the same server).
	// Store: 2nd AddMembership call is delayed, then fails.
	srvB, realStore := newTestServerWithStore(t)
	doInit(t, srvB)
	delayStore := &delayThenFailStore{
		Store:            realStore,
		delay:            50 * time.Millisecond,
		addMembershipErr: errors.New("injected: duplicate join"),
	}
	srvB.st = delayStore

	// Synchronise both goroutines to maximise overlap.
	var ready sync.WaitGroup
	var startGate sync.WaitGroup
	ready.Add(2)
	startGate.Add(1)

	type result struct {
		err *rpcError
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	joinArgs := `{"name":"campfire_join","arguments":{"campfire_id":"` + campfireID + `","peer_endpoint":"` + tsURL + `"}}`

	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			ready.Done()
			startGate.Wait()
			resp := srvB.dispatch(makeReq("tools/call", joinArgs))
			results[i] = result{err: resp.Error}
		}()
	}

	// Release both goroutines simultaneously.
	ready.Wait()
	startGate.Done()
	wg.Wait()

	// Exactly one join succeeded (the first). The second got the injected error.
	successCount := 0
	for _, r := range results {
		if r.err == nil {
			successCount++
		}
	}
	if successCount == 0 {
		t.Fatal("both concurrent joins failed — expected exactly one to succeed")
	}

	// The campfire directory MUST exist with campfire.cbor intact.
	// Without the mutex, the failing goroutine's defer would call
	// os.RemoveAll(campfireDir) and delete the winner's state.
	if _, err := os.Stat(campfireDir); err != nil {
		t.Errorf("campfire dir %s missing after concurrent joins: %v — cleanup race deleted it", campfireDir, err)
	}
	cbor := campfireDir + "/campfire.cbor"
	if _, err := os.Stat(cbor); err != nil {
		t.Errorf("campfire.cbor missing after concurrent joins: %v — cleanup race deleted it", err)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Concurrent joins for DIFFERENT campfireIDs — both succeed.
//
// Verifies that the per-campfireID mutex does not over-serialize: joins for
// campfireID-1 and campfireID-2 proceed concurrently without blocking each other.
// ---------------------------------------------------------------------------

// TestConcurrentJoin_DifferentCampfires verifies that concurrent joins for
// different campfireIDs are not over-serialized by the per-campfireID mutex.
// Both must succeed independently.
func TestConcurrentJoin_DifferentCampfires(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	origValidate := ssrfValidateEndpoint
	ssrfValidateEndpoint = func(string) error { return nil }
	t.Cleanup(func() { ssrfValidateEndpoint = origValidate })
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})

	newCleanupTransportDir(t)

	// Two campfires on two separate server A instances.
	campfireID1, tsURL1 := setupServerAWithCampfire(t)
	campfireID2, tsURL2 := setupServerAWithCampfire(t)

	// Server B: single instance joins both campfires concurrently.
	srvB, _ := newTestServerWithStore(t)
	doInit(t, srvB)

	var ready sync.WaitGroup
	var startGate sync.WaitGroup
	ready.Add(2)
	startGate.Add(1)

	type result struct {
		err *rpcError
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		ready.Done()
		startGate.Wait()
		args := `{"name":"campfire_join","arguments":{"campfire_id":"` + campfireID1 + `","peer_endpoint":"` + tsURL1 + `"}}`
		resp := srvB.dispatch(makeReq("tools/call", args))
		results[0] = result{err: resp.Error}
	}()

	go func() {
		defer wg.Done()
		ready.Done()
		startGate.Wait()
		args := `{"name":"campfire_join","arguments":{"campfire_id":"` + campfireID2 + `","peer_endpoint":"` + tsURL2 + `"}}`
		resp := srvB.dispatch(makeReq("tools/call", args))
		results[1] = result{err: resp.Error}
	}()

	ready.Wait()
	startGate.Done()
	wg.Wait()

	if results[0].err != nil {
		t.Errorf("join for campfire1 failed: code=%d msg=%s", results[0].err.Code, results[0].err.Message)
	}
	if results[1].err != nil {
		t.Errorf("join for campfire2 failed: code=%d msg=%s", results[1].err.Code, results[1].err.Message)
	}
}
