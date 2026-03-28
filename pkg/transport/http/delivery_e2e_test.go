package http_test

// E2E tests for 3-instance relay with delivery modes (campfire-agent-qhp).
//
// Scenarios:
//   1. TestDeliveryE2E_ThreeInstanceAllPaths: push-capable campfire, 3 instances,
//      all 6 relay paths (East→West, East→Central, West→East, West→Central relay,
//      Central→East, Central→West relay). Delivery modes = ["pull","push"].
//   2. TestDeliveryE2E_PullOnlyCampfireRejectsEndpoint: pull-only campfire rejects
//      a join request that carries an endpoint.
//   3. TestDeliveryE2E_SwitchPullToPush: member joins without endpoint (pull), then
//      sends a MembershipEvent "delivery" with endpoint to switch to push — messages
//      start arriving via push delivery.
//   4. TestDeliveryE2E_SwitchPushToPull: member joins with endpoint (push), then
//      sends a MembershipEvent "delivery" with empty endpoint to switch to pull —
//      endpoint is removed from the host store (no more push delivery).
//
// All transports use dynamic port allocation (freeDeliveryE2EAddr) to avoid
// conflicts with any static port block. SSRF validation is overridden at test
// start so localhost endpoints are accepted by the server-side join handler.
//
// Port block: dynamic (freeDeliveryE2EAddr pattern — no static offset used).

import (
	"crypto/ed25519"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// freeDeliveryE2EAddr discovers a free ephemeral port and returns "127.0.0.1:<port>".
// Named to avoid collision with freeRelayAddr in relay_auth_test.go.
func freeDeliveryE2EAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeDeliveryE2EAddr: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// newDeliveryE2ECampfire generates a campfire keypair, writes the CBOR state to a
// temp dir, adds the membership to s, and returns the campfire ID hex, state dir,
// private key, and public key.
func newDeliveryE2ECampfire(t *testing.T, s store.Store, modes []string) (campfireID string, stateDir string, cfPriv, cfPub []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire keypair: %v", err)
	}
	campfireID = fmt.Sprintf("%x", pub)
	cfPub = pub
	cfPriv = priv

	stateDir = t.TempDir()
	state := campfire.CampfireState{
		PublicKey:     pub,
		PrivateKey:    priv,
		JoinProtocol:  "open",
		Threshold:     1,
		DeliveryModes: modes,
	}
	data, encErr := cfencoding.Marshal(state)
	if encErr != nil {
		t.Fatalf("encoding campfire state: %v", encErr)
	}
	if writeErr := os.WriteFile(filepath.Join(stateDir, campfireID+".cbor"), data, 0600); writeErr != nil {
		t.Fatalf("writing campfire state: %v", writeErr)
	}

	if addErr := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: stateDir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); addErr != nil {
		t.Fatalf("AddMembership: %v", addErr)
	}
	return
}

// startDeliveryE2ETransport starts a cfhttp.Transport at addr with the given store
// and key provider. It registers a cleanup to stop the transport.
func startDeliveryE2ETransport(t *testing.T, addr string, s store.Store, id *identity.Identity, keyProvider func(string) ([]byte, []byte, error)) *cfhttp.Transport {
	t.Helper()
	ep := fmt.Sprintf("http://%s", addr)
	tr := cfhttp.New(addr, s)
	tr.SetSelfInfo(id.PublicKeyHex(), ep)
	if keyProvider != nil {
		tr.SetKeyProvider(keyProvider)
	}
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport at %s: %v", addr, err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	return tr
}

