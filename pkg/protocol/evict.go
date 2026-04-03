package protocol

// evict.go — protocol.Client.Evict()
//
// Covered bead: campfire-agent-2sa
//
// Evict removes a member from a campfire. For threshold>1 P2P HTTP campfires,
// it re-runs DKG with the remaining members, producing a new group keypair
// (campfire:rekey). The evicted member's old threshold share is useless after
// rekey — their old group key no longer matches the campfire's new group key.

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	"github.com/campfire-net/campfire/pkg/transport"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// EvictRequest holds parameters for Client.Evict().
type EvictRequest struct {
	// CampfireID is the hex-encoded campfire public key. Required.
	CampfireID string

	// MemberPubKeyHex is the hex-encoded Ed25519 public key of the member to evict. Required.
	MemberPubKeyHex string

	// Transport is required for P2P HTTP campfires with threshold>1 so that
	// the key provider can be updated after rekey. Pass a P2PHTTPTransport.
	// For filesystem campfires, Transport may be nil.
	Transport Transport
}

// EvictResult holds the outcome of a successful Evict() call.
type EvictResult struct {
	// NewCampfireID is the new campfire ID after rekey (only set when threshold>1).
	// Empty for filesystem/threshold=1 evictions.
	NewCampfireID string

	// Rekeyed is true when DKG was re-run and a new campfire keypair was created.
	Rekeyed bool
}

// Evict removes a member from the campfire identified by req.CampfireID.
//
// For filesystem transport:
//   - Removes the member's record file from the transport directory.
//   - Removes the member's peer endpoint from the store.
//
// For P2P HTTP transport with threshold=1:
//   - Removes the peer endpoint from the transport and store.
//
// For P2P HTTP transport with threshold>1:
//   - Removes the peer endpoint from the transport and store.
//   - Re-runs DKG with the remaining members (new group keypair = new campfire ID).
//   - Updates the campfire state CBOR file with the new group public key.
//   - Updates all store records from old campfire ID to new campfire ID.
//   - Stores the caller's new threshold share.
//   - Stores pending threshold shares for remaining members.
//
// Evicting self (caller's own pubkey) is rejected with an error.
func (c *Client) Evict(req EvictRequest) (*EvictResult, error) {
	if c.identity == nil {
		return nil, fmt.Errorf("protocol.Client.Evict: identity required")
	}
	if req.CampfireID == "" {
		return nil, fmt.Errorf("protocol.Client.Evict: CampfireID is required")
	}
	if req.MemberPubKeyHex == "" {
		return nil, fmt.Errorf("protocol.Client.Evict: MemberPubKeyHex is required")
	}

	// Reject self-eviction.
	if req.MemberPubKeyHex == c.identity.PublicKeyHex() {
		return nil, fmt.Errorf("protocol.Client.Evict: cannot evict self")
	}

	m, err := c.store.GetMembership(req.CampfireID)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: querying membership: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("protocol.Client.Evict: not a member of campfire %s", shortID(req.CampfireID))
	}

	switch transport.ResolveType(*m) {
	case transport.TypePeerHTTP:
		return c.evictP2PHTTP(req, m)
	default:
		return c.evictFilesystem(req, m)
	}
}

// evictFilesystem removes a member from a filesystem-transport campfire.
func (c *Client) evictFilesystem(req EvictRequest, m *store.Membership) (*EvictResult, error) {
	tr := fs.ForDir(m.TransportDir)

	// Decode the member pubkey hex to bytes.
	memberPubKeyBytes, err := hex.DecodeString(req.MemberPubKeyHex)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: decoding member pubkey: %w", err)
	}

	// Remove the member's record file from the filesystem transport directory.
	if err := tr.RemoveMember(req.CampfireID, memberPubKeyBytes); err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: removing member from transport: %w", err)
	}

	// Remove peer endpoint from store (best-effort; may not exist for filesystem).
	c.store.DeletePeerEndpoint(req.CampfireID, req.MemberPubKeyHex) //nolint:errcheck

	return &EvictResult{}, nil
}

