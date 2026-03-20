package http_test

// Tests for workspace-4jj: handleJoin with no EphemeralX25519Pub.
//
// The handler rejects a join request with no EphemeralX25519Pub with 400.
// Without the ephemeral key there is no ECDH exchange, so no key material
// can be delivered. This file verifies two properties:
//
//  (a) The server returns 400 when EphemeralX25519Pub is absent — confirmed
//      by TestJoinMissingEphemeralKeyRejected in join_peer_disclosure_test.go.
//
//  (b) The Join() client correctly handles a server response that contains
//      CampfirePubKey and Peers but no ResponderX25519Pub and no EncryptedPrivKey
//      (i.e. the server chose not to deliver key material). The client must
//      return a JoinResult with CampfirePrivKey == nil and no error — not a
//      decryption failure or a panic.
//
// NOTE on property (b): TestJoinClientHandlesNoKeyInResponse is a CLIENT ROBUSTNESS
// test, not a current-protocol flow test. The current server (handleJoin) cannot
// produce this response: it always requires EphemeralX25519Pub and always delivers
// key material on 200. The test uses a mock server to simulate a server response
// the real server does not produce. Its purpose is to guard the defensive "no key
// material" branch in Join() against silent removal during refactoring. If that
// branch is intentionally removed from Join(), delete this test at the same time.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestJoinClientHandlesNoKeyInResponse is a CLIENT ROBUSTNESS test.
//
// It verifies that the Join() client function handles a server response that
// provides CampfirePubKey + Peers but omits both ResponderX25519Pub and
// EncryptedPrivKey. This server response cannot be produced by the current
// handleJoin implementation (which always rejects requests missing
// EphemeralX25519Pub with 400, and always sends key material on 200).
//
// The test uses a mock HTTP server to simulate this hypothetical response.
// Its purpose is a forward-compatibility regression guard: if the defensive
// "no key material" branch in Join() (peer.go) is removed or broken, this
// test will catch it. The client must not panic or return an error — it must
// surface the absence via CampfirePrivKey==nil so callers can detect it.
func TestJoinClientHandlesNoKeyInResponse(t *testing.T) {
	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	// Use a fixed campfire pub key hex (32 bytes = 64 hex chars).
	fakeCampfirePubHex := fmt.Sprintf("%064x", 1) // "000...001"

	// Minimal server: accepts any POST, returns a JoinResponse with
	// CampfirePubKey and a single peer, but NO key material.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		resp := cfhttp.JoinResponse{
			CampfirePubKey: fakeCampfirePubHex,
			JoinProtocol:   "open",
			Threshold:      1,
			Peers: []cfhttp.PeerEntry{
				{PubKeyHex: "aabbcc", Endpoint: "http://127.0.0.1:9999"},
			},
			// ResponderX25519Pub intentionally absent — no ECDH.
			// EncryptedPrivKey intentionally absent — no key material.
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	// The campfire ID must be a valid hex-encoded Ed25519 public key (64 chars).
	campfireID := fakeCampfirePubHex

	result, err := cfhttp.Join(srv.URL, campfireID, joiner, "")
	if err != nil {
		t.Fatalf("Join() returned error for metadata-only response: %v", err)
	}

	// The client must surface the absence of key material via nil CampfirePrivKey.
	if result.CampfirePrivKey != nil {
		t.Errorf("expected CampfirePrivKey to be nil when server sends no key material, got %x", result.CampfirePrivKey)
	}

	// The campfire public key must be decoded correctly.
	if fmt.Sprintf("%x", result.CampfirePubKey) != fakeCampfirePubHex {
		t.Errorf("CampfirePubKey mismatch: got %x, want %s", result.CampfirePubKey, fakeCampfirePubHex)
	}

	// Peers must be populated from the response.
	if len(result.Peers) != 1 {
		t.Errorf("expected 1 peer in result, got %d", len(result.Peers))
	}
}

// TestJoinClientReturnsErrorOnServerReject verifies that the Join() client
// propagates a non-200 response from the server as an error.
//
// This covers the case where the server rejects a malformed join request (e.g.
// missing EphemeralX25519Pub) and the client correctly surfaces it as an error
// rather than silently returning an empty result.
func TestJoinClientReturnsErrorOnServerReject(t *testing.T) {
	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	// Server that always rejects with 400 (mirrors handleJoin when ephemeral key missing).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "ephemeral_x25519_pub is required", http.StatusBadRequest)
	}))
	defer srv.Close()

	campfireID := fmt.Sprintf("%064x", 2)

	_, err = cfhttp.Join(srv.URL, campfireID, joiner, "")
	if err == nil {
		t.Fatal("Join() must return an error when server responds with 400")
	}
}
