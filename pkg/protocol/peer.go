package protocol

// peer.go — protocol.Client.AddPeer/RemovePeer/Peers
//
// Covered bead: campfire-agent-vxh
//
// Exposes peer management on protocol.Client so SDK consumers can manage
// peers without importing the cfhttp transport package directly.

import (
	"errors"
	"fmt"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport"
)

// PeerInfo holds information about a peer endpoint.
type PeerInfo struct {
	Endpoint      string // HTTP endpoint URL
	ParticipantID string // peer's participant ID in the campfire
	PublicKeyHex  string // peer's public key (hex-encoded Ed25519)
}

// ErrTransportNotSupported is returned when the operation requires HTTP transport
// but the client is using filesystem or github transport.
var ErrTransportNotSupported = errors.New("operation requires HTTP transport")

// AddPeer registers a peer endpoint for a P2P HTTP campfire. The peer's
// endpoint is persisted in the store so subsequent Send/Read operations
// can reach it.
//
// Returns ErrTransportNotSupported if the campfire uses a non-HTTP transport.
func (c *Client) AddPeer(campfireID string, peer PeerInfo) error {
	if campfireID == "" {
		return fmt.Errorf("protocol.Client.AddPeer: CampfireID is required")
	}
	if peer.PublicKeyHex == "" {
		return fmt.Errorf("protocol.Client.AddPeer: PublicKeyHex is required")
	}
	if peer.Endpoint == "" {
		return fmt.Errorf("protocol.Client.AddPeer: Endpoint is required")
	}

	m, err := c.store.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("protocol.Client.AddPeer: querying membership: %w", err)
	}
	if m == nil {
		return fmt.Errorf("protocol.Client.AddPeer: not a member of campfire %s", shortID(campfireID))
	}

	if transport.ResolveType(*m) != transport.TypePeerHTTP {
		return ErrTransportNotSupported
	}

	// Parse participant ID (store uses uint32, protocol uses string).
	var participantID uint32
	if peer.ParticipantID != "" {
		var n int
		if _, err := fmt.Sscanf(peer.ParticipantID, "%d", &n); err == nil && n > 0 {
			participantID = uint32(n)
		}
	}

	return c.store.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:    campfireID,
		MemberPubkey:  peer.PublicKeyHex,
		Endpoint:      peer.Endpoint,
		ParticipantID: participantID,
	})
}

// RemovePeer removes a peer endpoint from a P2P HTTP campfire.
//
// Returns ErrTransportNotSupported if the campfire uses a non-HTTP transport.
func (c *Client) RemovePeer(campfireID string, publicKeyHex string) error {
	if campfireID == "" {
		return fmt.Errorf("protocol.Client.RemovePeer: CampfireID is required")
	}
	if publicKeyHex == "" {
		return fmt.Errorf("protocol.Client.RemovePeer: publicKeyHex is required")
	}

	m, err := c.store.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("protocol.Client.RemovePeer: querying membership: %w", err)
	}
	if m == nil {
		return fmt.Errorf("protocol.Client.RemovePeer: not a member of campfire %s", shortID(campfireID))
	}

	if transport.ResolveType(*m) != transport.TypePeerHTTP {
		return ErrTransportNotSupported
	}

	return c.store.DeletePeerEndpoint(campfireID, publicKeyHex)
}

// Peers returns the list of known peer endpoints for a P2P HTTP campfire.
//
// Returns ErrTransportNotSupported if the campfire uses a non-HTTP transport.
func (c *Client) Peers(campfireID string) ([]PeerInfo, error) {
	if campfireID == "" {
		return nil, fmt.Errorf("protocol.Client.Peers: CampfireID is required")
	}

	m, err := c.store.GetMembership(campfireID)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Peers: querying membership: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("protocol.Client.Peers: not a member of campfire %s", shortID(campfireID))
	}

	if transport.ResolveType(*m) != transport.TypePeerHTTP {
		return nil, ErrTransportNotSupported
	}

	endpoints, err := c.store.ListPeerEndpoints(campfireID)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Peers: listing peer endpoints: %w", err)
	}

	peers := make([]PeerInfo, len(endpoints))
	for i, ep := range endpoints {
		pid := ""
		if ep.ParticipantID > 0 {
			pid = fmt.Sprintf("%d", ep.ParticipantID)
		}
		peers[i] = PeerInfo{
			Endpoint:      ep.Endpoint,
			ParticipantID: pid,
			PublicKeyHex:  ep.MemberPubkey,
		}
	}

	return peers, nil
}
