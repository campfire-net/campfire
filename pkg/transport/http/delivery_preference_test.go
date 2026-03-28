package http_test

// Tests for handleMembership "delivery" event (campfire-agent-gol):
//
// A member can change their delivery preference after joining by sending a
// MembershipEvent with Event="delivery". When Endpoint is non-empty, the push
// endpoint is stored and the transport peer list is updated. When Endpoint is
// empty, the endpoint is removed (member switches to pull).
//
// Port block: 560-563 (this file uses 560-563).
// Register in http_test.go port registry when merging.

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestDeliveryEventSetEndpoint verifies that sending a "delivery" event with a
// non-empty endpoint stores the endpoint and the peer is discoverable via the store.
func TestDeliveryEventSetEndpoint(t *testing.T) {
	// 203.0.113.x is a documentation range blocked by SSRF validation.
	// Override the validator so the server-side endpoint check is a no-op here,
	// matching the pattern used by join_pubkey_test.go.
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	campfireID, ep, s := setupDeliveryModeServer(t, 560, []string{campfire.DeliveryModePull, campfire.DeliveryModePush})
	member := tempIdentity(t)
	addPeerEndpoint(t, s, campfireID, member.PublicKeyHex())

	newEndpoint := "http://203.0.113.60:9060"
	event := cfhttp.MembershipEvent{
		Event:    "delivery",
		Member:   member.PublicKeyHex(),
		Endpoint: newEndpoint,
	}
	if err := cfhttp.NotifyMembership(ep, campfireID, event, member); err != nil {
		t.Fatalf("delivery event with endpoint failed: %v", err)
	}

	// Verify endpoint stored in peer_endpoints.
	peers, err := s.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}
	found := false
	for _, p := range peers {
		if p.MemberPubkey == member.PublicKeyHex() {
			found = true
			if p.Endpoint != newEndpoint {
				t.Errorf("stored endpoint = %q, want %q", p.Endpoint, newEndpoint)
			}
			break
		}
	}
	if !found {
		t.Errorf("peer endpoint not found in peer_endpoints after delivery event")
	}
}

// TestDeliveryEventClearEndpoint verifies that sending a "delivery" event with an
// empty endpoint removes the stored endpoint (member switches to pull).
func TestDeliveryEventClearEndpoint(t *testing.T) {
	campfireID, ep, s := setupDeliveryModeServer(t, 561, []string{campfire.DeliveryModePull, campfire.DeliveryModePush})
	member := tempIdentity(t)

	// Store an initial endpoint for this member.
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: member.PublicKeyHex(),
		Endpoint:     "http://203.0.113.61:9061",
	}); err != nil {
		t.Fatalf("UpsertPeerEndpoint: %v", err)
	}

	// Send delivery event with empty endpoint → switch to pull.
	event := cfhttp.MembershipEvent{
		Event:    "delivery",
		Member:   member.PublicKeyHex(),
		Endpoint: "",
	}
	if err := cfhttp.NotifyMembership(ep, campfireID, event, member); err != nil {
		t.Fatalf("delivery event (clear endpoint) failed: %v", err)
	}

	// Verify endpoint is removed from peer_endpoints.
	peers, err := s.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}
	for _, p := range peers {
		if p.MemberPubkey == member.PublicKeyHex() && p.Endpoint != "" {
			t.Errorf("expected endpoint removed, but still found %q for member", p.Endpoint)
		}
	}
}

// TestDeliveryEventRejectedPullOnly verifies that sending a "delivery" event with
// a non-empty endpoint is rejected (400) when the campfire is pull-only.
func TestDeliveryEventRejectedPullOnly(t *testing.T) {
	campfireID, ep, s := setupDeliveryModeServer(t, 562, []string{campfire.DeliveryModePull})
	member := tempIdentity(t)
	addPeerEndpoint(t, s, campfireID, member.PublicKeyHex())

	event := cfhttp.MembershipEvent{
		Event:    "delivery",
		Member:   member.PublicKeyHex(),
		Endpoint: "http://203.0.113.62:9062",
	}
	if err := cfhttp.NotifyMembership(ep, campfireID, event, member); err == nil {
		t.Error("expected rejection for delivery event on pull-only campfire, got nil error")
	}
}

// TestDeliveryEventRejectedMemberMismatch verifies that a delivery event where
// Member != senderHex is rejected (400) — a member cannot change another member's
// delivery preference.
func TestDeliveryEventRejectedMemberMismatch(t *testing.T) {
	campfireID, ep, s := setupDeliveryModeServer(t, 563, []string{campfire.DeliveryModePull, campfire.DeliveryModePush})

	senderA := tempIdentity(t)
	senderB := tempIdentity(t)
	addPeerEndpoint(t, s, campfireID, senderA.PublicKeyHex())
	addPeerEndpoint(t, s, campfireID, senderB.PublicKeyHex())

	// senderA signs the event but claims to be senderB.
	event := cfhttp.MembershipEvent{
		Event:    "delivery",
		Member:   senderB.PublicKeyHex(),
		Endpoint: "http://203.0.113.63:9063",
	}
	if err := cfhttp.NotifyMembership(ep, campfireID, event, senderA); err == nil {
		t.Error("expected rejection when member != sender for delivery event, got nil error")
	}
}
