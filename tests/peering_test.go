// Package tests — E2E peering tests.
//
// Tests exercise the peering SDK surface (AddPeer, RemovePeer, Peers)
// through real stores — no mocks.
//
//   - TestPeeringNonHTTPError: filesystem transport returns ErrTransportNotSupported
//   - TestPeeringStoreLifecycle: full add/list/remove cycle on p2p-http membership
//   - TestPeeringValidation: input validation for empty/invalid args
package tests

import (
	"errors"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// peeringEnv holds a store + client pair for peering tests.
type peeringEnv struct {
	store  store.Store
	client *protocol.Client
	id     *identity.Identity
}

// newPeeringEnv creates a real store-backed client.
func newPeeringEnv(t *testing.T) *peeringEnv {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	return &peeringEnv{
		store:  s,
		client: protocol.New(s, id),
		id:     id,
	}
}

// addMembership is a helper that inserts a membership record with the given transport type.
func (e *peeringEnv) addMembership(t *testing.T, campfireID, transportType string) {
	t.Helper()
	if err := e.store.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  t.TempDir(),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		TransportType: transportType,
	}); err != nil {
		t.Fatalf("AddMembership(%s, %s): %v", campfireID, transportType, err)
	}
}

// ---------- TestPeeringNonHTTPError ----------

// TestPeeringNonHTTPError verifies that all peer operations return
// ErrTransportNotSupported when the campfire uses a filesystem transport.
func TestPeeringNonHTTPError(t *testing.T) {
	env := newPeeringEnv(t)
	campfireID := "e2e-peering-fs-nohttperror"
	env.addMembership(t, campfireID, "filesystem")

	t.Run("AddPeer", func(t *testing.T) {
		err := env.client.AddPeer(campfireID, protocol.PeerInfo{
			Endpoint:     "http://localhost:8080",
			PublicKeyHex: "aabbccdd",
		})
		if !errors.Is(err, protocol.ErrTransportNotSupported) {
			t.Errorf("AddPeer: expected ErrTransportNotSupported, got: %v", err)
		}
	})

	t.Run("RemovePeer", func(t *testing.T) {
		err := env.client.RemovePeer(campfireID, "aabbccdd")
		if !errors.Is(err, protocol.ErrTransportNotSupported) {
			t.Errorf("RemovePeer: expected ErrTransportNotSupported, got: %v", err)
		}
	})

	t.Run("Peers", func(t *testing.T) {
		_, err := env.client.Peers(campfireID)
		if !errors.Is(err, protocol.ErrTransportNotSupported) {
			t.Errorf("Peers: expected ErrTransportNotSupported, got: %v", err)
		}
	})
}

// ---------- TestPeeringStoreLifecycle ----------

