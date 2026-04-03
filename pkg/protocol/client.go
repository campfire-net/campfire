// Package protocol provides a unified client API for campfire operations.
// It consolidates the duplicate read/send stacks that exist in cmd/cf, cmd/cf-mcp,
// and pkg/convention, enabling those callers to migrate to a shared implementation.
//
// Downstream items (campfire-agent-zkg, r02, f4a) will migrate those callers
// to use Client.Send() and Client.Read() directly.
package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	"github.com/campfire-net/campfire/pkg/transport"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	ghtr "github.com/campfire-net/campfire/pkg/transport/github"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/google/uuid"
)

// SigningMode controls how the provenance hop is signed.
type SigningMode int

const (
	// SigningModeMemberKey signs the provenance hop with the campfire's own key (default).
	// This is the standard mode for filesystem and GitHub transports, and for
	// P2P HTTP campfires with threshold=1.
	SigningModeMemberKey SigningMode = iota

	// SigningModeCampfireKey is an alias for SigningModeMemberKey. The campfire
	// private key is always read from the transport state file.
	SigningModeCampfireKey

	// SigningModeThreshold uses FROST threshold signing for campfires with threshold>1.
	// Only applicable for P2P HTTP transport. Falls back to SigningModeMemberKey
	// when threshold<=1.
	SigningModeThreshold
)

// SendRequest holds all parameters for a single Client.Send() call.
type SendRequest struct {
	// CampfireID is the hex-encoded campfire public key.
	CampfireID string

	// Payload is the message body (UTF-8 text or binary).
	Payload []byte

	// Tags is the list of message tags (e.g. "status", "future", "fulfills").
	Tags []string

	// Antecedents is the list of message IDs this message is a reply to.
	Antecedents []string

	// Instance is tainted (sender-asserted) role metadata — NOT covered by signature.
	Instance string

	// SigningMode controls how the provenance hop is signed. Defaults to
	// SigningModeMemberKey, which is correct for filesystem and GitHub transports
	// and P2P HTTP with threshold=1.
	SigningMode SigningMode

	// GitHubToken is required when the campfire uses the GitHub transport.
	// If empty, the caller is responsible for injecting a token via the
	// environment (GITHUB_TOKEN) before calling Send.
	GitHubToken string

	// RoleOverride, when non-empty, overrides the membership role recorded in the
	// provenance hop. Used by Bridge() to force "blind-relay" hops regardless of
	// the sender's actual membership role. This makes IsBridged() return true for
	// messages forwarded by a bridge. If empty, the sender's stored membership role
	// is used (the default and correct behavior for non-bridge sends).
	RoleOverride string
}

// CoSigner is a peer endpoint used during FROST threshold signing.
// It matches cfhttp.CoSigner to avoid a circular dependency.
type CoSigner struct {
	Endpoint      string
	ParticipantID uint32
}

// Client is a high-level campfire client that wraps a store and optional identity
// to provide campfire operations with correct sync-before-query semantics.
//
// For filesystem-transport campfires, operations sync from the filesystem into
// the local store before querying. For HTTP-transport campfires, messages are
// delivered via push, so sync is skipped.
//
// Client is NOT safe for concurrent use from multiple goroutines without external
// synchronization. Each goroutine should use its own Client.
type Client struct {
	store     store.Store
	identity  *identity.Identity
	opts      options
	configDir string // set by Init(); empty when using New() directly
}

// New creates a Client wrapping the given store.
// identity may be nil for read-only clients that do not need to sign messages.
func New(s store.Store, id *identity.Identity) *Client {
	return &Client{store: s, identity: id, opts: defaultOptions()}
}

// GetMembership returns the membership record for the given campfire ID,
// or nil if the client is not a member. Used by naming auto-join to check
// whether the client has already joined a campfire before attempting to join.
func (c *Client) GetMembership(campfireID string) (*store.Membership, error) {
	return c.store.GetMembership(campfireID)
}

// PublicKeyHex returns the hex-encoded public key of the client's identity.
// Returns an empty string if the client has no identity (read-only).
func (c *Client) PublicKeyHex() string {
	if c.identity == nil {
		return ""
	}
	return c.identity.PublicKeyHex()
}

