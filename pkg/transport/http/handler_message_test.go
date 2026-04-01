package http_test

// Tests for relay delivery in handleDeliver (workspace-iwc2.2, workspace-t6vv).
//
// Scenarios covered:
//   - Member A signs a message; Member B (deliverer) relays it via POST /deliver → 200.
//   - Relay stores message with A's sender key (not B's deliverer key).
//   - Non-member C attempts to relay a message signed by Member A → 403.
//   - Observer member C attempts to relay a message signed by full member A → 403.
//   - Direct delivery (sender == deliverer) still works → 200.
//   - Message with forged provenance hop (invalid signature) rejected with 400.
//   - GetPeerRole store error (DB closed) → 500 fail-closed (FED-1).
//
// Port block: 440-459, 540 (handler_message_test.go)

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// getPeerRoleErrorStore wraps a real store but returns an error from GetPeerRole.
// Used by TestHandleDeliver_GetPeerRoleStoreError to exercise the fail-closed path
// in handleDeliver without closing the underlying DB (which would also break
// checkMembership in authMiddleware, causing 403 before 500 is reachable).
type getPeerRoleErrorStore struct {
	store.Store
}

func (s *getPeerRoleErrorStore) GetPeerRole(campfireID, memberPubkey string) (string, error) {
	return "", errors.New("injected GetPeerRole store error")
}

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

// TestRelayDeliverSenderAttributed verifies that when member B relays a message
// signed by member A, the stored message is attributed to A (the original author),
// not to B (the HTTP deliverer). This is the core relay correctness property:
// relaying preserves authorship.
func TestRelayDeliverSenderAttributed(t *testing.T) {
	campfireID := "relay-sender-attributed"
	idA := tempIdentity(t) // message author
	idB := tempIdentity(t) // relay deliverer

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idA.PublicKeyHex())
	addPeerEndpoint(t, s, campfireID, idB.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+444)
	startTransportWithSelf(t, addr, s, idA)
	ep := fmt.Sprintf("http://%s", addr)

	// A signs the message.
	msg := newTestMessage(t, idA)
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}

	// B delivers it on behalf of A.
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

	// Sync back messages — the stored sender must be A, not B.
	msgs, err := cfhttp.Sync(ep, campfireID, 0, idA)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message after relay deliver, got %d", len(msgs))
	}
	if msgs[0].SenderHex() != idA.PublicKeyHex() {
		t.Errorf("relay message stored with wrong sender: got %s, want %s (author A)", msgs[0].SenderHex(), idA.PublicKeyHex())
	}
	if msgs[0].ID != msg.ID {
		t.Errorf("relay message ID mismatch: got %s, want %s", msgs[0].ID, msg.ID)
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

// TestDeliverForgedProvenanceHopRejected verifies that a message with a forged
// provenance hop (invalid hop signature) is rejected by handleDeliver with 400.
//
// This is the fix for campfire-agent-pq2: prior to the fix, forged blind-relay hops
// would be stored, corrupting IsBridged() results. The hop verification loop now
// mirrors the check in pkg/protocol/read.go before calling store.AddMessage.
func TestDeliverForgedProvenanceHopRejected(t *testing.T) {
	campfireID := "deliver-forged-hop"
	idA := tempIdentity(t) // message author and deliverer

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idA.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+445)
	startTransportWithSelf(t, addr, s, idA)
	ep := fmt.Sprintf("http://%s", addr)

	// A signs a valid message.
	msg := newTestMessage(t, idA)

	// Attach a forged provenance hop: valid-looking hop with an all-zero (invalid) signature.
	// A real hop signature covers the hop fields signed with the campfire key.
	// We use idA's pubkey as the CampfireID to form a structurally valid hop, but
	// provide a random garbage signature so VerifyHop returns false.
	forgedHop := message.ProvenanceHop{
		CampfireID:     idA.PublicKey,        // any non-empty key bytes; signature won't match
		MembershipHash: []byte("fake-hash"),
		MemberCount:    2,
		JoinProtocol:   "http",
		Timestamp:      msg.Timestamp,
		Role:           "blind-relay",
		Signature:      make([]byte, 64), // all-zero signature — invalid
	}
	msg.Provenance = append(msg.Provenance, forgedHop)

	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	signTestRequest(req, idA, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deliver request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for forged provenance hop, got %d: %s", resp.StatusCode, respBody)
	}
}

// TestHandleDeliver_GetPeerRoleStoreError verifies fail-closed behaviour (FED-1):
// when GetPeerRole returns a store error, handleDeliver must return HTTP 500,
// not HTTP 200 and not a silent accept.
//
// Setup: idA is "self" for the transport; idB is a registered member peer.
// The transport is started with a getPeerRoleErrorStore wrapper that makes
// ListPeerEndpoints (used by authMiddleware) succeed from the underlying store,
// while GetPeerRole (called in handleDeliver for non-self senders) always returns
// an injected error. Because idB != self, handleDeliver calls GetPeerRole for
// idB → error → fail-closed HTTP 500 (handler_message.go lines ~65-69).
func TestHandleDeliver_GetPeerRoleStoreError(t *testing.T) {
	campfireID := "deliver-getpeerrole-store-error"
	idA := tempIdentity(t) // self / transport identity
	idB := tempIdentity(t) // non-self sender whose role lookup will fail

	realStore := tempStore(t)
	addMembership(t, realStore, campfireID)
	// Register both A (self) and B so authMiddleware's ListPeerEndpoints finds B.
	addPeerEndpoint(t, realStore, campfireID, idA.PublicKeyHex())
	addPeerEndpoint(t, realStore, campfireID, idB.PublicKeyHex())

	// Wrap: ListPeerEndpoints succeeds (from realStore), GetPeerRole always errors.
	wrapped := &getPeerRoleErrorStore{Store: realStore}

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+540)
	startTransportWithSelf(t, addr, wrapped, idA)
	ep := fmt.Sprintf("http://%s", addr)

	// B signs and delivers a message (B != self → GetPeerRole is called → error → 500).
	msg := newTestMessage(t, idB)
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	signTestRequest(req, idB, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deliver request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 500 (fail-closed on GetPeerRole store error), got %d: %s", resp.StatusCode, respBody)
	}
}
