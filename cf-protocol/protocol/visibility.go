// visibility.go — campfire:visibility-changed L1 system event emission (campfireagent-c66).
//
// Implements Client.EmitVisibilityChanged(), which fires a campfire:visibility-changed
// system event signed by the campfire's Ed25519 key (not the calling agent's key).
//
// Design: design v2 §3 hardening #4 + §4.1; protocol-spec §cf-protocol 1.0 Layer-1 Additions.
//
// The event fires when a campfire transitions from public to private (join protocol
// changes from "open" to "invite-only"). Signed by the campfire key so receivers can
// verify the event is authoritative — a member-signed message cannot forge this event.
//
// Wire format (JSON payload):
//
//	{
//	  "previous_join_protocol": "open",
//	  "new_join_protocol":      "invite-only",
//	  "changed_at":             <unix nanosecond timestamp>
//	}
//
// Stage 1 scope: emission API only. The caller is responsible for updating the
// campfire's JoinProtocol and publishing an updated beacon. This function only
// emits the signed event message.
package protocol

import (
	"fmt"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/internal/campfire"
	"github.com/campfire-net/campfire/cf-protocol/internal/message"
	"github.com/campfire-net/campfire/cf-protocol/internal/store"
	"github.com/campfire-net/campfire/cf-protocol/internal/transport/fs"
)

// VisibilityChangedPayload is the JSON payload for campfire:visibility-changed events.
// All fields are frozen at cf-protocol 1.0 (wire format).
type VisibilityChangedPayload struct {
	PreviousJoinProtocol string `json:"previous_join_protocol"`
	NewJoinProtocol      string `json:"new_join_protocol"`
	ChangedAt            int64  `json:"changed_at"`
}

// EmitVisibilityChanged emits a campfire:visibility-changed L1 system event into the
// campfire identified by campfireID. The message is signed by the campfire's own
// Ed25519 key — NOT the calling agent's identity key.
//
// oldVisibility is the previous join protocol (e.g. "open").
// newVisibility is the new join protocol (e.g. "invite-only").
//
// This function supports filesystem-transport campfires where the campfire's private
// key is available on disk (in campfire.cbor). The function reads the campfire state,
// signs with the campfire key, writes the event to the transport directory, and mirrors
// it to the local store.
//
// Callers are responsible for updating the campfire's JoinProtocol in the transport
// state and beacon after calling this function.
//
// Returns an error if:
//   - The campfire membership record is not found.
//   - The campfire state (private key) cannot be read.
//   - Message signing or transport I/O fails.
func (c *Client) EmitVisibilityChanged(campfireID, oldVisibility, newVisibility string) error {
	// Lookup the membership record to find the transport directory.
	m, err := c.store.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: querying membership: %w", err)
	}
	if m == nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: not a member of campfire %s", shortID(campfireID))
	}

	// Validate the transport directory path.
	transportDir, err := sanitizeTransportDir(m.TransportDir)
	if err != nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: invalid transport dir: %w", err)
	}

	// Read the campfire state to get the campfire's private key for signing.
	tr := fs.ForDir(transportDir)
	state, err := tr.ReadState(campfireID)
	if err != nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: reading campfire state: %w", err)
	}
	if len(state.PrivateKey) == 0 {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: campfire private key unavailable (read-only state)")
	}

	// Build JSON payload with the visibility change fields.
	changedAt := time.Now().UnixNano()
	payload := []byte(fmt.Sprintf(
		`{"previous_join_protocol":%q,"new_join_protocol":%q,"changed_at":%d}`,
		oldVisibility, newVisibility, changedAt,
	))

	// Sign the message with the CAMPFIRE key, not the operator/identity key.
	// This is the critical distinction: the Sender field will be the campfire's
	// public key, allowing receivers to verify via VerifySignature().
	cfSigner, err := message.NewEd25519Signer(state.PrivateKey, state.PublicKey)
	if err != nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: creating campfire signer: %w", err)
	}
	msg, err := message.NewMessage(
		cfSigner,
		payload,
		[]string{campfire.TagVisibilityChanged},
		nil,
	)
	if err != nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: creating message: %w", err)
	}

	// Add provenance hop signed by the campfire key (consistent with join.go).
	members, listErr := tr.ListMembers(campfireID)
	if listErr != nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: listing members: %w", listErr)
	}
	cfObj := state.ToCampfire(members)
	if hopErr := msg.AddHop(
		state.PrivateKey, state.PublicKey,
		cfObj.MembershipHash(), len(members),
		state.JoinProtocol, state.ReceptionRequirements,
		campfire.RoleFull,
	); hopErr != nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: adding provenance hop: %w", hopErr)
	}

	// Write to transport directory.
	if writeErr := tr.WriteMessage(campfireID, msg); writeErr != nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: writing message to transport: %w", writeErr)
	}

	// Mirror to local store so the sender can read the event without a sync step.
	if _, storeErr := c.store.AddMessage(
		store.MessageRecordFromMessage(campfireID, msg, store.NowNano()),
	); storeErr != nil {
		return fmt.Errorf("protocol.Client.EmitVisibilityChanged: storing message locally: %w", storeErr)
	}

	return nil
}