// Send creates a signed message and delivers it via the transport that backs
// the campfire. The caller must already be a member of the campfire (membership
// record present in the store). Role enforcement is applied before sending.
//
// Returns the created message on success. On failure returns a descriptive error;
// role enforcement errors satisfy errors.As(*RoleError).
func (c *Client) Send(req SendRequest) (*message.Message, error) {
	if c.identity == nil {
		return nil, fmt.Errorf("identity required to send messages")
	}

	m, err := c.store.GetMembership(req.CampfireID)
	if err != nil {
		return nil, fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("not a member of campfire %s", shortID(req.CampfireID))
	}

	if err := checkRoleCanSend(m.Role, req.Tags); err != nil {
		return nil, err
	}

	switch transport.ResolveType(*m) {
	case transport.TypeGitHub:
		return c.sendGitHub(req, m)
	case transport.TypePeerHTTP:
		return c.sendP2PHTTP(req, m)
	default:
		return c.sendFilesystem(req, m)
	}
}

// sendFilesystem delivers req via the local filesystem transport.
func (c *Client) sendFilesystem(req SendRequest, m *store.Membership) (*message.Message, error) {
	tr := fs.ForDir(m.TransportDir)

	// Verify sender is a member in the transport directory.
	members, err := tr.ListMembers(req.CampfireID)
	if err != nil {
		return nil, fmt.Errorf("listing members: %w", err)
	}
	if !isMember(members, c.identity.PublicKeyHex()) {
		return nil, fmt.Errorf("not recognized as a member in the transport directory")
	}

	msg, err := message.NewMessage(c.identity.PrivateKey, c.identity.PublicKey, req.Payload, req.Tags, req.Antecedents)
	if err != nil {
		return nil, fmt.Errorf("creating message: %w", err)
	}
	msg.Instance = req.Instance

	state, err := tr.ReadState(req.CampfireID)
	if err != nil {
		return nil, fmt.Errorf("reading campfire state: %w", err)
	}

	cf := state.ToCampfire(members)
	hopRole := campfire.EffectiveRole(m.Role)
	if req.RoleOverride != "" {
		hopRole = req.RoleOverride
	}
	if err := msg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(members),
		state.JoinProtocol, state.ReceptionRequirements,
		hopRole,
	); err != nil {
		return nil, fmt.Errorf("adding provenance hop: %w", err)
	}

	if err := tr.WriteMessage(req.CampfireID, msg); err != nil {
		return nil, fmt.Errorf("writing message: %w", err)
	}

	// Mirror to local store so the sender can read back their own messages
	// without a sync step. Consistent with sendP2PHTTP behavior.
	c.store.AddMessage(store.MessageRecordFromMessage(req.CampfireID, msg, store.NowNano())) //nolint:errcheck

	return msg, nil
}

// githubTransportMeta holds the parsed metadata from a GitHub transport dir.
type githubTransportMeta struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	BaseURL     string `json:"base_url,omitempty"`
}

// parseGitHubTransportDir parses the TransportDir value for a GitHub-transport campfire.
func parseGitHubTransportDir(transportDir string) (githubTransportMeta, bool) {
	const prefix = "github:"
	if !strings.HasPrefix(transportDir, prefix) {
		return githubTransportMeta{}, false
	}
	raw := strings.TrimPrefix(transportDir, prefix)
	var meta githubTransportMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return githubTransportMeta{}, false
	}
	return meta, true
}

