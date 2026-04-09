package cmd

// relay.go — Functions for registering a locally-created campfire on an
// external HTTP relay via POST /campfire/create.
//
// Protocol (design-cf-remote-relay.md §3):
//  1. Creator generates campfire locally (campfire.New).
//  2. Creator GETs relay's static X25519 pub key via GET /campfire/relay-info.
//  3. Creator encrypts campfire privkey: AES-GCM(HKDF(ECDH(creatorEphPr, relayStaticPub))).
//  4. Creator POSTs signed CreateCampfireRequest to /campfire/create.
//  5. Relay registers campfire, returns beacon + endpoint.
//  6. Creator records p2p-http membership in local store with relay as peer endpoint.

import (
	"context"
	"fmt"

	"github.com/campfire-net/campfire/pkg/admission"
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// registerOnRelay registers a locally-created campfire on an HTTP relay.
// It performs the full CREATE protocol:
//   - fetches relay's static X25519 pub key
//   - encrypts campfire privkey
//   - POSTs to /campfire/create
//   - records p2p-http membership in local store
//   - stores relay as peer endpoint (keyed by campfire pubkey)
//
// Returns the beacon string from the relay response (may be empty on older relays).
func registerOnRelay(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, relayURL, description string) (beaconStr string, relayEndpoint string, err error) {
	campfireID := cf.PublicKeyHex()

	// Build relay-agnostic campfire descriptor.
	cfDesc := &cfhttp.CampfireDescriptor{
		CampfireID:            campfireID,
		PrivateKey:            cf.PrivateKey,
		JoinProtocol:          cf.JoinProtocol,
		ReceptionRequirements: cf.ReceptionRequirements,
		Threshold:             cf.Threshold,
		Description:           description,
	}
	agentDesc := &cfhttp.AgentDescriptor{
		PublicKeyHex: agentID.PublicKeyHex(),
		PrivateKey:   agentID.PrivateKey,
	}

	// Register on relay (fetch relay-info + POST /campfire/create).
	resp, err := cfhttp.RegisterOnRelay(relayURL, cfDesc, agentDesc)
	if err != nil {
		return "", "", fmt.Errorf("registering on relay %s: %w", relayURL, err)
	}

	effectiveEndpoint := resp.Endpoint
	if effectiveEndpoint == "" {
		effectiveEndpoint = relayURL
	}

	// Record p2p-http membership in local store.
	stateDir := "" // no local CBOR state for relay-registered campfires
	if _, err := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		Store: s,
	}, admission.AdmissionRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            store.PeerRoleCreator,
		JoinProtocol:    cf.JoinProtocol,
		TransportDir:    stateDir,
		TransportType:   "p2p-http",
		Description:     description,
	}); err != nil {
		return "", "", fmt.Errorf("recording membership: %w", err)
	}

	// Store relay as peer endpoint (keyed by campfire pubkey so syncFromHTTPPeers
	// can find it — same convention as joinP2PHTTP).
	s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: campfireID, // campfire pubkey as member identifier for relay
		Endpoint:     effectiveEndpoint,
	})

	return resp.Beacon, effectiveEndpoint, nil
}
