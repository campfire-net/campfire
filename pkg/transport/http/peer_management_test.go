package http_test

import (
	"testing"

	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestAddPeerDeduplication verifies that calling AddPeer twice with the same
// pubKeyHex does not create a duplicate entry in the peer list.
func TestAddPeerDeduplication(t *testing.T) {
	s := tempStore(t)
	tr := cfhttp.New(":0", s)

	campfireID := "test-campfire-dedup"
	pubKey := "aabbccddeeff0011"
	endpoint := "http://peer1:8080"

	tr.AddPeer(campfireID, pubKey, endpoint)
	tr.AddPeer(campfireID, pubKey, endpoint) // duplicate

	peers := tr.Peers(campfireID)
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after duplicate AddPeer, got %d", len(peers))
	}
	if peers[0].PubKeyHex != pubKey {
		t.Errorf("expected PubKeyHex %q, got %q", pubKey, peers[0].PubKeyHex)
	}
}

// TestAddPeerDeduplicationDistinctKeys verifies that two distinct peers are
// both added (no false-positive dedup).
func TestAddPeerDeduplicationDistinctKeys(t *testing.T) {
	s := tempStore(t)
	tr := cfhttp.New(":0", s)

	campfireID := "test-campfire-distinct"
	tr.AddPeer(campfireID, "key-aaa", "http://peer-a:8080")
	tr.AddPeer(campfireID, "key-bbb", "http://peer-b:8080")

	peers := tr.Peers(campfireID)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers for distinct keys, got %d", len(peers))
	}
}

// TestRemovePeerEmptyList verifies that RemovePeer on an empty list is a no-op
// and does not panic.
func TestRemovePeerEmptyList(t *testing.T) {
	s := tempStore(t)
	tr := cfhttp.New(":0", s)

	campfireID := "test-campfire-empty"
	tr.RemovePeer(campfireID, "nonexistent-key") // must not panic

	peers := tr.Peers(campfireID)
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers after RemovePeer on empty list, got %d", len(peers))
	}
}

// TestRemovePeerNotFound verifies that RemovePeer is a no-op when the peer
// does not exist in a non-empty list.
func TestRemovePeerNotFound(t *testing.T) {
	s := tempStore(t)
	tr := cfhttp.New(":0", s)

	campfireID := "test-campfire-notfound"
	tr.AddPeer(campfireID, "key-existing", "http://peer:8080")

	tr.RemovePeer(campfireID, "key-absent") // no-op

	peers := tr.Peers(campfireID)
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer to remain, got %d", len(peers))
	}
}

// TestRemovePeerLeavesEmptySlice verifies that removing the last peer in a
// campfire leaves an empty (not nil-panicking) peer list.
func TestRemovePeerLeavesEmptySlice(t *testing.T) {
	s := tempStore(t)
	tr := cfhttp.New(":0", s)

	campfireID := "test-campfire-last"
	tr.AddPeer(campfireID, "only-peer", "http://solo:8080")
	tr.RemovePeer(campfireID, "only-peer")

	peers := tr.Peers(campfireID)
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers after removing last peer, got %d", len(peers))
	}
}

// TestRemovePeerMiddle verifies that removing a peer from the middle of the
// list preserves the remaining peers.
func TestRemovePeerMiddle(t *testing.T) {
	s := tempStore(t)
	tr := cfhttp.New(":0", s)

	campfireID := "test-campfire-middle"
	tr.AddPeer(campfireID, "key-a", "http://a:8080")
	tr.AddPeer(campfireID, "key-b", "http://b:8080")
	tr.AddPeer(campfireID, "key-c", "http://c:8080")

	tr.RemovePeer(campfireID, "key-b")

	peers := tr.Peers(campfireID)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers after removing middle peer, got %d", len(peers))
	}
	for _, p := range peers {
		if p.PubKeyHex == "key-b" {
			t.Errorf("removed peer key-b still present in list")
		}
	}
}