// sendGitHub delivers req via the GitHub Issues transport.
func (c *Client) sendGitHub(req SendRequest, m *store.Membership) (*message.Message, error) {
	meta, ok := parseGitHubTransportDir(m.TransportDir)
	if !ok {
		return nil, fmt.Errorf("invalid GitHub transport dir: %s", m.TransportDir)
	}

	// Resolve GitHub token: use the one in the request, then fall back to env.
	token := req.GitHubToken
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("GitHub token required: set GitHubToken in SendRequest or GITHUB_TOKEN env var")
	}

	cfg := ghtr.Config{
		Repo:        meta.Repo,
		IssueNumber: meta.IssueNumber,
		Token:       token,
		BaseURL:     meta.BaseURL,
	}
	tr, err := ghtr.New(cfg, c.store)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub transport: %w", err)
	}
	tr.RegisterCampfire(req.CampfireID, meta.IssueNumber)

	msg, err := message.NewMessage(c.identity.PrivateKey, c.identity.PublicKey, req.Payload, req.Tags, req.Antecedents)
	if err != nil {
		return nil, fmt.Errorf("creating message: %w", err)
	}
	msg.Instance = req.Instance

	// Add provenance hop signed by the campfire key.
	// The campfire private key is stored in the membership record at create/join time
	// because the GitHub transport has no on-disk state directory (unlike filesystem
	// and P2P HTTP transports which read campfire.cbor from the transport dir).
	if m.CampfirePrivKey != "" {
		cfPrivBytes, err := hex.DecodeString(m.CampfirePrivKey)
		if err != nil {
			return nil, fmt.Errorf("decoding campfire private key for provenance hop: %w", err)
		}
		cfPrivKey := ed25519.PrivateKey(cfPrivBytes)
		cfPubKey := cfPrivKey.Public().(ed25519.PublicKey)
		hopRole := campfire.EffectiveRole(m.Role)
		if req.RoleOverride != "" {
			hopRole = req.RoleOverride
		}
		// Member count and membership hash: GitHub transport has no on-disk member
		// file, so we derive both from the peer endpoint list in the local store.
		// If the list is empty (no peers discovered yet), fall back to a single
		// member (the sender) with an empty-set hash.
		memberCount := 1
		var ghPeers []store.PeerEndpoint
		if pp, peerErr := c.store.ListPeerEndpoints(req.CampfireID); peerErr == nil {
			ghPeers = pp
			if len(ghPeers) > 0 {
				memberCount = len(ghPeers)
			}
		}
		ghMemHash := membershipHashFromPeers(ghPeers)
		// ReceptionRequirements are not stored in the membership record; use empty
		// slice to match the GitHub transport's open-by-default join model.
		reqs := []string{}
		if err := msg.AddHop(
			cfPrivKey, cfPubKey,
			ghMemHash,
			memberCount,
			m.JoinProtocol,
			reqs,
			hopRole,
		); err != nil {
			return nil, fmt.Errorf("adding provenance hop: %w", err)
		}
	}

	if err := tr.Send(req.CampfireID, msg); err != nil {
		return nil, fmt.Errorf("sending via GitHub transport: %w", err)
	}

	// Mirror to local store so the sender can read back their own messages
	// without a sync step. Consistent with sendFilesystem and sendP2PHTTP behavior.
	c.store.AddMessage(store.MessageRecordFromMessage(req.CampfireID, msg, store.NowNano())) //nolint:errcheck

	return msg, nil
}

// sanitizeTransportDir validates that dir is a safe absolute path with no path
// traversal sequences. It returns the cleaned path or an error if dir is empty,
// not absolute, or contains ".." components in the raw value.
//
// This prevents a malicious or corrupted membership record from using a
// TransportDir like "/safe/../../../etc" to access files outside the intended
// campfire transport directory. We check the raw path before filepath.Clean
// because Clean silently resolves ".." — the raw presence of ".." indicates
// a tampered or malformed stored value.
func sanitizeTransportDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("transport dir is empty")
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("transport dir is not absolute: %q", dir)
	}
	// Check the raw path for ".." components before cleaning.
	// A legitimately-created TransportDir (set during Join via tr.CampfireDir)
	// is always a clean absolute path with no traversal components.
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("transport dir contains path traversal: %q", dir)
		}
	}
	return filepath.Clean(dir), nil
}

// membershipHashFromPeers computes a MembershipHash from a list of peer
// endpoints. It decodes each peer's hex-encoded public key, builds a Member
// slice, and delegates to campfire.Campfire.MembershipHash().
//
// Peer endpoint roles ("creator", "member") are normalised via
// campfire.EffectiveRole to their campfire equivalents before hashing so that
// the hash matches what the filesystem transport would produce for the same
// member set (spec §2.5).
//
// If peers is empty the function returns the SHA-256 hash of an empty member
// set (same as campfire.Campfire{}.MembershipHash()) rather than substituting
// the campfire public key.
func membershipHashFromPeers(peers []store.PeerEndpoint) []byte {
	members := make([]campfire.Member, 0, len(peers))
	for _, p := range peers {
		pubBytes, err := hex.DecodeString(p.MemberPubkey)
		if err != nil {
			// Skip peers with malformed public keys; they will be absent from
			// the hash rather than causing a substitution with wrong data.
			continue
		}
		members = append(members, campfire.Member{
			PublicKey: pubBytes,
			Role:      campfire.EffectiveRole(p.Role),
		})
	}
	cf := &campfire.Campfire{Members: members}
	return cf.MembershipHash()
}

