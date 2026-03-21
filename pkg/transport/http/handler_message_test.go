package http_test

// Tests for relay delivery in handleDeliver (workspace-iwc2.2).
//
// Scenarios covered:
//   - Member A signs a message; Member B (deliverer) relays it via POST /deliver → 200.
//   - Non-member C attempts to relay a message signed by Member A → 403.
//   - Direct delivery (sender == deliverer) still works → 200.
//
// Port block: 440-459 (handler_message_test.go)

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"testing"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestRelayDeliverMemberAllowed verifies that a campfire member (B) can deliver
// a message signed by a different campfire member (A). This is the relay case
// required for cf-bridge: the bridge agent delivers messages on behalf of their
// original authors.
func TestRelayDeliverMemberAllowed(t *testing.T) {
	campfireID := "relay-deliver-allowed"
	idA := tempIdentity(t) // message author
	idB := tempIdentity(t) // relay deliverer

	s := tempStore(t)
	addMembership(t, s, campfireID)
	// Both A and B are campfire members.
	addPeerEndpoint(t, s, campfireID, idA.PublicKeyHex())
	addPeerEndpoint(t, s, campfireID, idB.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+440)
	startTransportWithSelf(t, addr, s, idA)
	ep := fmt.Sprintf("http://%s", addr)

	// A signs the message.
	msg := newTestMessage(t, idA)
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}

	// B delivers it (relay: HTTP sender = B, message author = A).
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	signTestRequest(req, idB, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("relay deliver request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for member relay deliver, got %d: %s", resp.StatusCode, respBody)
	}
}

// TestRelayDeliverNonMemberForbidden verifies that a non-member (C) cannot relay
// a message signed by a campfire member (A). The deliverer must be a campfire member.
func TestRelayDeliverNonMemberForbidden(t *testing.T) {
	campfireID := "relay-deliver-forbidden"
	idA := tempIdentity(t) // message author (member)
	idC := tempIdentity(t) // deliverer (non-member)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	// Only A is a member; C is not.
	addPeerEndpoint(t, s, campfireID, idA.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+441)
	startTransportWithSelf(t, addr, s, idA)
	ep := fmt.Sprintf("http://%s", addr)

	// A signs the message.
	msg := newTestMessage(t, idA)
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}

	// C attempts to relay it — should be rejected (C is not a member).
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	signTestRequest(req, idC, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("non-member relay deliver request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 for non-member relay deliver, got %d: %s", resp.StatusCode, respBody)
	}
}

// TestRelayDeliverObserverForbidden verifies that a campfire member with RoleObserver
// cannot relay a message on behalf of another member. The observer role check in
// handleDeliver (applied to the HTTP request sender, not the message author) must
// reject the relay with 403, regardless of the fact that the message itself is signed
// by a full member. This exercises the role check at the relay path: observer C relays
// a message authored by full member A.
func TestRelayDeliverObserverForbidden(t *testing.T) {
	campfireID := "relay-deliver-observer-forbidden"
	idA := tempIdentity(t) // message author (full member)
	idC := tempIdentity(t) // relay deliverer (observer member)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	// A is a full member; C is a member but with RoleObserver.
	addPeerEndpoint(t, s, campfireID, idA.PublicKeyHex())
	addPeerEndpointWithRole(t, s, campfireID, idC.PublicKeyHex(), "observer")

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+443)
	startTransportWithSelf(t, addr, s, idA)
	ep := fmt.Sprintf("http://%s", addr)

	// A signs the message.
	msg := newTestMessage(t, idA)
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}

	// C (observer) attempts to relay it — should be rejected with 403.
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	signTestRequest(req, idC, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("observer relay deliver request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 for observer relay deliver, got %d: %s", resp.StatusCode, respBody)
	}
}

// TestDirectDeliverStillWorks verifies that the normal case (sender == deliverer)
// continues to work after the relay relaxation.
func TestDirectDeliverStillWorks(t *testing.T) {
	campfireID := "relay-deliver-direct"
	idA := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idA.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+442)
	startTransportWithSelf(t, addr, s, idA)
	ep := fmt.Sprintf("http://%s", addr)

	// A signs and delivers its own message (direct, no relay).
	msg := newTestMessage(t, idA)
	if err := cfhttp.Deliver(ep, campfireID, msg, idA); err != nil {
		t.Fatalf("direct deliver failed: %v", err)
	}
}
