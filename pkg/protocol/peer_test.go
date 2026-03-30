package protocol_test

import (
	"errors"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// helperInitClient creates a real protocol.Client backed by a temp dir.
func helperInitClient(t *testing.T) *protocol.Client {
	t.Helper()
	client, err := protocol.Init(t.TempDir())
	if err != nil {
		t.Fatalf("protocol.Init: %v", err)
	}
	return client
}

// helperStoreAndClient opens a store and creates a Client for direct store manipulation in tests.
func helperStoreAndClient(t *testing.T) (store.Store, *protocol.Client) {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	return s, protocol.New(s, id)
}

// TestAddPeerNoHTTPTransport verifies that AddPeer returns ErrTransportNotSupported
// when the campfire uses a filesystem transport.
func TestAddPeerNoHTTPTransport(t *testing.T) {
	s, client := helperStoreAndClient(t)

	campfireID := "test-campfire-fs-addpeer"
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  t.TempDir(),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	err := client.AddPeer(campfireID, protocol.PeerInfo{
		Endpoint:     "http://peer:8080",
		PublicKeyHex: "aabbccdd",
	})
	if !errors.Is(err, protocol.ErrTransportNotSupported) {
		t.Errorf("expected ErrTransportNotSupported, got: %v", err)
	}
}

// TestPeersNoHTTPTransport verifies that Peers returns ErrTransportNotSupported
// when the campfire uses a filesystem transport.
func TestPeersNoHTTPTransport(t *testing.T) {
	s, client := helperStoreAndClient(t)

	campfireID := "test-campfire-fs-peers"
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  t.TempDir(),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	_, err := client.Peers(campfireID)
	if !errors.Is(err, protocol.ErrTransportNotSupported) {
		t.Errorf("expected ErrTransportNotSupported, got: %v", err)
	}
}

// TestRemovePeerNoHTTPTransport verifies that RemovePeer returns ErrTransportNotSupported
// when the campfire uses a filesystem transport.
func TestRemovePeerNoHTTPTransport(t *testing.T) {
	s, client := helperStoreAndClient(t)

	campfireID := "test-campfire-fs-removepeer"
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  t.TempDir(),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	err := client.RemovePeer(campfireID, "aabbccdd")
	if !errors.Is(err, protocol.ErrTransportNotSupported) {
		t.Errorf("expected ErrTransportNotSupported, got: %v", err)
	}
}

// TestAddPeerRemovePeerWithHTTPTransport tests the full lifecycle:
// add a peer, list it, remove it, confirm it's gone.
func TestAddPeerRemovePeerWithHTTPTransport(t *testing.T) {
	s, client := helperStoreAndClient(t)

	campfireID := "test-campfire-http-lifecycle"
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  t.TempDir(),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Add a peer.
	peer := protocol.PeerInfo{
		Endpoint:      "http://peer-a:8080",
		PublicKeyHex:  "aabbccddeeff0011",
		ParticipantID: "1",
	}
	if err := client.AddPeer(campfireID, peer); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	// List peers — should contain the one we added.
	peers, err := client.Peers(campfireID)
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].PublicKeyHex != peer.PublicKeyHex {
		t.Errorf("expected pubkey %s, got %s", peer.PublicKeyHex, peers[0].PublicKeyHex)
	}
	if peers[0].Endpoint != peer.Endpoint {
		t.Errorf("expected endpoint %s, got %s", peer.Endpoint, peers[0].Endpoint)
	}
	if peers[0].ParticipantID != "1" {
		t.Errorf("expected participantID '1', got %q", peers[0].ParticipantID)
	}

	// Add a second peer.
	peer2 := protocol.PeerInfo{
		Endpoint:     "http://peer-b:8080",
		PublicKeyHex: "1122334455667788",
	}
	if err := client.AddPeer(campfireID, peer2); err != nil {
		t.Fatalf("AddPeer (second): %v", err)
	}

	peers, err = client.Peers(campfireID)
	if err != nil {
		t.Fatalf("Peers after second add: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	// Remove the first peer.
	if err := client.RemovePeer(campfireID, peer.PublicKeyHex); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	peers, err = client.Peers(campfireID)
	if err != nil {
		t.Fatalf("Peers after remove: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after remove, got %d", len(peers))
	}
	if peers[0].PublicKeyHex != peer2.PublicKeyHex {
		t.Errorf("expected remaining peer %s, got %s", peer2.PublicKeyHex, peers[0].PublicKeyHex)
	}
}

// TestAddPeerValidation tests input validation.
func TestAddPeerValidation(t *testing.T) {
	client := helperInitClient(t)

	// Empty campfire ID.
	err := client.AddPeer("", protocol.PeerInfo{PublicKeyHex: "aa", Endpoint: "http://x"})
	if err == nil {
		t.Error("expected error for empty campfire ID")
	}

	// Empty pubkey.
	err = client.AddPeer("some-id", protocol.PeerInfo{Endpoint: "http://x"})
	if err == nil {
		t.Error("expected error for empty pubkey")
	}

	// Empty endpoint.
	err = client.AddPeer("some-id", protocol.PeerInfo{PublicKeyHex: "aa"})
	if err == nil {
		t.Error("expected error for empty endpoint")
	}
}

// TestPeersNotMember verifies that Peers returns an error when the client
// is not a member of the campfire.
func TestPeersNotMember(t *testing.T) {
	client := helperInitClient(t)

	_, err := client.Peers("nonexistent-campfire-id")
	if err == nil {
		t.Error("expected error for non-member campfire")
	}
}