// sendP2PHTTP delivers req via the P2P HTTP transport.
// For threshold<=1: signs provenance hop with the campfire key.
// For threshold>1: runs FROST signing rounds with co-signers.
func (c *Client) sendP2PHTTP(req SendRequest, m *store.Membership) (*message.Message, error) {
	transportDir, err := sanitizeTransportDir(m.TransportDir)
	if err != nil {
		return nil, fmt.Errorf("invalid transport dir: %w", err)
	}
	statePath := filepath.Join(transportDir, req.CampfireID+".cbor")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("reading campfire state: %w", err)
	}
	var cfState campfire.CampfireState
	if err := cfencoding.Unmarshal(stateData, &cfState); err != nil {
		return nil, fmt.Errorf("decoding campfire state: %w", err)
	}

	msg, err := message.NewMessage(c.identity.PrivateKey, c.identity.PublicKey, req.Payload, req.Tags, req.Antecedents)
	if err != nil {
		return nil, fmt.Errorf("creating message: %w", err)
	}
	msg.Instance = req.Instance

	peers, err := c.store.ListPeerEndpoints(req.CampfireID)
	if err != nil {
		return nil, fmt.Errorf("listing peer endpoints: %w", err)
	}

	var otherPeers []p2pPeer
	for _, p := range peers {
		if p.MemberPubkey != c.identity.PublicKeyHex() && p.Endpoint != "" {
			otherPeers = append(otherPeers, p2pPeer{endpoint: p.Endpoint, participantID: p.ParticipantID})
		}
	}

	memberCount := len(peers)
	if memberCount == 0 {
		memberCount = 1
	}

	// Compute the membership hash from the known peer list. This is the correct
	// value per spec §2.5: it must be derived from the member set, not the
	// campfire public key. Previously the public key was used as a substitute,
	// weakening provenance verification.
	memHash := membershipHashFromPeers(peers)

	reqs := cfState.ReceptionRequirements
	if reqs == nil {
		reqs = []string{}
	}

	useThreshold := m.Threshold > 1
	if !useThreshold {
		// threshold=1 or non-threshold mode: sign with campfire private key directly.
		if err := msg.AddHop(
			ed25519.PrivateKey(cfState.PrivateKey),
			ed25519.PublicKey(cfState.PublicKey),
			memHash,
			memberCount,
			cfState.JoinProtocol,
			reqs,
			campfire.RoleFull,
		); err != nil {
			return nil, fmt.Errorf("adding provenance hop: %w", err)
		}
	} else {
		sig, hopTimestamp, err := c.thresholdSignHop(msg, &cfState, memberCount, req.CampfireID, otherPeers, m.Threshold, memHash)
		if err != nil {
			return nil, fmt.Errorf("threshold signing: %w", err)
		}
		hop := message.ProvenanceHop{
			CampfireID:            cfState.PublicKey,
			MembershipHash:        memHash,
			MemberCount:           memberCount,
			JoinProtocol:          cfState.JoinProtocol,
			ReceptionRequirements: reqs,
			Timestamp:             hopTimestamp,
			Signature:             sig,
		}
		msg.Provenance = append(msg.Provenance, hop)
	}

	var peerEndpoints []string
	for i := range otherPeers {
		peerEndpoints = append(peerEndpoints, otherPeers[i].endpoint)
	}

	if len(peerEndpoints) > 0 {
		errs := cfhttp.DeliverToAll(peerEndpoints, req.CampfireID, msg, c.identity)
		for i, e := range errs {
			if e != nil {
				// Non-fatal: log to stderr as in the original CLI implementation.
				fmt.Fprintf(os.Stderr, "warning: delivery to peer %d failed: %v\n", i, e)
			}
		}
	}

	c.store.AddMessage(store.MessageRecordFromMessage(req.CampfireID, msg, store.NowNano())) //nolint:errcheck

	return msg, nil
}

// p2pPeer holds endpoint info for a peer in the P2P HTTP transport.
type p2pPeer struct {
	endpoint      string
	participantID uint32
}

