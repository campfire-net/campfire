package main

// ssrf_peer_test.go — SSRF validation tests for peer endpoints returned by a
// remote server in the JoinResult.Peers list (handleRemoteJoin peer loop).
//
// TDD: these tests were written before the implementation to drive the fix for
// the defense-in-depth gap where result.Peers was stored without SSRF
// pre-validation.
//
// These tests use cfhttpJoin (package-level var) to inject a fake JoinResult
// without making any real network connections. The real ssrfValidateEndpoint
// is exercised for peer-list entries in all tests.
//
// Bead: campfire-agent-e8a

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// fakePeerPubKeyHex returns a hex-encoded ed25519 public key for use as a
// fake peer PubKeyHex in test JoinResult.Peers entries.
func fakePeerPubKeyHex(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating peer key: %v", err)
	}
	return hex.EncodeToString(pub)
}

// ---------------------------------------------------------------------------
// Test 1: private peer endpoint in JoinResult.Peers is NOT stored
// ---------------------------------------------------------------------------

// TestSSRFPeer_PrivateEndpointNotStored verifies that when a remote server
// returns a Peers list containing a private-range endpoint (10.0.0.1),
// handleRemoteJoin does NOT store that endpoint in the local peer store.
//
// The primary peer_endpoint "http://1.2.3.4/" is publicly routable and passes
// real ssrfValidateEndpoint validation (no bypass needed).
// The peer-list entry "http://10.0.0.1:8080/" is private and must be rejected
// by the real ssrfValidateEndpoint in the peer loop.
func TestSSRFPeer_PrivateEndpointNotStored(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	doInit(t, srv)

	privatePeerEndpoint := "http://10.0.0.1:8080/"
	peerPubKey := fakePeerPubKeyHex(t)

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID := fmt.Sprintf("%x", []byte(pub))

	// Inject a mock cfhttpJoin that returns a Peers list with a private address.
	// The primary peer_endpoint "http://1.2.3.4/" passes real SSRF validation
	// so no override of ssrfValidateEndpoint is needed.
	origJoin := cfhttpJoin
	cfhttpJoin = func(peerEndpoint, cID string, id *identity.Identity, myEndpoint string) (*cfhttp.JoinResult, error) {
		return &cfhttp.JoinResult{
			CampfirePrivKey: priv,
			CampfirePubKey:  pub,
			JoinProtocol:    "open",
			Threshold:       1,
			Peers: []cfhttp.PeerEntry{
				{PubKeyHex: peerPubKey, Endpoint: privatePeerEndpoint},
			},
		}, nil
	}
	t.Cleanup(func() { cfhttpJoin = origJoin })

	// Use a public IP for the primary endpoint — passes real ssrfValidateEndpoint.
	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": "http://1.2.3.4/",
	})
	joinResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	if joinResp.Error != nil {
		t.Fatalf("join failed unexpectedly: code=%d msg=%s", joinResp.Error.Code, joinResp.Error.Message)
	}

	// Verify the private peer endpoint is NOT in the store.
	storedPeers, listErr := st.ListPeerEndpoints(campfireID)
	if listErr != nil {
		t.Fatalf("listing peer endpoints: %v", listErr)
	}
	for _, p := range storedPeers {
		if p.Endpoint == privatePeerEndpoint {
			t.Errorf("private peer endpoint %s was stored — SSRF peer-loop validation not applied", privatePeerEndpoint)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 2: public peer endpoint in JoinResult.Peers IS stored
// ---------------------------------------------------------------------------

// TestSSRFPeer_PublicEndpointStored verifies that a valid public peer endpoint
// returned in JoinResult.Peers is stored after a successful join.
//
// This is a regression test ensuring the SSRF check does not block legitimate
// public endpoints. 1.2.3.4 is publicly routable and passes ssrfValidateEndpoint.
func TestSSRFPeer_PublicEndpointStored(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	doInit(t, srv)

	// Use a publicly routable address that passes real ssrfValidateEndpoint.
	publicPeerEndpoint := "http://8.8.8.8:8080/"
	peerPubKey := fakePeerPubKeyHex(t)

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID := fmt.Sprintf("%x", []byte(pub))

	// Inject a mock cfhttpJoin that returns a Peers list with a public address.
	origJoin := cfhttpJoin
	cfhttpJoin = func(peerEndpoint, cID string, id *identity.Identity, myEndpoint string) (*cfhttp.JoinResult, error) {
		return &cfhttp.JoinResult{
			CampfirePrivKey: priv,
			CampfirePubKey:  pub,
			JoinProtocol:    "open",
			Threshold:       1,
			Peers: []cfhttp.PeerEntry{
				{PubKeyHex: peerPubKey, Endpoint: publicPeerEndpoint},
			},
		}, nil
	}
	t.Cleanup(func() { cfhttpJoin = origJoin })

	// Primary endpoint is also a public IP — passes real ssrfValidateEndpoint.
	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id":   campfireID,
		"peer_endpoint": "http://1.2.3.4/",
	})
	joinResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))

	if joinResp.Error != nil {
		t.Fatalf("join failed unexpectedly: code=%d msg=%s", joinResp.Error.Code, joinResp.Error.Message)
	}

	// Verify the public peer endpoint IS stored.
	storedPeers, listErr := st.ListPeerEndpoints(campfireID)
	if listErr != nil {
		t.Fatalf("listing peer endpoints: %v", listErr)
	}
	found := false
	for _, p := range storedPeers {
		if p.Endpoint == publicPeerEndpoint && p.MemberPubkey == peerPubKey {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("public peer endpoint %s not found in store after join; stored: %+v", publicPeerEndpoint, storedPeers)
	}
}