// TestPeeringStoreLifecycle exercises the full peer add/list/remove cycle
// on a p2p-http membership backed by a real store.
func TestPeeringStoreLifecycle(t *testing.T) {
	env := newPeeringEnv(t)
	campfireID := "e2e-peering-http-lifecycle"
	env.addMembership(t, campfireID, "p2p-http")

	// Initially no peers.
	peers, err := env.client.Peers(campfireID)
	if err != nil {
		t.Fatalf("Peers (initial): %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers initially, got %d", len(peers))
	}

	// Add first peer.
	peer1 := protocol.PeerInfo{
		Endpoint:      "http://peer-alpha:9090",
		PublicKeyHex:  "1111111111111111",
		ParticipantID: "1",
	}
	if err := env.client.AddPeer(campfireID, peer1); err != nil {
		t.Fatalf("AddPeer(peer1): %v", err)
	}

	peers, err = env.client.Peers(campfireID)
	if err != nil {
		t.Fatalf("Peers after first add: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].Endpoint != peer1.Endpoint {
		t.Errorf("peer1 endpoint: want %s, got %s", peer1.Endpoint, peers[0].Endpoint)
	}
	if peers[0].PublicKeyHex != peer1.PublicKeyHex {
		t.Errorf("peer1 pubkey: want %s, got %s", peer1.PublicKeyHex, peers[0].PublicKeyHex)
	}
	if peers[0].ParticipantID != "1" {
		t.Errorf("peer1 participantID: want '1', got %q", peers[0].ParticipantID)
	}

	// Add second peer.
	peer2 := protocol.PeerInfo{
		Endpoint:     "http://peer-beta:9091",
		PublicKeyHex: "2222222222222222",
	}
	if err := env.client.AddPeer(campfireID, peer2); err != nil {
		t.Fatalf("AddPeer(peer2): %v", err)
	}

	peers, err = env.client.Peers(campfireID)
	if err != nil {
		t.Fatalf("Peers after second add: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	// Remove first peer.
	if err := env.client.RemovePeer(campfireID, peer1.PublicKeyHex); err != nil {
		t.Fatalf("RemovePeer(peer1): %v", err)
	}

	peers, err = env.client.Peers(campfireID)
	if err != nil {
		t.Fatalf("Peers after remove peer1: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after remove, got %d", len(peers))
	}
	if peers[0].PublicKeyHex != peer2.PublicKeyHex {
		t.Errorf("remaining peer: want %s, got %s", peer2.PublicKeyHex, peers[0].PublicKeyHex)
	}

	// Remove second peer — back to empty.
	if err := env.client.RemovePeer(campfireID, peer2.PublicKeyHex); err != nil {
		t.Fatalf("RemovePeer(peer2): %v", err)
	}

	peers, err = env.client.Peers(campfireID)
	if err != nil {
		t.Fatalf("Peers after remove all: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers after removing all, got %d", len(peers))
	}
}

// TestPeeringStoreLifecycle_Upsert verifies that adding a peer with the same
// public key updates the existing record rather than creating a duplicate.
func TestPeeringStoreLifecycle_Upsert(t *testing.T) {
	env := newPeeringEnv(t)
	campfireID := "e2e-peering-http-upsert"
	env.addMembership(t, campfireID, "p2p-http")

	peer := protocol.PeerInfo{
		Endpoint:     "http://old-endpoint:9090",
		PublicKeyHex: "aaaa1111bbbb2222",
	}
	if err := env.client.AddPeer(campfireID, peer); err != nil {
		t.Fatalf("AddPeer (initial): %v", err)
	}

	// Upsert with new endpoint.
	peer.Endpoint = "http://new-endpoint:9091"
	if err := env.client.AddPeer(campfireID, peer); err != nil {
		t.Fatalf("AddPeer (upsert): %v", err)
	}

	peers, err := env.client.Peers(campfireID)
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after upsert, got %d", len(peers))
	}
	if peers[0].Endpoint != "http://new-endpoint:9091" {
		t.Errorf("endpoint after upsert: want http://new-endpoint:9091, got %s", peers[0].Endpoint)
	}
}

// ---------- TestPeeringValidation ----------

// TestPeeringValidation checks input validation for all peer operations.
func TestPeeringValidation(t *testing.T) {
	env := newPeeringEnv(t)
	campfireID := "e2e-peering-http-validation"
	env.addMembership(t, campfireID, "p2p-http")

	t.Run("AddPeer_EmptyEndpoint", func(t *testing.T) {
		err := env.client.AddPeer(campfireID, protocol.PeerInfo{
			PublicKeyHex: "aabb",
		})
		if err == nil {
			t.Error("expected error for empty endpoint")
		}
	})

	t.Run("AddPeer_EmptyPubkey", func(t *testing.T) {
		err := env.client.AddPeer(campfireID, protocol.PeerInfo{
			Endpoint: "http://x:8080",
		})
		if err == nil {
			t.Error("expected error for empty pubkey")
		}
	})

	t.Run("AddPeer_EmptyCampfireID", func(t *testing.T) {
		err := env.client.AddPeer("", protocol.PeerInfo{
			Endpoint:     "http://x:8080",
			PublicKeyHex: "aabb",
		})
		if err == nil {
			t.Error("expected error for empty campfire ID")
		}
	})

	t.Run("RemovePeer_EmptyCampfireID", func(t *testing.T) {
		err := env.client.RemovePeer("", "aabb")
		if err == nil {
			t.Error("expected error for empty campfire ID")
		}
	})

	t.Run("RemovePeer_EmptyPubkey", func(t *testing.T) {
		err := env.client.RemovePeer(campfireID, "")
		if err == nil {
			t.Error("expected error for empty pubkey")
		}
	})

	t.Run("Peers_EmptyCampfireID", func(t *testing.T) {
		_, err := env.client.Peers("")
		if err == nil {
			t.Error("expected error for empty campfire ID")
		}
	})

	t.Run("RemovePeer_NonExistent", func(t *testing.T) {
		// Removing a peer that was never added should not error (idempotent delete).
		err := env.client.RemovePeer(campfireID, "nonexistent-pubkey-hex")
		if err != nil {
			t.Errorf("RemovePeer for non-existent peer should be idempotent, got: %v", err)
		}
	})

	t.Run("Peers_NotMember", func(t *testing.T) {
		_, err := env.client.Peers("nonexistent-campfire-not-a-member")
		if err == nil {
			t.Error("expected error for non-member campfire")
		}
	})
}