// awaitMessageByPayload polls targetStore for a message with the given payload for up to 3s.
// Named to avoid collision with waitForMessage in forwarding_test.go (which matches by message ID).
func awaitMessageByPayload(t *testing.T, name string, targetStore store.Store, campfireID, payload string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msgs, err := targetStore.ListMessages(campfireID, 0)
		if err == nil {
			for _, m := range msgs {
				if string(m.Payload) == payload {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("[%s] message %q not found in target store after 3s", name, payload)
}

// TestDeliveryE2E_ThreeInstanceAllPaths verifies all 6 relay paths in a push-capable
// 3-instance campfire (East = host, West + Central = members with push endpoints).
//
// Delivery modes: ["pull","push"] — West and Central both join with endpoints.
// Six paths:
//
//	East→West      direct
//	East→Central   direct
//	West→East      direct
//	West→Central   relay: West→East, East relays→Central (signed with campfire key)
//	Central→East   direct
//	Central→West   relay: Central→East, East relays→West (signed with campfire key)
func TestDeliveryE2E_ThreeInstanceAllPaths(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	sEast := tempStore(t)
	sWest := tempStore(t)
	sCentral := tempStore(t)

	campfireID, _, cfPriv, cfPub := newDeliveryE2ECampfire(t, sEast,
		[]string{campfire.DeliveryModePull, campfire.DeliveryModePush})

	keyProvider := func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("campfire not found: %s", id)
	}

	idEast := tempIdentity(t)
	idWest := tempIdentity(t)
	idCentral := tempIdentity(t)

	addrEast := freeDeliveryE2EAddr(t)
	addrWest := freeDeliveryE2EAddr(t)
	addrCentral := freeDeliveryE2EAddr(t)
	epEast := fmt.Sprintf("http://%s", addrEast)
	epWest := fmt.Sprintf("http://%s", addrWest)
	epCentral := fmt.Sprintf("http://%s", addrCentral)

	startDeliveryE2ETransport(t, addrEast, sEast, idEast, keyProvider)
	startDeliveryE2ETransport(t, addrWest, sWest, idWest, keyProvider)
	startDeliveryE2ETransport(t, addrCentral, sCentral, idCentral, keyProvider)

	time.Sleep(50 * time.Millisecond)

	// West joins East (push endpoint provided — accepted because DeliveryModes includes "push").
	resultWest, err := cfhttp.Join(epEast, campfireID, idWest, epWest)
	if err != nil {
		t.Fatalf("West join failed: %v", err)
	}
	if err := sWest.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: resultWest.JoinProtocol,
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    resultWest.Threshold,
	}); err != nil {
		t.Fatalf("West AddMembership: %v", err)
	}
	for _, p := range resultWest.Peers {
		if p.Endpoint != "" {
			sWest.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
				CampfireID:   campfireID,
				MemberPubkey: p.PubKeyHex,
				Endpoint:     p.Endpoint,
			})
		}
	}
	// Register East as a known member on West (so West accepts direct deliveries from East).
	sWest.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idEast.PublicKeyHex(),
		Endpoint:     epEast,
	})
	// Register West as a known member on East (so East accepts West's deliveries).
	sEast.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idWest.PublicKeyHex(),
		Endpoint:     epWest,
	})

	// Central joins East (push endpoint provided).
	resultCentral, err := cfhttp.Join(epEast, campfireID, idCentral, epCentral)
	if err != nil {
		t.Fatalf("Central join failed: %v", err)
	}
	if err := sCentral.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: resultCentral.JoinProtocol,
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    resultCentral.Threshold,
	}); err != nil {
		t.Fatalf("Central AddMembership: %v", err)
	}
	for _, p := range resultCentral.Peers {
		if p.Endpoint != "" {
			sCentral.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
				CampfireID:   campfireID,
				MemberPubkey: p.PubKeyHex,
				Endpoint:     p.Endpoint,
			})
		}
	}
	// Register East as a known member on Central.
	sCentral.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idEast.PublicKeyHex(),
		Endpoint:     epEast,
	})
	// Register Central as a known member on East.
	sEast.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idCentral.PublicKeyHex(),
		Endpoint:     epCentral,
	})

	time.Sleep(50 * time.Millisecond)

	// sendAndVerify delivers a message from sender to deliverEndpoint and
	// waits for it to appear in targetStore.
	sendAndVerify := func(t *testing.T, name string, sender *identity.Identity, deliverEndpoint string, targetStore store.Store) {
		t.Helper()
		payload := fmt.Sprintf("e2e-delivery-%s-%s", name, sender.PublicKeyHex()[:8])
		msg, err := message.NewMessage(sender.PrivateKey, sender.PublicKey, []byte(payload), []string{"test"}, nil)
		if err != nil {
			t.Fatalf("[%s] NewMessage: %v", name, err)
		}
		if err := cfhttp.Deliver(deliverEndpoint, campfireID, msg, sender); err != nil {
			t.Fatalf("[%s] Deliver: %v", name, err)
		}
		awaitMessageByPayload(t, name, targetStore, campfireID, payload)
	}

	t.Run("East→West", func(t *testing.T) {
		sendAndVerify(t, "East→West", idEast, epWest, sWest)
	})
	t.Run("East→Central", func(t *testing.T) {
		sendAndVerify(t, "East→Central", idEast, epCentral, sCentral)
	})
	t.Run("West→East", func(t *testing.T) {
		sendAndVerify(t, "West→East", idWest, epEast, sEast)
	})
	t.Run("West→Central (relay)", func(t *testing.T) {
		// West delivers to East; East relays to Central using the campfire key.
		sendAndVerify(t, "West→Central", idWest, epEast, sCentral)
	})
	t.Run("Central→East", func(t *testing.T) {
		sendAndVerify(t, "Central→East", idCentral, epEast, sEast)
	})
	t.Run("Central→West (relay)", func(t *testing.T) {
		// Central delivers to East; East relays to West using the campfire key.
		sendAndVerify(t, "Central→West", idCentral, epEast, sWest)
	})
}

