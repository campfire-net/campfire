package cmd

// Tests for workspace-com: evictThresholdN stores the creator in peer_endpoints
// when the creator's own pubkey appears in remainingPeers (e.g. creator endpoint
// was stored during original join). This results in duplicate participant ID
// entries (PID=1 in both threshold_shares and peer_endpoints), which causes
// FROST signing failures post-eviction.
//
// Fix: skip the creator in the UpsertPeerEndpoint loop, just like the delivery loop.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestEvictThresholdN_CreatorNotStoredInPeerEndpoints verifies that when the
// creator's pubkey appears in remainingPeers, the creator is NOT stored in
// peer_endpoints after eviction. The creator's share must only appear in
// threshold_shares with participant ID 1.
func TestEvictThresholdN_CreatorNotStoredInPeerEndpoints(t *testing.T) {
	stateDir := t.TempDir()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	// Generate a peer.
	peerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating peer identity: %v", err)
	}

	// Generate evicted peer identity.
	evictedID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating evicted identity: %v", err)
	}

	// Open store.
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Use a synthetic old campfire ID (just a hex string).
	oldCFID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating old CF ID: %v", err)
	}
	oldCampfireID := oldCFID.PublicKeyHex()

	// Build old campfire state (threshold=2, no single private key).
	oldCFState := &campfire.CampfireState{
		PublicKey:             oldCFID.PublicKey,
		PrivateKey:            nil,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		Threshold:             2,
	}
	oldStateData, err := cfencoding.Marshal(oldCFState)
	if err != nil {
		t.Fatalf("marshalling old campfire state: %v", err)
	}
	oldStateFile := filepath.Join(stateDir, oldCampfireID+".cbor")
	if err := os.WriteFile(oldStateFile, oldStateData, 0600); err != nil {
		t.Fatalf("writing old state file: %v", err)
	}

	// Add membership.
	if err := s.AddMembership(store.Membership{
		CampfireID:   oldCampfireID,
		TransportDir: stateDir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
		Threshold:    2,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Store a threshold share for the old campfire (creator = participant 1).
	// We need this so UpdateCampfireID can rename it.
	dummyShareData := []byte(`{"participant_id":1,"secret_share":"dGVzdA==","public_data":"dGVzdA=="}`)
	if err := s.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    oldCampfireID,
		ParticipantID: 1,
		SecretShare:   dummyShareData,
	}); err != nil {
		t.Fatalf("upserting threshold share: %v", err)
	}

	// Key scenario: the creator's own pubkey IS in remainingPeers.
	// This is the trigger condition described in workspace-com.
	remainingPeers := []store.PeerEndpoint{
		{
			CampfireID:    oldCampfireID,
			MemberPubkey:  agentID.PublicKeyHex(), // creator's own pubkey
			Endpoint:      "http://localhost:9001",
			ParticipantID: 1,
		},
		{
			CampfireID:    oldCampfireID,
			MemberPubkey:  peerID.PublicKeyHex(),
			Endpoint:      "http://localhost:9002",
			ParticipantID: 2,
		},
	}

	// Add peer endpoints to store for the old campfire.
	for _, pe := range remainingPeers {
		if err := s.UpsertPeerEndpoint(pe); err != nil {
			t.Fatalf("upserting peer endpoint: %v", err)
		}
	}

	// Call evictThresholdN. The evicted peer is evictedID (not in remainingPeers).
	// Delivery will fail (no real server) but that's expected — we want to test
	// the store state after the function completes.
	newCampfireID, err := evictThresholdN(
		agentID, s, stateDir,
		oldCampfireID, oldCFState,
		evictedID.PublicKeyHex(),
		remainingPeers,
		"test-eviction",
		2,
	)
	if err != nil {
		t.Fatalf("evictThresholdN: %v", err)
	}

	if newCampfireID == "" {
		t.Fatal("evictThresholdN returned empty new campfire ID")
	}

	// Verify: the creator's pubkey is NOT stored in peer_endpoints for the new campfire.
	newPeers, err := s.ListPeerEndpoints(newCampfireID)
	if err != nil {
		t.Fatalf("listing peer endpoints for new campfire: %v", err)
	}
	for _, pe := range newPeers {
		if pe.MemberPubkey == agentID.PublicKeyHex() {
			t.Errorf("creator pubkey %s should not appear in peer_endpoints for campfire %s (got PID=%d)",
				agentID.PublicKeyHex(), newCampfireID, pe.ParticipantID)
		}
	}

	// Verify: the creator's threshold share IS stored with participant ID 1.
	share, err := s.GetThresholdShare(newCampfireID)
	if err != nil {
		t.Fatalf("getting threshold share for new campfire: %v", err)
	}
	if share == nil {
		t.Fatal("creator threshold share should exist for new campfire")
	}
	if share.ParticipantID != 1 {
		t.Errorf("creator threshold share participant ID: got %d, want 1", share.ParticipantID)
	}

	// Verify: the legitimate peer IS in peer_endpoints with participant ID >= 2.
	found := false
	for _, pe := range newPeers {
		if pe.MemberPubkey == peerID.PublicKeyHex() {
			found = true
			if pe.ParticipantID < 2 {
				t.Errorf("peer participant ID should be >= 2, got %d", pe.ParticipantID)
			}
		}
	}
	if !found {
		t.Errorf("peer %s should appear in peer_endpoints for new campfire %s",
			peerID.PublicKeyHex(), newCampfireID)
	}

}

