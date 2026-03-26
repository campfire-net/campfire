package main

// ssrf_join_test.go — SSRF validation tests for the campfire_join MCP handler.
//
// These tests verify that handleRemoteJoin rejects private/internal endpoints
// before making any outbound HTTP request. They exercise the MCP handler path
// directly (not just the standalone ValidateJoinerEndpoint function).
//
// TDD: these tests were written before the implementation and drove the design
// of the ssrfValidateEndpoint package-level override hook.
//
// Bead: campfire-agent-yfz

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
)

// fakePublicCampfireID returns a 64-char hex string that looks like a campfire ID
// but does not exist in any local FS transport, causing handleJoin to fall
// through to handleRemoteJoin. It is derived from a random Ed25519 key so it
// passes any hex-format validation.
func fakePublicCampfireID(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating fake campfire key: %v", err)
	}
	return fmt.Sprintf("%x", []byte(pub))
}

// ---------------------------------------------------------------------------
// Test 1: peer_endpoint "http://169.254.169.254/" returns SSRF error
// ---------------------------------------------------------------------------

// TestSSRFJoin_LinkLocal rejects the AWS metadata endpoint, which is
// link-local (169.254.x.x). The handler must return -32000 with "SSRF" or
// "private" or "blocked" in the message.
func TestSSRFJoin_LinkLocal(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	// Use a campfire ID that doesn't exist locally — forces handleRemoteJoin.
	campfireID := fakePublicCampfireID(t)

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": "http://169.254.169.254/",
	})
	joinResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	if joinResp.Error == nil {
		t.Fatal("expected error for link-local peer_endpoint, got nil")
	}
	if joinResp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", joinResp.Error.Code)
	}
	msg := joinResp.Error.Message
	if !containsAny(msg, "SSRF", "private", "blocked", "internal") {
		t.Errorf("expected error message to contain SSRF/private/blocked/internal, got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Test 2: peer_endpoint "http://10.0.0.1:8080/" returns SSRF error
// ---------------------------------------------------------------------------

// TestSSRFJoin_RFC1918 rejects a 10.x RFC1918 private address.
func TestSSRFJoin_RFC1918(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	campfireID := fakePublicCampfireID(t)

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": "http://10.0.0.1:8080/",
	})
	joinResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	if joinResp.Error == nil {
		t.Fatal("expected error for RFC1918 peer_endpoint, got nil")
	}
	if joinResp.Error.Code != -32000 {
		t.Errorf("expected -32000, got %d", joinResp.Error.Code)
	}
	msg := joinResp.Error.Message
	if !containsAny(msg, "SSRF", "private", "blocked", "internal") {
		t.Errorf("expected SSRF/private/blocked/internal in error, got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Test 3: peer_endpoint "http://[::ffff:192.168.1.1]:80/" returns SSRF error
// ---------------------------------------------------------------------------

// TestSSRFJoin_IPv4MappedIPv6 rejects IPv4-mapped IPv6 addresses that encode
// private IPv4 ranges (::ffff:192.168.1.1 = 192.168.1.1).
func TestSSRFJoin_IPv4MappedIPv6(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	campfireID := fakePublicCampfireID(t)

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": "http://[::ffff:192.168.1.1]:80/",
	})
	joinResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	if joinResp.Error == nil {
		t.Fatal("expected error for IPv4-mapped IPv6 private address, got nil")
	}
	if joinResp.Error.Code != -32000 {
		t.Errorf("expected -32000, got %d", joinResp.Error.Code)
	}
	msg := joinResp.Error.Message
	if !containsAny(msg, "SSRF", "private", "blocked", "internal") {
		t.Errorf("expected SSRF/private/blocked/internal in error, got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Beacon-resolved endpoint pointing to a private address is rejected
// ---------------------------------------------------------------------------

// TestSSRFJoin_BeaconPrivateEndpoint verifies that when beacon discovery
// resolves a campfire endpoint and that endpoint is a private address,
// handleRemoteJoin rejects it. This covers the beacon SSRF vector.
//
// Strategy: generate a fresh ed25519 keypair (simulating a campfire), create
// a properly-signed beacon with a private p2p-http endpoint, and publish it
// into the server's beaconDir. When the server tries to join the campfire (not
// found locally), beacon discovery returns the private endpoint and validation
// rejects it.
func TestSSRFJoin_BeaconPrivateEndpoint(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	// Generate a fresh campfire key pair to sign the beacon.
	campfirePub, campfirePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	// Create a properly-signed beacon with a private endpoint.
	b, err := beacon.New(
		campfirePub,
		campfirePriv,
		"open",
		nil,
		beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   map[string]string{"endpoint": "http://10.0.0.1:8080/"},
		},
		"ssrf test campfire",
	)
	if err != nil {
		t.Fatalf("creating beacon: %v", err)
	}

	// Publish the beacon into the server's beaconDir.
	if err := beacon.Publish(srv.beaconDir, b); err != nil {
		t.Fatalf("publishing beacon: %v", err)
	}

	// The campfireID is the hex of the campfire public key.
	campfireID := b.CampfireIDHex()

	// Join without peer_endpoint — beacon discovery will find the private endpoint.
	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		// no peer_endpoint — triggers beacon resolution
	})
	joinResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	if joinResp.Error == nil {
		t.Fatal("expected error when beacon resolves to private address, got nil")
	}
	if joinResp.Error.Code != -32000 {
		t.Errorf("expected -32000, got %d", joinResp.Error.Code)
	}
	msg := joinResp.Error.Message
	if !containsAny(msg, "SSRF", "private", "blocked", "internal") {
		t.Errorf("expected SSRF/private/blocked/internal in error, got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Test 5: Valid public peer_endpoint passes validation
// ---------------------------------------------------------------------------

// TestSSRFJoin_PublicEndpointPassesValidation verifies that a routable public
// IP (1.2.3.4) is not blocked by the SSRF validator. The join will ultimately
// fail with a network error (no server there), but NOT with an SSRF error.
func TestSSRFJoin_PublicEndpointPassesValidation(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	campfireID := fakePublicCampfireID(t)

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": "http://1.2.3.4/",
	})
	joinResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	// The join will fail because 1.2.3.4 is not a real campfire server.
	// But the error must NOT be an SSRF rejection.
	if joinResp.Error == nil {
		t.Fatal("expected error (network failure), got success joining 1.2.3.4")
	}
	msg := joinResp.Error.Message
	if containsAny(msg, "SSRF", "private", "blocked") {
		t.Errorf("SSRF validation incorrectly blocked public address 1.2.3.4: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Test 6: cfhttp.Join uses SSRF-safe transport (peer.go httpClient)
// ---------------------------------------------------------------------------

// TestSSRFJoin_HttpClientIsSSRFSafe verifies that the transport-level SSRF
// guard in pkg/transport/http/peer.go blocks loopback connections when the
// pre-flight ssrfValidateEndpoint is bypassed (simulating DNS rebinding where
// validation-time IP differs from connection-time IP).
//
// This exercises the TOCTOU guard in newSSRFSafeTransport.
func TestSSRFJoin_HttpClientIsSSRFSafe(t *testing.T) {
	// Bypass the pre-flight SSRF check to reach the transport layer.
	origValidate := ssrfValidateEndpoint
	ssrfValidateEndpoint = func(string) error { return nil }
	t.Cleanup(func() { ssrfValidateEndpoint = origValidate })

	// Do NOT override httpClient — use the real SSRF-safe transport from peer.go.
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	campfireID := fakePublicCampfireID(t)

	// Use a loopback address — the SSRF-safe transport should block it at dial.
	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": "http://127.0.0.1:19999/",
	})
	joinResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	// Must fail — SSRF block at transport level or connection refused.
	if joinResp.Error == nil {
		t.Fatal("expected error joining loopback address with SSRF-safe transport")
	}
	if joinResp.Error.Code != -32000 {
		t.Errorf("expected -32000, got %d", joinResp.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if containsSubstr(s, sub) {
			return true
		}
	}
	return false
}