// TestDeliveryE2E_PullOnlyCampfireRejectsEndpoint verifies that a campfire with
// DeliveryModes=["pull"] rejects a join request that carries a non-empty endpoint.
func TestDeliveryE2E_PullOnlyCampfireRejectsEndpoint(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	sHost := tempStore(t)
	campfireID, _, cfPriv, cfPub := newDeliveryE2ECampfire(t, sHost,
		[]string{campfire.DeliveryModePull})

	keyProvider := func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	}

	idHost := tempIdentity(t)
	addrHost := freeDeliveryE2EAddr(t)
	startDeliveryE2ETransport(t, addrHost, sHost, idHost, keyProvider)
	time.Sleep(30 * time.Millisecond)

	epHost := fmt.Sprintf("http://%s", addrHost)

	joiner := tempIdentity(t)
	// Join with a non-empty endpoint — should be rejected (400) by the pull-only campfire.
	_, err := cfhttp.Join(epHost, campfireID, joiner, "http://203.0.113.99:9099")
	if err == nil {
		t.Error("expected Join to fail for pull-only campfire with endpoint, got nil error")
	}
}

// TestDeliveryE2E_SwitchPullToPush verifies the pull→push transition:
//   - Member joins without an endpoint (pull mode).
//   - Member sends a MembershipEvent "delivery" with endpoint to switch to push.
//   - Messages sent to the host start arriving at the member's transport (push delivery).
func TestDeliveryE2E_SwitchPullToPush(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	sHost := tempStore(t)
	sMember := tempStore(t)

	campfireID, _, cfPriv, cfPub := newDeliveryE2ECampfire(t, sHost,
		[]string{campfire.DeliveryModePull, campfire.DeliveryModePush})

	keyProvider := func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	}

	idHost := tempIdentity(t)
	idMember := tempIdentity(t)

	addrHost := freeDeliveryE2EAddr(t)
	addrMember := freeDeliveryE2EAddr(t)
	epHost := fmt.Sprintf("http://%s", addrHost)
	epMember := fmt.Sprintf("http://%s", addrMember)

	startDeliveryE2ETransport(t, addrHost, sHost, idHost, keyProvider)
	startDeliveryE2ETransport(t, addrMember, sMember, idMember, keyProvider)
	time.Sleep(50 * time.Millisecond)

	// Join WITHOUT endpoint (pull mode — accepted regardless of DeliveryModes).
	result, err := cfhttp.Join(epHost, campfireID, idMember, "")
	if err != nil {
		t.Fatalf("join without endpoint failed: %v", err)
	}
	if err := sMember.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: result.JoinProtocol,
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    result.Threshold,
	}); err != nil {
		t.Fatalf("member AddMembership: %v", err)
	}

	// Wire member as a known peer on host so host accepts member's messages and
	// membership events.
	sHost.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idMember.PublicKeyHex(),
		Endpoint:     "",
	})
	// Wire host as known peer on member so member accepts host's direct deliveries.
	sMember.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idHost.PublicKeyHex(),
		Endpoint:     epHost,
	})

	// Verify no endpoint is stored for member yet.
	peersBeforeSwitch, _ := sHost.ListPeerEndpoints(campfireID)
	for _, p := range peersBeforeSwitch {
		if p.MemberPubkey == idMember.PublicKeyHex() && p.Endpoint != "" {
			t.Errorf("expected no push endpoint before switch, got %q", p.Endpoint)
		}
	}

	// Switch from pull to push: send "delivery" event with endpoint.
	deliveryEvent := cfhttp.MembershipEvent{
		Event:    "delivery",
		Member:   idMember.PublicKeyHex(),
		Endpoint: epMember,
	}
	if err := cfhttp.NotifyMembership(epHost, campfireID, deliveryEvent, idMember); err != nil {
		t.Fatalf("delivery event (pull→push) failed: %v", err)
	}

	// Verify the endpoint is now stored on the host.
	peersAfterSwitch, err := sHost.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}
	found := false
	for _, p := range peersAfterSwitch {
		if p.MemberPubkey == idMember.PublicKeyHex() && p.Endpoint == epMember {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected push endpoint %q stored after switch, not found", epMember)
	}

	// Verify messages now arrive via push: host delivers to member's endpoint.
	payload := "e2e-switch-pull-to-push"
	msg, err := message.NewMessage(idHost.PrivateKey, idHost.PublicKey, []byte(payload), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	// Host's transport now knows member's endpoint (from the delivery event); deliver there.
	if err := cfhttp.Deliver(epMember, campfireID, msg, idHost); err != nil {
		t.Fatalf("direct push delivery to member failed: %v", err)
	}
	awaitMessageByPayload(t, "pull→push delivery", sMember, campfireID, payload)
}

// TestDeliveryE2E_SwitchPushToPull verifies the push→pull transition:
//   - Member joins with an endpoint (push mode).
//   - Member sends a MembershipEvent "delivery" with empty endpoint to switch to pull.
//   - The endpoint is removed from the host store (no more push-targeted delivery).
func TestDeliveryE2E_SwitchPushToPull(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)

	sHost := tempStore(t)
	sMember := tempStore(t)

	campfireID, _, cfPriv, cfPub := newDeliveryE2ECampfire(t, sHost,
		[]string{campfire.DeliveryModePull, campfire.DeliveryModePush})

	keyProvider := func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	}

	idHost := tempIdentity(t)
	idMember := tempIdentity(t)

	addrHost := freeDeliveryE2EAddr(t)
	addrMember := freeDeliveryE2EAddr(t)
	epHost := fmt.Sprintf("http://%s", addrHost)
	epMember := fmt.Sprintf("http://%s", addrMember)

	startDeliveryE2ETransport(t, addrHost, sHost, idHost, keyProvider)
	startDeliveryE2ETransport(t, addrMember, sMember, idMember, keyProvider)
	time.Sleep(50 * time.Millisecond)

	// Join WITH endpoint (push mode — accepted because DeliveryModes includes "push").
	result, err := cfhttp.Join(epHost, campfireID, idMember, epMember)
	if err != nil {
		t.Fatalf("join with endpoint failed: %v", err)
	}
	if err := sMember.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: result.JoinProtocol,
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    result.Threshold,
	}); err != nil {
		t.Fatalf("member AddMembership: %v", err)
	}
	// Wire host as known peer on member.
	sMember.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idHost.PublicKeyHex(),
		Endpoint:     epHost,
	})

	// Verify endpoint is stored after push join.
	peersAfterJoin, _ := sHost.ListPeerEndpoints(campfireID)
	foundAfterJoin := false
	for _, p := range peersAfterJoin {
		if p.MemberPubkey == idMember.PublicKeyHex() && p.Endpoint == epMember {
			foundAfterJoin = true
			break
		}
	}
	if !foundAfterJoin {
		t.Errorf("expected push endpoint %q stored after push join, not found", epMember)
	}

	// Switch from push to pull: send "delivery" event with empty endpoint.
	deliveryEvent := cfhttp.MembershipEvent{
		Event:    "delivery",
		Member:   idMember.PublicKeyHex(),
		Endpoint: "",
	}
	if err := cfhttp.NotifyMembership(epHost, campfireID, deliveryEvent, idMember); err != nil {
		t.Fatalf("delivery event (push→pull) failed: %v", err)
	}

	// Verify the endpoint is now removed from the host store.
	peersAfterSwitch, err := sHost.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}
	for _, p := range peersAfterSwitch {
		if p.MemberPubkey == idMember.PublicKeyHex() && p.Endpoint != "" {
			t.Errorf("expected endpoint cleared after push→pull switch, still found %q", p.Endpoint)
		}
	}
}