// TestEvictThresholdN_CreatorNotInPeers verifies the normal case where the
// creator is NOT in remainingPeers (the common case). The function should
// assign participant IDs correctly starting from 2.
func TestEvictThresholdN_CreatorNotInPeers(t *testing.T) {
	stateDir := t.TempDir()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	peerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating peer identity: %v", err)
	}

	evictedID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating evicted identity: %v", err)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	oldCFID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating old CF ID: %v", err)
	}
	oldCampfireID := oldCFID.PublicKeyHex()

	oldCFState := &campfire.CampfireState{
		PublicKey:             oldCFID.PublicKey,
		PrivateKey:            nil,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		Threshold:             2,
	}
	oldStateData, err := cfencoding.Marshal(oldCFState)
	if err != nil {
		t.Fatalf("marshalling state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, oldCampfireID+".cbor"), oldStateData, 0600); err != nil {
		t.Fatalf("writing state file: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:   oldCampfireID,
		TransportDir: stateDir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     1000,
		Threshold:    2,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	dummyShareData := []byte(`{"participant_id":1,"secret_share":"dGVzdA==","public_data":"dGVzdA=="}`)
	if err := s.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    oldCampfireID,
		ParticipantID: 1,
		SecretShare:   dummyShareData,
	}); err != nil {
		t.Fatalf("upserting threshold share: %v", err)
	}

	// Normal case: creator NOT in remainingPeers.
	remainingPeers := []store.PeerEndpoint{
		{
			CampfireID:    oldCampfireID,
			MemberPubkey:  peerID.PublicKeyHex(),
			Endpoint:      "http://localhost:9002",
			ParticipantID: 2,
		},
	}
	for _, pe := range remainingPeers {
		if err := s.UpsertPeerEndpoint(pe); err != nil {
			t.Fatalf("upserting peer endpoint: %v", err)
		}
	}

	newCampfireID, err := evictThresholdN(
		agentID, s, stateDir,
		oldCampfireID, oldCFState,
		evictedID.PublicKeyHex(),
		remainingPeers,
		"test-eviction",
		2,
	)
	if err != nil {
		t.Fatalf("evictThresholdN: %v", err)
	}

	// Creator should not be in peer_endpoints.
	newPeers, err := s.ListPeerEndpoints(newCampfireID)
	if err != nil {
		t.Fatalf("listing peer endpoints: %v", err)
	}
	for _, pe := range newPeers {
		if pe.MemberPubkey == agentID.PublicKeyHex() {
			t.Errorf("creator should not be in peer_endpoints, got entry with PID=%d", pe.ParticipantID)
		}
	}

	// Peer should be present with PID >= 2.
	found := false
	for _, pe := range newPeers {
		if pe.MemberPubkey == peerID.PublicKeyHex() {
			found = true
			if pe.ParticipantID < 2 {
				t.Errorf("peer PID should be >= 2, got %d", pe.ParticipantID)
			}
		}
	}
	if !found {
		t.Errorf("peer should appear in peer_endpoints for new campfire")
	}
}
