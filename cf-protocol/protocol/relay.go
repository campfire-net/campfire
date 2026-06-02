package protocol

import (
	"context"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/internal/admission"
	"github.com/campfire-net/campfire/cf-protocol/internal/campfire"
	"github.com/campfire-net/campfire/cf-protocol/internal/store"
	"github.com/campfire-net/campfire/cf-protocol/internal/transport/fs"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/internal/transport/http"
)

// RelayRegistration is the outcome of registering a campfire on an HTTP relay.
//
// Beacon and InviteCode are issued by the relay and may be empty when the relay
// is older or the campfire is open (no invite needed).
type RelayRegistration struct {
	// Endpoint is the relay's public endpoint URL (falls back to the registration
	// URL when the relay does not return one).
	Endpoint string
	// Beacon is the relay-issued portable beacon string ("beacon:…"), or empty.
	Beacon string
	// InviteCode is the relay-issued default invite code, or empty.
	InviteCode string
}

// RegisterOnRelay registers an already-created campfire on an HTTP relay and
// records the local state needed to send to it: it POSTs the campfire to the
// relay, writes the campfire state to the local filesystem transport (so the
// caller can sign provenance hops), records a p2p-http membership for the
// creator, and stores the relay as a peer endpoint.
//
// It is the single implementation behind both Client.Create's relay path
// (P2PHTTPTransport.RelayEndpoint) and the cf CLI's relay create, so the two
// stay in parity. It does NOT publish a local beacon — that is the caller's
// choice (Client.Create publishes one pointing at the relay endpoint).
//
// baseDir is the local filesystem transport base; empty uses fs.DefaultBaseDir().
func (c *Client) RegisterOnRelay(cf *campfire.Campfire, relayURL, baseDir, description string) (*RelayRegistration, error) {
	if c.identity == nil {
		return nil, fmt.Errorf("identity required to register on a relay")
	}
	if baseDir == "" {
		baseDir = fs.DefaultBaseDir()
	}
	campfireID := cf.PublicKeyHex()

	cfDesc := &cfhttp.CampfireDescriptor{
		CampfireID:            campfireID,
		PrivateKey:            cf.PrivateKey,
		JoinProtocol:          cf.JoinProtocol,
		ReceptionRequirements: cf.ReceptionRequirements,
		Threshold:             cf.Threshold,
		Description:           description,
	}
	agentDesc := &cfhttp.AgentDescriptor{
		PublicKeyHex: c.identity.PublicKeyHex(),
		PrivateKey:   c.identity.PrivateKey,
		Signer:       c.identity.NewSigner().Sign,
	}

	resp, err := cfhttp.RegisterOnRelay(relayURL, cfDesc, agentDesc)
	if err != nil {
		return nil, fmt.Errorf("registering on relay %s: %w", relayURL, err)
	}

	endpoint := resp.Endpoint
	if endpoint == "" {
		endpoint = relayURL
	}

	// Store campfire state locally so the creator can sign provenance hops; the
	// relay holds an encrypted copy of the private key but the creator needs the
	// plaintext locally.
	transport := fs.New(baseDir)
	if err := transport.Init(cf); err != nil {
		return nil, fmt.Errorf("storing campfire state locally: %w", err)
	}

	// Record p2p-http membership in the local store.
	if _, err := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		FSTransport: transport,
		Store:       c.store,
	}, admission.AdmissionRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: c.identity.PublicKeyHex(),
		Role:            store.PeerRoleCreator,
		JoinProtocol:    cf.JoinProtocol,
		TransportDir:    transport.CampfireDir(campfireID),
		TransportType:   "p2p-http",
		Description:     description,
	}); err != nil {
		return nil, fmt.Errorf("recording membership: %w", err)
	}

	// Store the relay as a peer endpoint (keyed by campfire pubkey, the same
	// convention joinP2PHTTP uses, so syncFromHTTPPeers can find it).
	if err := c.store.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: campfireID,
		Endpoint:     endpoint,
	}); err != nil {
		return nil, fmt.Errorf("storing relay peer endpoint: %w", err)
	}

	return &RelayRegistration{
		Endpoint:   endpoint,
		Beacon:     resp.Beacon,
		InviteCode: resp.InviteCode,
	}, nil
}
