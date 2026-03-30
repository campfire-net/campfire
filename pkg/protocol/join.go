package protocol

// join.go — protocol.Client.Join() and Client.Admit().
//
// Covered bead: campfire-agent-ykv
//
// Join admits the calling agent to a campfire identified by campfireID and transport
// information. Three transports are supported:
//   - Filesystem: read state from disk, enforce invite-only, write member record
//   - P2P HTTP: contact an admitting peer endpoint via cfhttp.Join
//   - GitHub: not implemented here (handled by cmd/cf/cmd/join.go)
//
// Admit writes a member record for a third party to the filesystem transport
// directory, enabling invite-only pre-admission.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/admission"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// JoinRequest holds parameters for Client.Join().
type JoinRequest struct {
	// CampfireID is the hex-encoded campfire public key. Required.
	CampfireID string

	// TransportDir is the transport directory for the campfire.
	// For filesystem transport: this is the campfire-specific directory that
	// contains campfire.cbor, members/, and messages/ directly (path-rooted mode).
	// For p2p-http transport: campfire state file is written to TransportDir/{campfireID}.cbor.
	// Required for both filesystem and p2p-http transports.
	TransportDir string

	// TransportType selects the transport: "filesystem" (default) or "p2p-http".
	TransportType string

	// PeerEndpoint is the HTTP endpoint of an existing campfire member to join through.
	// Required when TransportType is "p2p-http".
	// Example: "http://127.0.0.1:9001"
	PeerEndpoint string

	// MyHTTPEndpoint is this agent's own HTTP endpoint (optional, for P2P HTTP).
	// When set, it is registered so peers can deliver messages to us.
	MyHTTPEndpoint string

	// HTTPTransport is the P2P HTTP Transport instance to register with.
	// Optional for p2p-http transport. When set, the joiner's self-info is registered
	// on the transport so it can receive delivered messages.
	HTTPTransport *cfhttp.Transport
}

// JoinResult is returned from Client.Join() on success.
type JoinResult struct {
	// CampfireID is the hex-encoded campfire public key of the joined campfire.
	CampfireID string

	// JoinProtocol is the join protocol of the campfire ("open" or "invite-only").
	JoinProtocol string

	// TransportDir is the transport directory recorded in the membership.
	TransportDir string
}

// Join joins the campfire described by req. The calling agent becomes a member,
// syncs existing messages from the transport, and returns a JoinResult.
//
// For filesystem transport, the campfire state must already exist at TransportDir.
// For P2P HTTP, the JoinRequest is forwarded to PeerEndpoint and the campfire
// state is received and stored locally.
//
// Returns an error for:
//   - invite-only campfires when the caller has not been pre-admitted
//   - transport errors
//   - invalid campfire state
func (c *Client) Join(req JoinRequest) (*JoinResult, error) {
	if c.identity == nil {
		return nil, fmt.Errorf("protocol.Client.Join: identity required")
	}
	if req.CampfireID == "" {
		return nil, fmt.Errorf("protocol.Client.Join: CampfireID is required")
	}

	tt := req.TransportType
	if tt == "" {
		tt = "filesystem"
	}

	switch tt {
	case "p2p-http":
		return c.joinP2PHTTP(req)
	default:
		return c.joinFilesystem(req)
	}
}

