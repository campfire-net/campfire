package protocol

// evict.go — protocol.Client.Evict().
//
// Covered bead: campfire-agent-c4h
//
// Evict removes a target member from the campfire by deleting their member
// record from the filesystem transport directory. Only the creator (or any
// current member with filesystem-transport access) can evict — callers are
// responsible for enforcing authorization at the application layer.
//
// Scope: filesystem transport only. Evicting over P2P HTTP or GitHub transport
// requires rekeying (the evicted member retains the campfire private key and can
// continue to sign messages until the key is rotated). Rekeying is not implemented
// here — create a campfire-agent bead to track that work.
//
// What Evict does NOT do:
//   - It does not remove messages the evicted member already sent.
//   - It does not revoke any cryptographic material (no rekey).
//   - It does not update the evicted member's own local store (they remain
//     in their store until they call Members() and discover they're gone).
//
// After Evict, the evicted member's next Send attempt will fail because
// sendFilesystem checks isMember() against the transport directory.

import (
	"encoding/hex"
	"fmt"

	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// EvictRequest holds parameters for Client.Evict().
type EvictRequest struct {
	// CampfireID is the hex-encoded campfire public key. Required.
	CampfireID string

	// MemberPubKeyHex is the hex-encoded Ed25519 public key of the member to evict.
	// Required.
	MemberPubKeyHex string

	// TransportDir is the campfire-specific filesystem transport directory
	// (the directory containing campfire.cbor, members/, messages/).
	// Required for filesystem transport.
	TransportDir string
}

// Evict removes MemberPubKeyHex from campfireID's member list in the filesystem
// transport directory. After Evict, the target member's Send calls fail because
// they are no longer recognized in the transport directory.
//
// The caller must be a member of the campfire (so their store record exists).
// Application-layer authorization (creator-only, etc.) is not enforced here.
//
// Returns *ErrNotMember when the caller is not a member of the campfire.
// Returns a descriptive error if the target member record does not exist
// (the member was never in the campfire, or already removed).
func (c *Client) Evict(req EvictRequest) error {
	if req.CampfireID == "" {
		return fmt.Errorf("protocol.Client.Evict: CampfireID is required")
	}
	if req.MemberPubKeyHex == "" {
		return fmt.Errorf("protocol.Client.Evict: MemberPubKeyHex is required")
	}
	if req.TransportDir == "" {
		return fmt.Errorf("protocol.Client.Evict: TransportDir is required")
	}

	// Verify the caller is a member.
	m, err := c.store.GetMembership(req.CampfireID)
	if err != nil {
		return fmt.Errorf("protocol.Client.Evict: querying membership: %w", err)
	}
	if m == nil {
		return &ErrNotMember{CampfireID: req.CampfireID}
	}

	targetPubKey, err := hex.DecodeString(req.MemberPubKeyHex)
	if err != nil {
		return fmt.Errorf("protocol.Client.Evict: decoding MemberPubKeyHex: %w", err)
	}

	tr := fs.ForDir(req.TransportDir)
	if err := tr.RemoveMember(req.CampfireID, targetPubKey); err != nil {
		return fmt.Errorf("protocol.Client.Evict: removing member from transport: %w", err)
	}

	return nil
}
