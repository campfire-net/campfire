package http_test

// Tests for handleMembership event type coverage (workspace-wn1).
// Three cases not covered by existing tests:
//  1. Unknown event type (e.g. "kick") → 400 Bad Request.
//  2. Join event with empty endpoint → 200 OK, no peer added to transport.
//  3. Evict event (happy path) → 200 OK, peer removed from transport.

import (
	"fmt"
	"testing"
	"time"

	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestMembershipUnknownEventType verifies that the handleMembership default case
// returns 400 for event types that are not "join", "leave", or "evict".
func TestMembershipUnknownEventType(t *testing.T) {
	campfireID := "test-membership-unknown-event"
	idMember := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+110)
	startTransport(t, addr, s)
	ep := fmt.Sprintf("http://%s", addr)

	unknownEvent := cfhttp.MembershipEvent{
		Event:  "kick", // not a valid event type
		Member: idMember.PublicKeyHex(),
	}

	err := cfhttp.NotifyMembership(ep, campfireID, unknownEvent, idMember)
	if err == nil {
		t.Error("expected 400 for unknown event type 'kick', got nil error")
	}
}

// TestMembershipJoinEmptyEndpointIgnored verifies that a join event with an empty
// endpoint is silently accepted (200 OK) but does not add a peer to the transport.
func TestMembershipJoinEmptyEndpointIgnored(t *testing.T) {
	campfireID := "test-membership-join-empty-endpoint"
	idMember := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+111)
	tr := cfhttp.New(addr, s)
	if err := tr.Start(); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ep := fmt.Sprintf("http://%s", addr)

	joinEvent := cfhttp.MembershipEvent{
		Event:    "join",
		Member:   idMember.PublicKeyHex(),
		Endpoint: "", // empty endpoint: handler must skip AddPeer
	}

	if err := cfhttp.NotifyMembership(ep, campfireID, joinEvent, idMember); err != nil {
		t.Errorf("join with empty endpoint should return 200, got error: %v", err)
	}

	// Peer must NOT have been added to the in-memory transport.
	peers := tr.Peers(campfireID)
	for _, p := range peers {
		if p.PubKeyHex == idMember.PublicKeyHex() {
			t.Errorf("peer %s was added despite empty endpoint on join", idMember.PublicKeyHex())
		}
	}
}

// TestMembershipEvictRemovesPeer verifies that an evict event (from a non-creator campfire
// where the membership record has an empty CreatorPubkey) removes the target peer.
func TestMembershipEvictRemovesPeer(t *testing.T) {
	campfireID := "test-membership-evict-removes-peer"
	idSender := tempIdentity(t)
	idVictim := tempIdentity(t)

	s := tempStore(t)
	// Use addMembership (no CreatorPubkey) so the backward-compat path allows any sender to evict.
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idSender.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+112)
	tr := cfhttp.New(addr, s)
	if err := tr.Start(); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ep := fmt.Sprintf("http://%s", addr)

	// Pre-add the victim so we can verify removal.
	tr.AddPeer(campfireID, idVictim.PublicKeyHex(), "http://victim:9999")

	// Sanity: victim is present before evict.
	found := false
	for _, p := range tr.Peers(campfireID) {
		if p.PubKeyHex == idVictim.PublicKeyHex() {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("victim peer was not pre-added to transport; test setup error")
	}

	evictEvent := cfhttp.MembershipEvent{
		Event:  "evict",
		Member: idVictim.PublicKeyHex(),
	}

	if err := cfhttp.NotifyMembership(ep, campfireID, evictEvent, idSender); err != nil {
		t.Errorf("evict should succeed (empty CreatorPubkey = backward compat), got error: %v", err)
	}

	// Victim must be gone from transport peer list.
	for _, p := range tr.Peers(campfireID) {
		if p.PubKeyHex == idVictim.PublicKeyHex() {
			t.Errorf("victim peer %s still in peer list after evict", idVictim.PublicKeyHex())
		}
	}
}