// evictP2PHTTP removes a member from a P2P HTTP campfire.
// For threshold>1, re-runs DKG and rekeys the campfire.
func (c *Client) evictP2PHTTP(req EvictRequest, m *store.Membership) (*EvictResult, error) {
	// Remove from in-memory transport routing table.
	if httpTr := p2pHTTPTransportFrom(req.Transport); httpTr != nil {
		httpTr.RemovePeer(req.CampfireID, req.MemberPubKeyHex)
	}

	// Remove from store's peer endpoints.
	if err := c.store.DeletePeerEndpoint(req.CampfireID, req.MemberPubKeyHex); err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: deleting peer endpoint: %w", err)
	}

	// For threshold<=1, no DKG re-run needed.
	if m.Threshold <= 1 {
		return &EvictResult{}, nil
	}

	// Threshold>1: re-run DKG with remaining members.
	return c.rekeyAfterEvict(req, m)
}

// rekeyAfterEvict re-runs DKG with the remaining members (excluding the evicted one)
// and updates all store records to use the new campfire ID.
func (c *Client) rekeyAfterEvict(req EvictRequest, m *store.Membership) (*EvictResult, error) {
	oldCampfireID := req.CampfireID

	// Get remaining peers (after deletion of evicted peer).
	remainingPeers, err := c.store.ListPeerEndpoints(oldCampfireID)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: listing remaining peers: %w", err)
	}

	numRemaining := len(remainingPeers)
	if numRemaining < 1 {
		return nil, fmt.Errorf("protocol.Client.Evict: no members remain after eviction")
	}

	// New threshold: min(old_threshold, numRemaining). If fewer members than
	// old threshold, reduce the threshold.
	newThreshold := int(m.Threshold)
	if newThreshold > numRemaining {
		newThreshold = numRemaining
	}

	// Build new sequential participant ID list (1..N).
	newParticipantIDs := make([]uint32, numRemaining)
	for i := range newParticipantIDs {
		newParticipantIDs[i] = uint32(i + 1)
	}

	// Run DKG in-process with the remaining participant count.
	dkgResults, err := threshold.RunDKG(newParticipantIDs, newThreshold)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: running DKG: %w", err)
	}

	// The new campfire ID is the group public key from the new DKG.
	newGroupPub := dkgResults[1].GroupPublicKey()
	newCampfireID := fmt.Sprintf("%x", newGroupPub)

	// Validate TransportDir before any filesystem access.
	transportDir, err := sanitizeTransportDir(m.TransportDir)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: invalid transport dir: %w", err)
	}

	// Read the old campfire state to preserve join protocol and reception requirements.
	statePath := filepath.Join(transportDir, oldCampfireID+".cbor")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: reading old campfire state: %w", err)
	}
	var oldState campfire.CampfireState
	if err := cfencoding.Unmarshal(stateData, &oldState); err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: decoding old campfire state: %w", err)
	}

	// Write new campfire state CBOR with new group public key.
	// In threshold mode, cfState.PrivateKey is not used for signing (FROST handles that),
	// but sendP2PHTTP reads the CBOR file and needs a valid state. We preserve the old
	// private key bytes in the state (they remain unused).
	newState := campfire.CampfireState{
		PublicKey:             newGroupPub,
		PrivateKey:            oldState.PrivateKey,
		JoinProtocol:          oldState.JoinProtocol,
		ReceptionRequirements: oldState.ReceptionRequirements,
		Threshold:             uint(newThreshold),
	}
	newStateData, err := cfencoding.Marshal(newState)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: encoding new campfire state: %w", err)
	}
	newStatePath := filepath.Join(transportDir, newCampfireID+".cbor")
	if err := atomicWriteFile(newStatePath, newStateData); err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: writing new campfire state: %w", err)
	}

	// Find caller's new participant ID (position in remaining peer list, 1-indexed).
	callerPubHex := c.identity.PublicKeyHex()
	var myNewParticipantID uint32
	for i, p := range remainingPeers {
		if p.MemberPubkey == callerPubHex {
			myNewParticipantID = uint32(i + 1)
			break
		}
	}
	if myNewParticipantID == 0 {
		return nil, fmt.Errorf("protocol.Client.Evict: caller not found in remaining peers after eviction")
	}

	// Migrate all store records from old campfire ID to new campfire ID.
	// UpdateCampfireID renames rows in: campfire_memberships, peer_endpoints,
	// threshold_shares, pending_threshold_shares, messages, read_cursors, filters,
	// epoch secrets, invites.
	if err := c.store.UpdateCampfireID(oldCampfireID, newCampfireID); err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: updating campfire ID in store: %w", err)
	}

	// Store caller's new threshold share (overwrites the old one now under newCampfireID).
	myShare, err := threshold.MarshalResult(myNewParticipantID, dkgResults[myNewParticipantID])
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: serializing caller share: %w", err)
	}
	if err := c.store.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    newCampfireID,
		ParticipantID: myNewParticipantID,
		SecretShare:   myShare,
	}); err != nil {
		return nil, fmt.Errorf("protocol.Client.Evict: storing caller threshold share: %w", err)
	}

	// Update peer endpoints for all remaining peers with new participant IDs,
	// and store pending DKG shares for non-self remaining members.
	for i, peer := range remainingPeers {
		newPID := uint32(i + 1)
		if err := c.store.UpsertPeerEndpoint(store.PeerEndpoint{
			CampfireID:    newCampfireID,
			MemberPubkey:  peer.MemberPubkey,
			Endpoint:      peer.Endpoint,
			ParticipantID: newPID,
			Role:          peer.Role,
		}); err != nil {
			return nil, fmt.Errorf("protocol.Client.Evict: updating peer endpoint for %s: %w",
				shortID(peer.MemberPubkey), err)
		}

		// Store pending DKG share for non-self peers so they can claim on next interaction.
		if peer.MemberPubkey != callerPubHex {
			peerShare, err := threshold.MarshalResult(newPID, dkgResults[newPID])
			if err != nil {
				return nil, fmt.Errorf("protocol.Client.Evict: serializing share for peer %d: %w", newPID, err)
			}
			if err := c.store.StorePendingThresholdShare(newCampfireID, newPID, peerShare); err != nil {
				return nil, fmt.Errorf("protocol.Client.Evict: storing pending share for peer %d: %w", newPID, err)
			}
		}
	}

	// Update the transport's key provider and threshold share provider if provided.
	if httpTr := p2pHTTPTransportFrom(req.Transport); httpTr != nil {
		newPubKey := []byte(newGroupPub)
		newPrivKey := oldState.PrivateKey
		newCfID := newCampfireID
		httpTr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
			if id == newCfID {
				return newPrivKey, newPubKey, nil
			}
			return nil, nil, fmt.Errorf("campfire %s not hosted on this node", shortID(id))
		})
		s := c.store
		httpTr.SetThresholdShareProvider(func(id string) (uint32, []byte, error) {
			share, err := s.GetThresholdShare(id)
			if err != nil {
				return 0, nil, err
			}
			if share == nil {
				return 0, nil, fmt.Errorf("no threshold share for campfire %s", shortID(id))
			}
			return share.ParticipantID, share.SecretShare, nil
		})
	}

	return &EvictResult{
		NewCampfireID: newCampfireID,
		Rekeyed:       true,
	}, nil
}

// p2pHTTPTransportFrom extracts the underlying *cfhttp.Transport from a Transport
// interface value. Returns nil if t is nil or not a P2PHTTPTransport.
func p2pHTTPTransportFrom(t Transport) *cfhttp.Transport {
	switch v := t.(type) {
	case *P2PHTTPTransport:
		return v.Transport
	case P2PHTTPTransport:
		return v.Transport
	}
	return nil
}
