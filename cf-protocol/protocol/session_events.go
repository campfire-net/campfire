// session_events.go — L1 session lifecycle event emission API (campfireagent-647).
//
// Defines and re-exports the session:open and session:close tag constants from
// cf-protocol/internal/tagspec, and provides EmitSessionOpen / EmitSessionClose
// helper functions that sign and post the events using the session's signing key.
//
// These are L1 system events — they are NOT reserved ops (that list is frozen),
// NOT convention-defined tags (those use the "convention:" prefix), and NOT
// campfire-key-signed membership events (those use "campfire:"). They sit in
// the "session:" namespace alongside any future session-lifecycle primitives.
//
// Stage 1 scope: tag constants + emission helpers only. The Stage 3 cf-session
// orchestrator code that calls these helpers (and upgrades to creator's real
// identity key) is NOT in this file.
//
// Usage:
//
//	// Session creator emits open event:
//	msg, err := protocol.EmitSessionOpen(sess, payloadBytes)
//
//	// Session creator emits close event:
//	msg, err := protocol.EmitSessionClose(sess, payloadBytes)
package protocol

import (
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/internal/message"
	"github.com/campfire-net/campfire/cf-protocol/internal/tagspec"
)

// ── Tag constants (re-exported from internal/tagspec) ────────────────────────

// TagSessionOpen is the reserved tag for session-open L1 system events.
// Wire value: "session:open" — frozen at cf-protocol 1.0.
//
// Re-exported from cf-protocol/internal/tagspec.TagSessionOpen.
const TagSessionOpen = tagspec.TagSessionOpen

// TagSessionClose is the reserved tag for session-close L1 system events.
// Wire value: "session:close" — frozen at cf-protocol 1.0.
//
// Re-exported from cf-protocol/internal/tagspec.TagSessionClose.
const TagSessionClose = tagspec.TagSessionClose

// ── Emission helpers ─────────────────────────────────────────────────────────

// EmitSessionOpen posts a session:open L1 system event into the given session
// campfire. The message is signed by the session's signing key. In Stage 1,
// this is the ephemeral session key; Stage 3 (cf-session) will upgrade to the
// creator's real Ed25519 identity key when the campfire admits creator's key.
//
// The payload is treated as opaque bytes — callers are expected to marshal a
// JSON object with session metadata, but the emitter does not validate the
// payload structure (Stage 3 enforces the schema).
//
// Returns the created signed wire message on success.
//
// Errors:
//   - If sess is nil or closed.
//   - If transport I/O fails.
func EmitSessionOpen(sess *Session, payload []byte) (*message.Message, error) {
	msg, err := sess.sendTagged(payload, []string{TagSessionOpen})
	if err != nil {
		return nil, fmt.Errorf("protocol.EmitSessionOpen: %w", err)
	}
	return msg, nil
}

// EmitSessionClose posts a session:close L1 system event into the given session
// campfire. After this event, the session campfire may be compacted by the
// operator. The session's grant log (in the creator's identity campfire) is
// unaffected by compaction.
//
// Returns the created signed wire message on success.
//
// Errors:
//   - If sess is nil or closed.
//   - If transport I/O fails.
func EmitSessionClose(sess *Session, payload []byte) (*message.Message, error) {
	msg, err := sess.sendTagged(payload, []string{TagSessionClose})
	if err != nil {
		return nil, fmt.Errorf("protocol.EmitSessionClose: %w", err)
	}
	return msg, nil
}
