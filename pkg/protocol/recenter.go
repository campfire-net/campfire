package protocol

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// recenterClaimedFile is the filename persisted in cfHome after a successful
// recentering claim, preventing the authorize hook from firing again.
const recenterClaimedFile = "recenter-claimed.json"

// recenterClaimedState is the JSON structure persisted to recenter-claimed.json.
type recenterClaimedState struct {
	CenterID  string `json:"center_id"`
	ClaimedAt string `json:"claimed_at"`
}

// RecenterClaim is the JSON payload posted to the center campfire as a
// two-signature claim message. Both signatures must verify independently.
type RecenterClaim struct {
	NewKeyHex string `json:"new_key_hex"`
	CenterID  string `json:"center_id"`
	CenterSig []byte `json:"center_sig"`
	NewKeySig []byte `json:"new_key_sig"`
}

// RecenterCanonicalPayload builds the canonical byte string signed by both keys.
// Exported for test verification.
func RecenterCanonicalPayload(newKeyHex, centerID string) []byte {
	return []byte("recenter-claim:" + newKeyHex + ":" + centerID)
}

// maybeRecenter runs the recentering slide-in logic during Init().
//
// It checks for a center campfire via walk-up, and if found:
//   - Checks if a claim has already been made (local state file)
//   - Checks if the current key is already linked (delegation cert in center)
//   - If neither: fires the authorize hook exactly once
//   - If authorized: posts a two-signature claim to the center campfire
//
// This function is a no-op when walk-up is disabled or no center is found.
func (c *Client) maybeRecenter(configDir string) error {
	if !c.opts.walkUp {
		return nil
	}

	// Walk up the filesystem to find a .campfire/center sentinel.
	centerID := walkUpForCenter(configDir)
	if centerID == "" {
		return nil // no center found — normal, not an error
	}

	// Check local state file — have we already claimed?
	claimedPath := filepath.Join(configDir, recenterClaimedFile)
	if alreadyClaimed(claimedPath, centerID) {
		return nil
	}

	// Check if our key is already linked via a delegation cert in the center.
	if c.isAlreadyLinked(centerID) {
		return nil
	}

	// Fire the authorize hook exactly once.
	approved, err := c.Authorize("Link this identity to your existing account?")
	if err != nil {
		return nil // hook error — don't block Init
	}
	if !approved {
		return nil // user declined — return normal client, no error
	}

	// Post two-signature claim to center campfire.
	if err := c.postRecenterClaim(configDir, centerID); err != nil {
		return nil // claim failure — don't block Init
	}

	// Persist "claimed" state so the hook never fires again for this center.
	persistClaimed(claimedPath, centerID)

	return nil
}

// isAlreadyLinked checks whether the current context key already has a
// delegation cert in the center campfire. A delegation cert is a message
// with tag "delegation-cert" where the payload contains this key's hex.
func (c *Client) isAlreadyLinked(centerID string) bool {
	result, err := c.Read(ReadRequest{
		CampfireID: centerID,
		Tags:       []string{"delegation-cert"},
	})
	if err != nil || result == nil {
		return false
	}

	myKeyHex := c.identity.PublicKeyHex()
	for _, msg := range result.Messages {
		var claim RecenterClaim
		if err := json.Unmarshal(msg.Payload, &claim); err != nil {
			continue
		}
		if claim.NewKeyHex == myKeyHex {
			return true
		}
	}
	return false
}

// postRecenterClaim creates and posts a two-signature claim to the center
// campfire. The claim contains signatures from both the center key (read from
// the center's transport state) and the new context key.
func (c *Client) postRecenterClaim(configDir, centerID string) error {
	newKeyHex := c.identity.PublicKeyHex()
	payload := RecenterCanonicalPayload(newKeyHex, centerID)

	// Sign with the new context key (we have the private key).
	newKeySig := ed25519.Sign(c.identity.PrivateKey, payload)

	// Get the center's private key from the transport state.
	centerPrivKey, err := c.getCenterPrivateKey(centerID)
	if err != nil {
		return fmt.Errorf("getting center private key: %w", err)
	}
	centerSig := ed25519.Sign(centerPrivKey, payload)

	claim := RecenterClaim{
		NewKeyHex: newKeyHex,
		CenterID:  centerID,
		CenterSig: centerSig,
		NewKeySig: newKeySig,
	}
	claimJSON, err := json.Marshal(claim)
	if err != nil {
		return fmt.Errorf("marshaling claim: %w", err)
	}

	_, err = c.Send(SendRequest{
		CampfireID: centerID,
		Payload:    claimJSON,
		Tags:       []string{"delegation-cert"},
	})
	return err
}

// getCenterPrivateKey retrieves the center campfire's private key from its
// filesystem transport state.
func (c *Client) getCenterPrivateKey(centerID string) (ed25519.PrivateKey, error) {
	m, err := c.store.GetMembership(centerID)
	if err != nil || m == nil {
		return nil, fmt.Errorf("no membership for center campfire %s", shortID(centerID))
	}

	tr := fs.ForDir(m.TransportDir)
	state, err := tr.ReadState(centerID)
	if err != nil {
		return nil, fmt.Errorf("reading center state: %w", err)
	}

	return ed25519.PrivateKey(state.PrivateKey), nil
}

// alreadyClaimed checks if the recenter-claimed.json file exists and matches
// the given center ID.
func alreadyClaimed(path, centerID string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var state recenterClaimedState
	if err := json.Unmarshal(data, &state); err != nil {
		return false
	}
	return state.CenterID == centerID
}

// persistClaimed writes the recenter-claimed.json file.
func persistClaimed(path, centerID string) {
	state := recenterClaimedState{
		CenterID:  centerID,
		ClaimedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0600) //nolint:errcheck
}
