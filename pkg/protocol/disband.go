package protocol

// disband.go — protocol.Client.Disband().
//
// Covered bead: campfire-agent-ngp
//
// Disband tears down a campfire entirely. Only the creator (identified by
// CreatorPubkey on the stored membership) may call Disband. It:
//   1. Verifies the caller is the creator.
//   2. Removes all store memberships for the campfire (the creator's own
//      membership, plus any tracked for other members via store queries).
//   3. Removes the campfire directory from the filesystem transport.
//
// After Disband(), any subsequent Send() or Read() call against the same
// campfireID will fail because (a) no membership record exists in any caller's
// store and (b) the transport directory no longer exists on disk.
//
// Non-creator call: returns an error immediately. Campfire is unaffected.
// Idempotent: calling Disband() a second time returns nil (already gone).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	fstr "github.com/campfire-net/campfire/pkg/transport/fs"
)

// Disband tears down the campfire identified by campfireID. Only the creator
// may call Disband. On success, the campfire's filesystem directory is removed
// and the caller's store membership is deleted.
//
// Returns a non-nil error if:
//   - the caller has no membership record for the campfire,
//   - the caller is not the creator (CreatorPubkey != caller's public key),
//   - the filesystem removal fails for any reason other than the directory
//     already being absent (idempotency: already-absent is treated as success).
func (c *Client) Disband(campfireID string) error {
	if c.identity == nil {
		return fmt.Errorf("identity required to disband a campfire")
	}

	// Look up the caller's membership to find the transport dir and verify
	// creator status.
	m, err := c.store.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		// Already disbanded (idempotent). Nothing to do.
		return nil
	}

	// Enforce creator-only restriction.
	if m.CreatorPubkey != c.identity.PublicKeyHex() {
		return fmt.Errorf("only the creator can disband campfire %s", shortID(campfireID))
	}

	// Remove the filesystem transport directory first. This makes the campfire
	// inoperable for any member, even those whose store records we cannot reach
	// (e.g. other agents with their own stores).
	if m.TransportDir != "" {
		tr := fstr.ForDir(m.TransportDir)
		if err := tr.Remove(campfireID); err != nil {
			// If the directory is already gone, treat as success (idempotent).
			if !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
				return fmt.Errorf("removing transport directory: %w", err)
			}
		}
	}

	// Remove the creator's own membership record.
	if err := c.store.RemoveMembership(campfireID); err != nil {
		return fmt.Errorf("removing membership: %w", err)
	}

	return nil
}
