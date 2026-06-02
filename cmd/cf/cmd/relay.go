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
	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/identity"
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
func registerOnRelay(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, baseDir, relayURL, description string) (beaconStr string, relayEndpoint string, inviteCode string, err error) {
	// Delegate to the SDK so the CLI and protocol.Client.Create share one relay
	// registration implementation (no drift). The local beacon publish stays in
	// the CLI's createAndRegisterOnRelay, matching prior behavior.
	reg, err := protocol.New(s, agentID).RegisterOnRelay(cf, relayURL, baseDir, description)
	if err != nil {
		return "", "", "", err
	}
	return reg.Beacon, reg.Endpoint, reg.InviteCode, nil
}
