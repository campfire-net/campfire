package protocol

// leave.go — protocol.Client.Leave() and Client.Members().
//
// Covered bead: campfire-agent-wfm
//
// Leave removes the calling agent from a campfire:
//   - Removes the member record from the filesystem transport directory.
//   - Removes the membership from the local store.
//
// After Leave, any Send attempt by the departing member will fail because:
//   1. The store membership check in Send() returns nil (store record gone).
//   2. The transport directory member record is gone (filesystem check fails).
//
// Members returns the current member list for a campfire from the filesystem
// transport directory. It is a lightweight query that does not touch the store.

import (
	"encoding/hex"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/internal/transport/fs"
)

// MemberRecord describes a campfire member returned by Members().
type MemberRecord struct {
	// MemberPubkey is the hex-encoded Ed25519 public key of the member.
	MemberPubkey string
	// Role is the campfire role ("full", "writer", "observer").
	Role string
	// JoinedAt is the unix nanosecond timestamp when the member joined.
	JoinedAt int64
}

// Members returns the member list for campfireID from the filesystem transport
// directory. The caller must be a member (store record present) so the
// transport directory can be located.
//
// Returns *ErrNotMember when campfireID is not in the caller's store.
func (c *Client) Members(campfireID string) ([]MemberRecord, error) {
	if campfireID == "" {
		return nil, fmt.Errorf("protocol.Client.Members: campfireID is required")
	}

	// Resolve beacon strings and cf:// URIs. Hint discarded — uses stored transport.
	resolvedID, _, resolveErr := resolveInput(campfireID, c.opts.namingResolver)
	if resolveErr != nil {
		return nil, fmt.Errorf("protocol.Client.Members: resolving campfire address: %w", resolveErr)
	}
	campfireID = resolvedID

	// Scope enforcement: campfire allowlist + read operation class.
	if err := c.checkCampfire(campfireID); err != nil {
		return nil, err
	}
	if err := c.checkOperation("read"); err != nil {
		return nil, err
	}

	m, err := c.store.GetMembership(campfireID)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Members: querying membership: %w", err)
	}
	if m == nil {
		return nil, &ErrNotMember{CampfireID: campfireID}
	}

	tr := fs.ForDir(m.TransportDir)
	raw, err := tr.ListMembers(campfireID)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Members: listing transport members: %w", err)
	}

	out := make([]MemberRecord, 0, len(raw))
	for _, r := range raw {
		out = append(out, MemberRecord{
			MemberPubkey: fmt.Sprintf("%x", r.PublicKey),
			Role:         r.Role,
			JoinedAt:     r.JoinedAt,
		})
	}
	return out, nil
}

// ErrNotMember is returned by Leave when the caller is not a member of the campfire.
type ErrNotMember struct {
	CampfireID string
}

// Error implements the error interface for ErrNotMember.
func (e *ErrNotMember) Error() string {
	return fmt.Sprintf("not a member of campfire %s", shortID(e.CampfireID))
}

// IsNotMemberError returns true if err is an *ErrNotMember.
// If target is non-nil it is set to the *ErrNotMember value.
func IsNotMemberError(err error, target **ErrNotMember) bool {
	if err == nil {
		return false
	}
	nme, ok := err.(*ErrNotMember)
	if ok && target != nil {
		*target = nme
	}
	return ok
}

// Leave removes the calling agent's membership from campfireID.
// It removes:
//   - The member record file from the filesystem transport directory.
//   - The membership record from the local store.
//
// Leave is idempotent in the sense that calling it a second time returns
// *ErrNotMember (not a panic and not a silent no-op). Callers can test for
// this with IsNotMemberError.
//
// Leave only supports the filesystem transport. P2P HTTP and GitHub transports
// require protocol-level eviction and are not handled here.
func (c *Client) Leave(campfireID string) error {
	if c.identity == nil {
		return fmt.Errorf("protocol.Client.Leave: identity required")
	}
	if campfireID == "" {
		return fmt.Errorf("protocol.Client.Leave: campfireID is required")
	}

	// Resolve beacon strings and cf:// URIs. Hint discarded — uses stored transport.
	resolvedID, _, resolveErr := resolveInput(campfireID, c.opts.namingResolver)
	if resolveErr != nil {
		return fmt.Errorf("protocol.Client.Leave: resolving campfire address: %w", resolveErr)
	}
	campfireID = resolvedID

	// Scope enforcement: campfire allowlist + admin operation class.
	if err := c.checkCampfire(campfireID); err != nil {
		return err
	}
	if err := c.checkOperation("admin"); err != nil {
		return err
	}

	// Check that the caller is currently a member (store record present).
	m, err := c.store.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("protocol.Client.Leave: querying membership: %w", err)
	}
	if m == nil {
		return &ErrNotMember{CampfireID: campfireID}
	}

	// Serialize membership mutations for this campfire (unified lock with Admit,
	// Evict, and Disband -- FIX-1/MB1).
	mu := c.membershipLock(campfireID)
	mu.Lock()
	defer mu.Unlock()

	// Remove member record from the filesystem transport directory.
	// We do this before removing the store record so that a partial failure
	// (transport removal succeeds, store removal fails) is detectable via a
	// store re-check rather than leaving orphaned filesystem state.
	tr := fs.ForDir(m.TransportDir)
	pubKeyBytes, err := hex.DecodeString(c.identity.PublicKeyHex())
	if err != nil {
		return fmt.Errorf("protocol.Client.Leave: decoding public key: %w", err)
	}
	if err := tr.RemoveMember(campfireID, pubKeyBytes); err != nil {
		return fmt.Errorf("protocol.Client.Leave: removing transport member record: %w", err)
	}

	// Remove membership from local store.
	if err := c.store.RemoveMembership(campfireID); err != nil {
		return fmt.Errorf("protocol.Client.Leave: removing store membership: %w", err)
	}

	return nil
}

