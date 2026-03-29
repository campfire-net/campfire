// Package admission implements the AdmitMember operation — recording a new
// member in the store, optionally writing a filesystem member file, and
// registering the peer's HTTP endpoint for delivery.
//
// Callers are responsible for convention replication, key exchange, and
// delivery mode logic. This package handles only the storage side.
package admission

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	campfire "github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/store"
)

// FSTransport is the minimal interface for writing a member record to the
// filesystem transport directory.
type FSTransport interface {
	WriteMember(campfireID string, member campfire.MemberRecord) error
}

// Store is the minimal interface for recording membership and peer endpoints.
type Store interface {
	AddMembership(store.Membership) error
	UpsertPeerEndpoint(store.PeerEndpoint) error
}

// HTTPTransport is the minimal interface for notifying the in-process HTTP
// transport layer of a new peer.
type HTTPTransport interface {
	AddPeer(campfireID, pubKeyHex, endpoint string)
}

// AdmitterDeps holds the injected dependencies for AdmitMember.
// FSTransport and HTTPTransport are optional (nil-safe). Store is required.
type AdmitterDeps struct {
	// FSTransport writes member records to the filesystem transport directory.
	// Optional — if nil, no member file is written.
	FSTransport FSTransport

	// Store records membership and peer endpoint data. Required.
	Store Store

	// HTTPTransport notifies the in-process HTTP transport of new peers.
	// Optional — if nil, AddPeer is not called but the store record is still written.
	HTTPTransport HTTPTransport

	// ExternalAddr is this node's externally reachable address, available for
	// callers that need it. AdmitMember does not use it directly.
	ExternalAddr string
}

// AdmissionRequest carries all inputs needed to admit a member.
type AdmissionRequest struct {
	CampfireID      string
	MemberPubKeyHex string // hex-encoded Ed25519 public key
	Endpoint        string // HTTP endpoint for peer delivery; empty = no peer endpoint
	Role            string // explicit role; empty = derive from Encrypted flag
	Encrypted       bool
	Source          string
	ParticipantID   uint32
	JoinProtocol    string
	TransportDir    string
	TransportType   string
	Description     string
	CreatorPubkey   string
}

// AdmissionResult reports what was done during admission.
type AdmissionResult struct {
	MemberFileWritten      bool
	MembershipRecorded     bool
	PeerEndpointRegistered bool
	EffectiveRole          string
}

// AdmitMember records a new member in the store, optionally writes a member
// file via FSTransport, and registers the peer endpoint when an Endpoint is
// provided.
//
// Error handling: if any required write fails, the error is returned
// immediately and no further writes are attempted.
func AdmitMember(_ context.Context, deps AdmitterDeps, req AdmissionRequest) (AdmissionResult, error) {
	var result AdmissionResult

	// Determine effective role.
	if req.Role != "" {
		result.EffectiveRole = req.Role
	} else if req.Encrypted {
		result.EffectiveRole = campfire.RoleBlindRelay
	} else {
		result.EffectiveRole = campfire.RoleFull
	}

	// Write filesystem member file if FSTransport is available.
	if deps.FSTransport != nil {
		pubKeyBytes, err := hex.DecodeString(req.MemberPubKeyHex)
		if err != nil {
			return result, fmt.Errorf("admission: decoding member public key: %w", err)
		}
		memberRecord := campfire.MemberRecord{
			PublicKey: pubKeyBytes,
			JoinedAt:  time.Now().Unix(),
			Role:      result.EffectiveRole,
		}
		if err := deps.FSTransport.WriteMember(req.CampfireID, memberRecord); err != nil {
			return result, fmt.Errorf("admission: writing member file: %w", err)
		}
		result.MemberFileWritten = true
	}

	// Record membership in store.
	ms := store.Membership{
		CampfireID:    req.CampfireID,
		TransportDir:  req.TransportDir,
		JoinProtocol:  req.JoinProtocol,
		Role:          result.EffectiveRole,
		JoinedAt:      time.Now().Unix(),
		Description:   req.Description,
		CreatorPubkey: req.CreatorPubkey,
		TransportType: req.TransportType,
		Encrypted:     req.Encrypted,
	}
	if err := deps.Store.AddMembership(ms); err != nil {
		return result, fmt.Errorf("admission: recording membership: %w", err)
	}
	result.MembershipRecorded = true

	// Register peer endpoint if an endpoint address was provided.
	if req.Endpoint != "" {
		pe := store.PeerEndpoint{
			CampfireID:    req.CampfireID,
			MemberPubkey:  req.MemberPubKeyHex,
			Endpoint:      req.Endpoint,
			ParticipantID: req.ParticipantID,
		}
		if err := deps.Store.UpsertPeerEndpoint(pe); err != nil {
			return result, fmt.Errorf("admission: upserting peer endpoint: %w", err)
		}
		if deps.HTTPTransport != nil {
			deps.HTTPTransport.AddPeer(req.CampfireID, req.MemberPubKeyHex, req.Endpoint)
		}
		result.PeerEndpointRegistered = true
	}

	return result, nil
}