// joinFilesystem joins a campfire via the filesystem transport.
func (c *Client) joinFilesystem(req JoinRequest) (*JoinResult, error) {
	if req.TransportDir == "" {
		return nil, fmt.Errorf("protocol.Client.Join: TransportDir is required for filesystem transport")
	}

	// TransportDir is the campfire-specific dir (path-rooted mode):
	// CampfireDir() returns TransportDir directly (campfire.cbor lives at TransportDir/campfire.cbor).
	tr := fs.ForDir(req.TransportDir)

	// Read campfire state.
	state, err := tr.ReadState(req.CampfireID)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Join: reading campfire state: %w", err)
	}

	// Check existing member list for invite-only enforcement.
	existingMembers, err := tr.ListMembers(req.CampfireID)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Join: listing members: %w", err)
	}

	alreadyOnDisk := false
	existingRole := campfire.RoleFull
	for _, m := range existingMembers {
		if fmt.Sprintf("%x", m.PublicKey) == c.identity.PublicKeyHex() {
			alreadyOnDisk = true
			if m.Role != "" {
				existingRole = m.Role
			}
			break
		}
	}

	// Enforce invite-only: reject callers that are not pre-admitted.
	if !alreadyOnDisk {
		switch state.JoinProtocol {
		case "open":
			// Immediately admit below.
		case "invite-only":
			return nil, fmt.Errorf("protocol.Client.Join: campfire %s is invite-only; ask a member to call Admit first",
				shortID(req.CampfireID))
		default:
			return nil, fmt.Errorf("protocol.Client.Join: unknown join protocol: %s", state.JoinProtocol)
		}
	}

	effectiveRole := existingRole

	// Admit via shared admission package.
	var fstr admission.FSTransport
	if !alreadyOnDisk {
		fstr = tr
	}
	_, err = admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		FSTransport: fstr,
		Store:       c.store,
	}, admission.AdmissionRequest{
		CampfireID:      req.CampfireID,
		MemberPubKeyHex: c.identity.PublicKeyHex(),
		Role:            effectiveRole,
		JoinProtocol:    state.JoinProtocol,
		TransportDir:    tr.CampfireDir(req.CampfireID),
		TransportType:   "filesystem",
	})
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Join: admitting member: %w", err)
	}

	// Write campfire:member-joined system message (skip if already on disk).
	if !alreadyOnDisk {
		now := time.Now().UnixNano()
		updatedMembers, _ := tr.ListMembers(req.CampfireID)
		cfObj := state.ToCampfire(updatedMembers)
		sysMsg, sysMsgErr := message.NewMessage(
			state.PrivateKey, state.PublicKey,
			[]byte(fmt.Sprintf(`{"member":"%s","joined_at":%d}`, c.identity.PublicKeyHex(), now)),
			[]string{"campfire:member-joined"},
			nil,
		)
		if sysMsgErr == nil {
			if hopErr := sysMsg.AddHop(
				state.PrivateKey, state.PublicKey,
				cfObj.MembershipHash(), len(updatedMembers),
				state.JoinProtocol, state.ReceptionRequirements,
				campfire.RoleFull,
			); hopErr == nil {
				tr.WriteMessage(req.CampfireID, sysMsg) //nolint:errcheck
			}
		}
	}

	// Sync messages from transport into local store (trust comparison + convention sync).
	m, err := c.store.GetMembership(req.CampfireID)
	if err == nil && m != nil {
		c.syncIfFilesystem(req.CampfireID) //nolint:errcheck
	}

	return &JoinResult{
		CampfireID:   req.CampfireID,
		JoinProtocol: state.JoinProtocol,
		TransportDir: tr.CampfireDir(req.CampfireID),
	}, nil
}

