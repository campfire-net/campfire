package main

import (
	"encoding/json"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

// addHTTPMembership inserts a p2p-http membership into the store.
func addHTTPMembership(t *testing.T, st store.Store, campfireID string) {
	t.Helper()
	err := st.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  "http://localhost:9999",
		TransportType: "p2p-http",
		JoinProtocol:  "open",
		Role:          "full",
		JoinedAt:      1,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

// addFSMembership inserts a filesystem membership into the store.
func addFSMembership(t *testing.T, st store.Store, campfireID, dir string) {
	t.Helper()
	err := st.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  dir,
		TransportType: "filesystem",
		JoinProtocol:  "open",
		Role:          "full",
		JoinedAt:      1,
	})
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

// extractPeerResult parses a JSON-RPC tool response into a map.
func extractPeerResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshaling result: %v", err)
	}
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("cannot extract content: %s", string(b))
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &parsed); err != nil {
		t.Fatalf("parsing JSON: %v — raw: %s", err, outer.Content[0].Text)
	}
	return parsed
}

func TestMCPAddPeer(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	campfireID := "test-campfire-add-peer"
	addHTTPMembership(t, st, campfireID)

	resp := srv.handleAddPeer("req-1", map[string]interface{}{
		"campfire_id":    campfireID,
		"endpoint":       "https://peer1.example.com/campfire",
		"public_key_hex": "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
	})

	result := extractPeerResult(t, resp)
	if result["status"] != "ok" {
		t.Errorf("expected status ok, got %v", result["status"])
	}
	if result["endpoint"] != "https://peer1.example.com/campfire" {
		t.Errorf("expected endpoint in response, got %v", result["endpoint"])
	}
}

func TestMCPRemovePeer(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	campfireID := "test-campfire-remove-peer"
	pubkey := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	addHTTPMembership(t, st, campfireID)

	// Add a peer first.
	resp := srv.handleAddPeer("req-1", map[string]interface{}{
		"campfire_id":    campfireID,
		"endpoint":       "https://peer1.example.com/campfire",
		"public_key_hex": pubkey,
	})
	if resp.Error != nil {
		t.Fatalf("add peer failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	// Remove it.
	resp = srv.handleRemovePeer("req-2", map[string]interface{}{
		"campfire_id":    campfireID,
		"public_key_hex": pubkey,
	})
	if resp.Error != nil {
		t.Fatalf("remove peer failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	result := extractPeerResult(t, resp)
	if result["status"] != "ok" {
		t.Errorf("expected status ok, got %v", result["status"])
	}

	// Verify peer is gone by listing.
	resp = srv.handlePeers("req-3", map[string]interface{}{
		"campfire_id": campfireID,
	})
	if resp.Error != nil {
		t.Fatalf("list peers failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	peersResult := extractPeerResult(t, resp)
	count, _ := peersResult["count"].(float64)
	if count != 0 {
		t.Errorf("expected 0 peers after removal, got %v", count)
	}
}

func TestMCPPeers(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	campfireID := "test-campfire-list-peers"
	addHTTPMembership(t, st, campfireID)

	// Add two peers.
	for _, p := range []struct {
		endpoint string
		pubkey   string
	}{
		{"https://peer1.example.com/campfire", "aaaa1234567890abcdef1234567890abcdef1234567890abcdef1234567890aa"},
		{"https://peer2.example.com/campfire", "bbbb1234567890abcdef1234567890abcdef1234567890abcdef1234567890bb"},
	} {
		resp := srv.handleAddPeer("req-add", map[string]interface{}{
			"campfire_id":    campfireID,
			"endpoint":       p.endpoint,
			"public_key_hex": p.pubkey,
		})
		if resp.Error != nil {
			t.Fatalf("add peer failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
		}
	}

	// List peers.
	resp := srv.handlePeers("req-list", map[string]interface{}{
		"campfire_id": campfireID,
	})
	result := extractPeerResult(t, resp)
	count, _ := result["count"].(float64)
	if count != 2 {
		t.Errorf("expected 2 peers, got %v", count)
	}
}

func TestMCPPeerNonHTTP(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	campfireID := "test-campfire-fs-peer"
	addFSMembership(t, st, campfireID, t.TempDir())

	// AddPeer should fail on filesystem transport.
	resp := srv.handleAddPeer("req-1", map[string]interface{}{
		"campfire_id":    campfireID,
		"endpoint":       "https://peer1.example.com/campfire",
		"public_key_hex": "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
	})
	if resp.Error == nil {
		t.Fatal("expected error for non-HTTP campfire, got success")
	}

	// Peers should also fail.
	resp = srv.handlePeers("req-2", map[string]interface{}{
		"campfire_id": campfireID,
	})
	if resp.Error == nil {
		t.Fatal("expected error for non-HTTP campfire peers, got success")
	}

	// RemovePeer should fail.
	resp = srv.handleRemovePeer("req-3", map[string]interface{}{
		"campfire_id":    campfireID,
		"public_key_hex": "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
	})
	if resp.Error == nil {
		t.Fatal("expected error for non-HTTP campfire remove peer, got success")
	}
}

func TestMCPAddPeerMissingParams(t *testing.T) {
	srv, _ := newTestServerWithStore(t)

	// Missing campfire_id.
	resp := srv.handleAddPeer("req-1", map[string]interface{}{
		"endpoint":       "https://peer.example.com",
		"public_key_hex": "abc123",
	})
	if resp.Error == nil {
		t.Error("expected error for missing campfire_id")
	}

	// Missing endpoint.
	resp = srv.handleAddPeer("req-2", map[string]interface{}{
		"campfire_id":    "some-id",
		"public_key_hex": "abc123",
	})
	if resp.Error == nil {
		t.Error("expected error for missing endpoint")
	}

	// Missing public_key_hex.
	resp = srv.handleAddPeer("req-3", map[string]interface{}{
		"campfire_id": "some-id",
		"endpoint":    "https://peer.example.com",
	})
	if resp.Error == nil {
		t.Error("expected error for missing public_key_hex")
	}
}
