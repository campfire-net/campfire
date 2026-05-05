package http_test

// ssrf_peer_test.go — SEC-HIGH regression tests for campfireagent-824.
//
// Before this fix, httpClient (used by Deliver, Sync, Join, NotifyMembership,
// SendRekeyPhase1, SendRekey, SendSignRound) had no SSRF-safe transport —
// only Poll() used newSSRFSafeTransport(). This created a DNS-rebinding window:
// an endpoint validated at join/admission time could be rebound to an internal
// address before the outbound connection was established.
//
// The fix installs newSSRFSafeTransport() as the Transport for httpClient, so all
// outbound peer-to-peer paths are protected at connection time, not just at
// endpoint validation time.
//
// These tests verify:
//  1. Deliver to a loopback server is blocked when SSRF-safe transport is active.
//  2. Sync to a loopback server is blocked when SSRF-safe transport is active.
//  3. Join to a loopback server is blocked when SSRF-safe transport is active.
//  4. Mutation guard: reverting to the default transport allows the loopback call to
//     succeed, proving the SSRF-safe transport is doing the blocking work.
//
// Happy-path tests for Deliver, Sync, and Join use the TestMain-overridden client
// (plain http.Client, no SSRF filtering) so they can reach loopback test servers.
// These tests restore the SSRF-safe client for the duration of the SSRF checks.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cfhttp "github.com/campfire-net/campfire/cf-protocol/internal/transport/http"
)

// loopbackServer starts a real httptest.Server on 127.0.0.1 and returns it.
// The server responds 200 OK to any request so we can distinguish a successful
// connection from a blocked one.
func loopbackServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// restoreSSRFSafeClient switches httpClient to the SSRF-safe transport for the
// duration of a test and restores the test client (plain, loopback-permitting) in
// t.Cleanup so that subsequent tests are not affected.
func restoreSSRFSafeClient(t *testing.T) {
	t.Helper()
	// TestMain installed an unprotected client; save it for cleanup.
	unprotected := &http.Client{Timeout: 30 * time.Second}
	cfhttp.RestoreSSRFSafeHTTPClientForTest()
	t.Cleanup(func() {
		cfhttp.OverrideHTTPClientForTest(unprotected)
	})
}

// TestDeliverSSRFBlocksLoopback verifies that Deliver() is blocked when the
// target endpoint resolves to a loopback address — the SSRF-safe transport on
// httpClient must reject the connection attempt.
func TestDeliverSSRFBlocksLoopback(t *testing.T) {
	ts := loopbackServer(t)
	restoreSSRFSafeClient(t)

	id := tempIdentity(t)
	msg := newTestMessage(t, id)

	// Point Deliver at the loopback server's URL.
	err := cfhttp.Deliver(ts.URL, "ssrf-deliver-test", msg, id)
	if err == nil {
		t.Fatal("Deliver to loopback server: expected SSRF block, got nil error")
	}
	// Verify the error originates from the SSRF guard (contains "private" or "blocked").
	errMsg := err.Error()
	if !containsAny(errMsg, "private", "blocked", "internal") {
		t.Errorf("Deliver SSRF block: error should mention private/blocked/internal address, got: %v", err)
	}
}

// TestSyncSSRFBlocksLoopback verifies that Sync() is blocked when the target
// endpoint resolves to a loopback address.
func TestSyncSSRFBlocksLoopback(t *testing.T) {
	ts := loopbackServer(t)
	restoreSSRFSafeClient(t)

	id := tempIdentity(t)

	_, err := cfhttp.Sync(ts.URL, "ssrf-sync-test", 0, id)
	if err == nil {
		t.Fatal("Sync to loopback server: expected SSRF block, got nil error")
	}
	errMsg := err.Error()
	if !containsAny(errMsg, "private", "blocked", "internal") {
		t.Errorf("Sync SSRF block: error should mention private/blocked/internal address, got: %v", err)
	}
}

// TestJoinSSRFBlocksLoopback verifies that Join() is blocked when the peer endpoint
// resolves to a loopback address.
func TestJoinSSRFBlocksLoopback(t *testing.T) {
	// Start a real server that returns a valid-looking join response so we can
	// distinguish "SSRF blocked before reaching server" from "server rejected us".
	// Under the SSRF-safe transport the connection should be rejected before any
	// HTTP request is sent, so the server handler is never invoked.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If the handler is reached, the SSRF guard did NOT block the request.
		// Return a recognizable payload so the test can detect this case.
		json.NewEncoder(w).Encode(map[string]string{"ssrf_guard": "bypassed"}) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	restoreSSRFSafeClient(t)

	id := tempIdentity(t)

	_, err := cfhttp.Join(ts.URL, "ssrf-join-test", id, "")
	if err == nil {
		t.Fatal("Join to loopback server: expected SSRF block, got nil error")
	}
	errMsg := err.Error()
	if !containsAny(errMsg, "private", "blocked", "internal") {
		t.Errorf("Join SSRF block: error should mention private/blocked/internal address, got: %v", err)
	}
}

// TestDeliverSSRFMutationCheck verifies that removing the SSRF-safe transport from
// httpClient allows a connection to a loopback server to succeed (or fail for a
// different reason — e.g., 404 because the server doesn't speak the campfire
// protocol — but NOT with an SSRF block). This is the mutation check: it proves
// the SSRF-safe transport is what blocks the connection in the tests above.
//
// The test intentionally installs an unprotected client, makes a request to a loopback
// server, and confirms that either:
//   - The request reaches the server (200-level or non-SSRF 4xx/5xx), or
//   - The connection fails for a non-SSRF reason (unexpected response format, etc.)
//
// The critical assertion: the error must NOT contain the SSRF block message.
func TestDeliverSSRFMutationCheck(t *testing.T) {
	ts := loopbackServer(t)

	// Install the unprotected client (mimicking the pre-fix state).
	unprotected := &http.Client{Timeout: 10 * time.Second}
	cfhttp.OverrideHTTPClientForTest(unprotected)
	t.Cleanup(func() {
		// Restore TestMain's unprotected client after the test.
		cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 30 * time.Second})
	})

	id := tempIdentity(t)
	msg := newTestMessage(t, id)

	err := cfhttp.Deliver(ts.URL, "mutation-check", msg, id)
	// With the unprotected client, the connection to loopback is permitted.
	// The server will return 200 OK (our stub returns 200 to any request),
	// but Deliver may still fail because the response body doesn't contain what
	// the handler expects.  What matters is that it does NOT fail with an SSRF block.
	if err != nil {
		errMsg := err.Error()
		if containsAny(errMsg, "blocked", "resolves to a private") {
			t.Errorf("mutation check FAILED: unprotected client still returned SSRF block message: %v", err)
			t.Logf("This means the SSRF block is coming from somewhere other than the transport.")
		} else {
			// Connection reached the server; error is from the campfire protocol layer —
			// that is expected (stub returns 200 with no campfire payload). Test passes.
			t.Logf("Mutation check: unprotected client reached loopback server; got non-SSRF error: %v", err)
		}
		return
	}
	// No error: Deliver succeeded — connection was allowed. Mutation check passes.
	t.Log("Mutation check: unprotected client successfully connected to loopback server (no SSRF block)")
}

// containsAny returns true if s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 {
			if len(s) >= len(sub) {
				for i := 0; i <= len(s)-len(sub); i++ {
					if s[i:i+len(sub)] == sub {
						return true
					}
				}
			}
		}
	}
	return false
}