// thresholdSignHop runs FROST signing rounds with co-signers to produce a
// threshold signature for the provenance hop.
// membershipHash must be the proper hash of the member set (computed from the
// peer endpoint list via membershipHashFromPeers); it is embedded in the hop
// sign input so all co-signers sign over the same membership snapshot.
func (c *Client) thresholdSignHop(
	msg *message.Message,
	cfState *campfire.CampfireState,
	memberCount int,
	campfireID string,
	peers []p2pPeer,
	thresh uint,
	membershipHash []byte,
) ([]byte, int64, error) {
	share, err := c.store.GetThresholdShare(campfireID)
	if err != nil {
		return nil, 0, fmt.Errorf("loading threshold share: %w", err)
	}
	if share == nil {
		return nil, 0, fmt.Errorf("no threshold share — DKG not completed for campfire %s", shortID(campfireID))
	}
	myParticipantID, myDKGResult, err := threshold.UnmarshalResult(share.SecretShare)
	if err != nil {
		return nil, 0, fmt.Errorf("deserializing threshold share: %w", err)
	}

	// Build a set of participant IDs that are members of the DKG group.
	// myDKGResult.Public.Shares is keyed by party.ID (uint16); only IDs present
	// in this map are legitimate co-signers. Any participant ID not in this set
	// was never part of the DKG and must be rejected before being used in signing.
	dkgGroupIDs := make(map[uint32]struct{}, len(myDKGResult.Public.Shares))
	for pid := range myDKGResult.Public.Shares {
		dkgGroupIDs[uint32(pid)] = struct{}{}
	}
	if _, ok := dkgGroupIDs[myParticipantID]; !ok {
		return nil, 0, fmt.Errorf("own participant ID %d not found in DKG group — share is corrupt or belongs to a different group", myParticipantID)
	}

	needed := int(thresh) - 1
	var coSigners []p2pPeer
	for _, p := range peers {
		if p.participantID > 0 {
			if _, inGroup := dkgGroupIDs[p.participantID]; !inGroup {
				// Participant ID is not a member of our DKG group. Reject it
				// to prevent a rogue participant from injecting an out-of-group
				// ID into the FROST signing session.
				continue
			}
			coSigners = append(coSigners, p2pPeer{endpoint: p.endpoint, participantID: p.participantID})
		}
		if len(coSigners) >= needed {
			break
		}
	}
	if len(coSigners) < needed {
		return nil, 0, fmt.Errorf("need %d co-signers with known participant IDs, only %d available", needed, len(coSigners))
	}

	reqs := cfState.ReceptionRequirements
	if reqs == nil {
		reqs = []string{}
	}
	hopTimestamp := time.Now().UnixNano()
	signInput := message.HopSignInput{
		MessageID:             msg.ID,
		CampfireID:            cfState.PublicKey,
		MembershipHash:        membershipHash,
		MemberCount:           memberCount,
		JoinProtocol:          cfState.JoinProtocol,
		ReceptionRequirements: reqs,
		Timestamp:             hopTimestamp,
	}
	signBytes, err := cfencoding.Marshal(signInput)
	if err != nil {
		return nil, 0, fmt.Errorf("computing hop sign bytes: %w", err)
	}

	frostCoSigners := make([]cfhttp.CoSigner, len(coSigners))
	for i, cs := range coSigners {
		frostCoSigners[i] = cfhttp.CoSigner{
			Endpoint:      cs.endpoint,
			ParticipantID: cs.participantID,
		}
	}

	sessionID := uuid.New().String()
	sig, err := cfhttp.RunFROSTSign(myDKGResult, myParticipantID, signBytes, frostCoSigners, campfireID, sessionID, c.identity)
	if err != nil {
		return nil, 0, fmt.Errorf("FROST signing: %w", err)
	}
	return sig, hopTimestamp, nil
}

// RoleError is returned by Send when the membership role prohibits the send.
type RoleError struct {
	msg string
}

func (e *RoleError) Error() string { return e.msg }

// IsRoleError returns true if err is a *RoleError. If target is non-nil, it is
// set to the *RoleError value (mirrors errors.As usage pattern).
func IsRoleError(err error, target **RoleError) bool {
	if err == nil {
		return false
	}
	re, ok := err.(*RoleError)
	if ok && target != nil {
		*target = re
	}
	return ok
}

// checkRoleCanSend enforces campfire membership role restrictions.
// Observer: cannot send any messages.
// BlindRelay: cannot originate messages (store/forward only, no CEK).
// Writer: cannot send campfire:* system messages.
// Full (default): no restrictions.
func checkRoleCanSend(role string, tags []string) error {
	effective := campfire.EffectiveRole(role)
	switch effective {
	case campfire.RoleObserver:
		return &RoleError{msg: "role observer: cannot send messages (read-only membership)"}
	case campfire.RoleBlindRelay:
		return &RoleError{msg: "role blind-relay: cannot originate messages (store/forward only)"}
	case campfire.RoleWriter:
		if hasSystemTag(tags) {
			return &RoleError{msg: "role writer: cannot send campfire:* system messages (requires full membership)"}
		}
		return nil
	default: // full
		return nil
	}
}

// hasSystemTag returns true if any tag in the list is a campfire:* system tag.
func hasSystemTag(tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, "campfire:") {
			return true
		}
	}
	return false
}

// isMember returns true if the given hex public key is in the members list.
func isMember(members []campfire.MemberRecord, pubKeyHex string) bool {
	for _, m := range members {
		if fmt.Sprintf("%x", m.PublicKey) == pubKeyHex {
			return true
		}
	}
	return false
}

// shortID returns the first 12 chars of an ID for error messages.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
