package http_test

// Regression test for campfire-agent-4ii: relay auth — campfire key accepted as valid sender.
//
// Bug: When East relays a message to West or Central (signed with the campfire key),
// Central/West's checkMembership rejects the request with 403 "not a campfire member"
// because the campfire public key is not in the peer_endpoints table.
//
// Fix: checkMembership now accepts senderHex == campfireID as a valid sender.
//
// This test verifies the 3-instance relay paths WITHOUT registering the campfire key
// as a row in peer_endpoints. The relay test passes only if the auth fix is in place.
//
// Three instances: East (campfire host), West (member), Central (member).
// Six paths:
//   East→West     direct
//   East→Central  direct
//   West→East     direct
//   West→Central  relay: West→East direct, East relays→Central (signed with campfire key)
//   Central→East  direct
//   Central→West  relay: Central→East direct, East relays→West (signed with campfire key)
//
// Port block: 560-599 (relay_auth_test.go)

import (
	"crypto/ed25519"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// freeRelayAddr returns a "127.0.0.1:<port>" address using an ephemeral port.
// It briefly opens a listener to discover a free port, then closes it.
// Named freeRelayAddr to avoid collision with freeAddr in peer_join_test.go.
func freeRelayAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestRelayAuth_ThreeInstanceRelay(t *testing.T) {
	// Generate campfire identity.
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := fmt.Sprintf("%x", cfPub)

	// Three agent identities: East (host), West (member), Central (member).
	idEast := tempIdentity(t)
	idWest := tempIdentity(t)
	idCentral := tempIdentity(t)

	sEast := tempStore(t)
	sWest := tempStore(t)
	sCentral := tempStore(t)

	// East owns (hosts) the campfire.
	if err := sEast.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("East AddMembership: %v", err)
	}

	addrEast := freeRelayAddr(t)
	addrWest := freeRelayAddr(t)
	addrCentral := freeRelayAddr(t)
	epEast := fmt.Sprintf("http://%s", addrEast)
	epWest := fmt.Sprintf("http://%s", addrWest)
	epCentral := fmt.Sprintf("http://%s", addrCentral)

	keyProvider := func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("campfire not found: %s", id)
	}

	// Start East transport (hosts the campfire, has the key provider).
	trEast := cfhttp.New(addrEast, sEast)
	trEast.SetSelfInfo(idEast.PublicKeyHex(), epEast)
	trEast.SetKeyProvider(keyProvider)
	if err := trEast.Start(); err != nil {
		t.Fatalf("starting East: %v", err)
	}
	t.Cleanup(func() { trEast.Stop() }) //nolint:errcheck

	// Start West transport (member, has key provider for outbound signing).
	trWest := cfhttp.New(addrWest, sWest)
	trWest.SetSelfInfo(idWest.PublicKeyHex(), epWest)
	trWest.SetKeyProvider(keyProvider)
	if err := trWest.Start(); err != nil {
		t.Fatalf("starting West: %v", err)
	}
	t.Cleanup(func() { trWest.Stop() }) //nolint:errcheck

	// Start Central transport (member, has key provider for outbound signing).
	trCentral := cfhttp.New(addrCentral, sCentral)
	trCentral.SetSelfInfo(idCentral.PublicKeyHex(), epCentral)
	trCentral.SetKeyProvider(keyProvider)
	if err := trCentral.Start(); err != nil {
		t.Fatalf("starting Central: %v", err)
	}
	t.Cleanup(func() { trCentral.Stop() }) //nolint:errcheck

	time.Sleep(50 * time.Millisecond) // let listeners start

	// West joins East's campfire (with endpoint, so East can relay back to West).
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
	// Register East's endpoint on West so West can deliver directly to East.
	for _, p := range resultWest.Peers {
		if p.Endpoint != "" {
			trWest.AddPeer(campfireID, p.PubKeyHex, p.Endpoint)
			sWest.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
				CampfireID:   campfireID,
				MemberPubkey: p.PubKeyHex,
				Endpoint:     p.Endpoint,
			})
		}
	}
	// Register West as a member on East (so East's auth accepts West's deliveries).
	sEast.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idWest.PublicKeyHex(),
		Endpoint:     epWest,
	})

	// Central joins East's campfire.
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
	// Register East's endpoint on Central so Central can deliver directly to East.
	for _, p := range resultCentral.Peers {
		if p.Endpoint != "" {
			trCentral.AddPeer(campfireID, p.PubKeyHex, p.Endpoint)
			sCentral.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
				CampfireID:   campfireID,
				MemberPubkey: p.PubKeyHex,
				Endpoint:     p.Endpoint,
			})
		}
	}
	// Register Central as a member on East (so East's auth accepts Central's deliveries).
	sEast.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idCentral.PublicKeyHex(),
		Endpoint:     epCentral,
	})

	// Register East's agent key on West and Central so they accept direct deliveries
	// from East's agent key. Do NOT register the campfire key — the auth fix must
	// handle that implicitly via senderHex == campfireID in checkMembership.
	sWest.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idEast.PublicKeyHex(),
		Endpoint:     epEast,
	})
	sCentral.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: idEast.PublicKeyHex(),
		Endpoint:     epEast,
	})

	// Note: We do NOT register the campfire public key (campfireID) as a peer_endpoint
	// row on West or Central. The bug was that such registrations were required; the fix
	// makes them unnecessary by accepting senderHex == campfireID in checkMembership.

	time.Sleep(50 * time.Millisecond) // settle

	// sendAndVerify delivers a message from senderID to deliverEndpoint and checks
	// that it eventually appears in targetStore.
	sendAndVerify := func(t *testing.T, name string, senderID *identity.Identity, deliverEndpoint string, targetStore store.Store) {
		t.Helper()
		payload := fmt.Sprintf("relay-auth-test-%s-%s", name, senderID.PublicKeyHex()[:8])
		msg, err := message.NewMessage(senderID.PrivateKey, senderID.PublicKey, []byte(payload), []string{"test"}, nil)
		if err != nil {
			t.Fatalf("[%s] NewMessage: %v", name, err)
		}
		if err := cfhttp.Deliver(deliverEndpoint, campfireID, msg, senderID); err != nil {
			t.Fatalf("[%s] Deliver: %v", name, err)
		}
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

	// Path 1: East→West direct (East delivers to West's endpoint using East's agent key).
	t.Run("East→West", func(t *testing.T) {
		sendAndVerify(t, "East→West", idEast, epWest, sWest)
	})

	// Path 2: East→Central direct.
	t.Run("East→Central", func(t *testing.T) {
		sendAndVerify(t, "East→Central", idEast, epCentral, sCentral)
	})

	// Path 3: West→East direct.
	t.Run("West→East", func(t *testing.T) {
		sendAndVerify(t, "West→East", idWest, epEast, sEast)
	})

	// Path 4: West→Central relay.
	// West delivers to East; East relays to Central signed with the campfire key.
	// Central's checkMembership must accept the campfire key sender (the bug path).
	t.Run("West→Central (relay)", func(t *testing.T) {
		sendAndVerify(t, "West→Central", idWest, epEast, sCentral)
	})

	// Path 5: Central→East direct.
	t.Run("Central→East", func(t *testing.T) {
		sendAndVerify(t, "Central→East", idCentral, epEast, sEast)
	})

	// Path 6: Central→West relay.
	// Central delivers to East; East relays to West signed with the campfire key.
	// West's checkMembership must accept the campfire key sender (the bug path).
	t.Run("Central→West (relay)", func(t *testing.T) {
		sendAndVerify(t, "Central→West", idCentral, epEast, sWest)
	})
}