// joinP2PHTTP joins a campfire via the P2P HTTP transport by contacting PeerEndpoint.
func (c *Client) joinP2PHTTP(req JoinRequest) (*JoinResult, error) {
	if req.PeerEndpoint == "" {
		return nil, fmt.Errorf("protocol.Client.Join: PeerEndpoint is required for p2p-http transport")
	}
	if req.TransportDir == "" {
		return nil, fmt.Errorf("protocol.Client.Join: TransportDir is required for p2p-http transport")
	}

	// Contact the admitting peer.
	result, err := cfhttp.Join(req.PeerEndpoint, req.CampfireID, c.identity, req.MyHTTPEndpoint)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Join: joining via %s: %w", req.PeerEndpoint, err)
	}

	// Persist campfire state CBOR to TransportDir/{campfireID}.cbor.
	if err := os.MkdirAll(req.TransportDir, 0700); err != nil {
		return nil, fmt.Errorf("protocol.Client.Join: creating state directory: %w", err)
	}

	cfState := campfire.CampfireState{
		PublicKey:             result.CampfirePubKey,
		PrivateKey:            result.CampfirePrivKey,
		JoinProtocol:          result.JoinProtocol,
		ReceptionRequirements: result.ReceptionRequirements,
		Threshold:             result.Threshold,
	}
	stateData, err := cfencoding.Marshal(cfState)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Join: encoding campfire state: %w", err)
	}
	statePath := filepath.Join(req.TransportDir, req.CampfireID+".cbor")
	if err := os.WriteFile(statePath, stateData, 0600); err != nil {
		return nil, fmt.Errorf("protocol.Client.Join: writing campfire state: %w", err)
	}

	// Register joiner's self-info on the HTTP transport (if provided).
	if req.HTTPTransport != nil && req.MyHTTPEndpoint != "" {
		req.HTTPTransport.SetSelfInfo(c.identity.PublicKeyHex(), req.MyHTTPEndpoint)
	}

	// Admit self via admission package (store-only, no filesystem member file for P2P).
	_, err = admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		Store: c.store,
	}, admission.AdmissionRequest{
		CampfireID:      req.CampfireID,
		MemberPubKeyHex: c.identity.PublicKeyHex(),
		Role:            campfire.RoleFull,
		JoinProtocol:    result.JoinProtocol,
		TransportDir:    req.TransportDir,
		TransportType:   "p2p-http",
	})
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Join: recording membership: %w", err)
	}

	// Store peer endpoints from the join response.
	for _, peer := range result.Peers {
		if peer.PubKeyHex != "" && peer.Endpoint != "" {
			c.store.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
				CampfireID:    req.CampfireID,
				MemberPubkey:  peer.PubKeyHex,
				Endpoint:      peer.Endpoint,
				ParticipantID: peer.ParticipantID,
			})
		}
	}

	// Store convention declarations from the join response so B can read them immediately.
	for _, decl := range result.Declarations {
		ts := decl.Timestamp
		if ts == 0 {
			ts = store.NowNano()
		}
		rec := store.MessageRecord{
			CampfireID: req.CampfireID,
			ID:         decl.ID,
			Payload:    decl.Payload,
			Tags:       decl.Tags,
			Timestamp:  ts,
		}
		c.store.AddMessage(rec) //nolint:errcheck
	}

	return &JoinResult{
		CampfireID:   req.CampfireID,
		JoinProtocol: result.JoinProtocol,
		TransportDir: req.TransportDir,
	}, nil
}

// AdmitRequest holds parameters for Client.Admit().
type AdmitRequest struct {
	// CampfireID is the hex-encoded campfire public key. Required.
	CampfireID string

	// MemberPubKeyHex is the hex-encoded Ed25519 public key of the member to admit.
	// Required.
	MemberPubKeyHex string

	// Role is the role to assign to the admitted member.
	// Defaults to campfire.RoleFull when empty.
	Role string

	// TransportDir is the filesystem transport directory for the campfire.
	// Required for filesystem transport.
	TransportDir string
}

// Admit pre-admits a member to an invite-only campfire by writing their
// member record to the filesystem transport directory. After Admit, the
// target can call Join() and will be allowed in.
//
// This is the filesystem-transport equivalent of the server-side pre-admission
// in the P2P HTTP transport.
func (c *Client) Admit(req AdmitRequest) error {
	if req.CampfireID == "" {
		return fmt.Errorf("protocol.Client.Admit: CampfireID is required")
	}
	if req.MemberPubKeyHex == "" {
		return fmt.Errorf("protocol.Client.Admit: MemberPubKeyHex is required")
	}
	if req.TransportDir == "" {
		return fmt.Errorf("protocol.Client.Admit: TransportDir is required")
	}

	role := req.Role
	if role == "" {
		role = campfire.RoleFull
	}

	tr := fs.ForDir(req.TransportDir)
	_, err := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		FSTransport: tr,
		// Store is not passed here: Admit only writes the transport-layer member file.
		// The joiner's own store is updated when they call Join().
		Store: &noopStore{},
	}, admission.AdmissionRequest{
		CampfireID:      req.CampfireID,
		MemberPubKeyHex: req.MemberPubKeyHex,
		Role:            role,
		TransportDir:    req.TransportDir,
		TransportType:   "filesystem",
	})
	return err
}

// noopStore is a no-op implementation of admission.Store used by Admit()
// when we only need to write the filesystem member record and not record
// membership in the local store.
type noopStore struct{}

func (n *noopStore) AddMembership(store.Membership) error         { return nil }
func (n *noopStore) UpsertPeerEndpoint(store.PeerEndpoint) error  { return nil }
