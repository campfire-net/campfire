// session_events_test.go — TDD tests for session:open / session:close L1 system events.
//
// campfireagent-647: Stage 1 — tag definitions + emission machinery.
//
// Test coverage:
//   1. Tag constants have the right wire values.
//   2. EmitSessionOpen produces a signed message with the session:open tag.
//   3. EmitSessionClose produces a signed message with the session:close tag.
//   4. Emitted messages have valid signatures (VerifySignature passes).
//   5. A forged message (tampered payload) fails VerifySignature.
//   6. Missing signature fails VerifySignature.
//   7. Malformed (binary) payload is accepted — payload is opaque bytes.
//   8. Wrong sender key substitution causes VerifySignature to return false.
//   9. Sender in emitted message is a valid Ed25519 public key.

package protocol_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// TestTagSessionOpen_WireValue verifies the session:open tag wire value.
func TestTagSessionOpen_WireValue(t *testing.T) {
	if protocol.TagSessionOpen != "session:open" {
		t.Errorf("TagSessionOpen = %q, want %q", protocol.TagSessionOpen, "session:open")
	}
}

// TestTagSessionClose_WireValue verifies the session:close tag wire value.
func TestTagSessionClose_WireValue(t *testing.T) {
	if protocol.TagSessionClose != "session:close" {
		t.Errorf("TagSessionClose = %q, want %q", protocol.TagSessionClose, "session:close")
	}
}

// TestTagSessionPrefix_SessionPrefix verifies that both tags use the session: prefix
// (distinct from campfire: and convention: prefixes).
func TestTagSessionPrefix_SessionPrefix(t *testing.T) {
	for _, tag := range []string{protocol.TagSessionOpen, protocol.TagSessionClose} {
		if len(tag) < 8 || tag[:8] != "session:" {
			t.Errorf("tag %q does not start with session:", tag)
		}
	}
}

// TestTagSessionOpen_DistinctFromSessionClose verifies the two tag values are distinct.
func TestTagSessionOpen_DistinctFromSessionClose(t *testing.T) {
	if protocol.TagSessionOpen == protocol.TagSessionClose {
		t.Errorf("TagSessionOpen and TagSessionClose must be distinct; both = %q", protocol.TagSessionOpen)
	}
}

// TestEmitSessionOpen_TagPresent verifies that EmitSessionOpen posts a message
// with the session:open tag.
func TestEmitSessionOpen_TagPresent(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionOpen(sess, []byte(`{"session_id":"test"}`))
	if err != nil {
		t.Fatalf("EmitSessionOpen: %v", err)
	}
	if !hasTagInSlice(msg.Tags, protocol.TagSessionOpen) {
		t.Errorf("message tags %v do not include %q", msg.Tags, protocol.TagSessionOpen)
	}
}

// TestEmitSessionClose_TagPresent verifies that EmitSessionClose posts a message
// with the session:close tag.
func TestEmitSessionClose_TagPresent(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionClose(sess, []byte(`{"reason":"normal"}`))
	if err != nil {
		t.Fatalf("EmitSessionClose: %v", err)
	}
	if !hasTagInSlice(msg.Tags, protocol.TagSessionClose) {
		t.Errorf("message tags %v do not include %q", msg.Tags, protocol.TagSessionClose)
	}
}

// TestEmitSessionOpen_SignatureVerifies verifies that the emitted message has a
// valid signature.
func TestEmitSessionOpen_SignatureVerifies(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionOpen(sess, []byte(`{}`))
	if err != nil {
		t.Fatalf("EmitSessionOpen: %v", err)
	}
	if !msg.VerifySignature() {
		t.Error("EmitSessionOpen: message signature did not verify")
	}
}

// TestEmitSessionClose_SignatureVerifies verifies that the emitted session:close
// message has a valid signature.
func TestEmitSessionClose_SignatureVerifies(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionClose(sess, []byte(`{}`))
	if err != nil {
		t.Fatalf("EmitSessionClose: %v", err)
	}
	if !msg.VerifySignature() {
		t.Error("EmitSessionClose: message signature did not verify")
	}
}

// TestEmitSessionOpen_ForgeryRejected_TamperedPayload verifies that tampering
// with the payload after signing causes VerifySignature to return false.
//
// Adversarial test: sign a legitimate session:open, then mutate payload bytes.
// The signature must NOT pass because the sign-input no longer matches.
func TestEmitSessionOpen_ForgeryRejected_TamperedPayload(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionOpen(sess, []byte(`{"legitimate":true}`))
	if err != nil {
		t.Fatalf("EmitSessionOpen (creator): %v", err)
	}
	if !msg.VerifySignature() {
		t.Fatal("pre-tamper: legitimate message should verify")
	}

	// Tamper: change the payload after signing.
	forgedMsg := *msg
	forgedMsg.Payload = []byte(`{"forged":true}`)
	if forgedMsg.VerifySignature() {
		t.Error("tampered payload: VerifySignature should return false for forged message")
	}
}

// TestEmitSessionOpen_ForgeryRejected_TamperedTags verifies that tampering with
// the tags after signing causes VerifySignature to return false.
func TestEmitSessionOpen_ForgeryRejected_TamperedTags(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionOpen(sess, []byte(`{}`))
	if err != nil {
		t.Fatalf("EmitSessionOpen: %v", err)
	}

	// Tamper: inject an extra tag after signing.
	forgedMsg := *msg
	forgedMsg.Tags = append([]string{}, msg.Tags...)
	forgedMsg.Tags = append(forgedMsg.Tags, "adversary:injected")
	if forgedMsg.VerifySignature() {
		t.Error("tampered tags: VerifySignature should return false")
	}
}

// TestEmitSessionOpen_MissingSignature verifies that a message with an empty
// signature fails VerifySignature.
func TestEmitSessionOpen_MissingSignature(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionOpen(sess, []byte(`{}`))
	if err != nil {
		t.Fatalf("EmitSessionOpen: %v", err)
	}
	// Strip the signature.
	msg.Signature = nil
	if msg.VerifySignature() {
		t.Error("nil signature: VerifySignature should return false")
	}
}

// TestEmitSessionOpen_WrongSenderKey verifies that substituting the sender's
// public key with a different key causes VerifySignature to fail.
//
// Adversarial: key substitution attack — attacker replaces the sender pubkey
// with their own after the message is signed by the legitimate creator.
func TestEmitSessionOpen_WrongSenderKey(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionOpen(sess, []byte(`{}`))
	if err != nil {
		t.Fatalf("EmitSessionOpen: %v", err)
	}
	if !msg.VerifySignature() {
		t.Fatal("pre-substitution: legitimate message should verify")
	}

	// Substitute the sender's public key with a freshly-generated adversary key.
	wrongPub, _, genErr := ed25519.GenerateKey(nil)
	if genErr != nil {
		t.Fatalf("generating adversary key: %v", genErr)
	}
	msg.Sender = wrongPub
	if msg.VerifySignature() {
		t.Error("wrong sender key: VerifySignature should return false")
	}
}

// TestEmitSessionOpen_MalformedPayload verifies that an arbitrary/binary
// payload is accepted — payload is opaque bytes, not validated by the emitter.
func TestEmitSessionOpen_MalformedPayload(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	// Binary garbage — not valid JSON.
	garbage := []byte{0x00, 0xFF, 0xDE, 0xAD, 0xBE, 0xEF}
	msg, err := protocol.EmitSessionOpen(sess, garbage)
	if err != nil {
		t.Fatalf("EmitSessionOpen with binary payload: %v", err)
	}
	if !msg.VerifySignature() {
		t.Error("binary payload message should still have valid signature")
	}
	if !hasTagInSlice(msg.Tags, protocol.TagSessionOpen) {
		t.Errorf("binary payload message missing session:open tag, got %v", msg.Tags)
	}
}

// TestEmitSessionOpen_SenderIsPresent verifies that the emitted message has a
// non-empty Sender field (signed by a real key, not anonymous).
func TestEmitSessionOpen_SenderIsPresent(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionOpen(sess, []byte(`{}`))
	if err != nil {
		t.Fatalf("EmitSessionOpen: %v", err)
	}

	if len(msg.Sender) != ed25519.PublicKeySize {
		t.Errorf("Sender length = %d, want %d (Ed25519 public key size)", len(msg.Sender), ed25519.PublicKeySize)
	}
}

// TestEmitSessionClose_SenderIsPresent verifies that session:close messages
// also have a non-empty Sender field.
func TestEmitSessionClose_SenderIsPresent(t *testing.T) {
	sess, cleanup := setupEmitSession(t)
	defer cleanup()

	msg, err := protocol.EmitSessionClose(sess, []byte(`{}`))
	if err != nil {
		t.Fatalf("EmitSessionClose: %v", err)
	}

	if len(msg.Sender) != ed25519.PublicKeySize {
		t.Errorf("Sender length = %d, want %d (Ed25519 public key size)", len(msg.Sender), ed25519.PublicKeySize)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// setupEmitSession creates a client with identity + a session campfire for emission tests.
// Returns the session handle and a cleanup function.
func setupEmitSession(t *testing.T) (*protocol.Session, func()) {
	t.Helper()
	client, cleanup := setupSessionClient(t)
	sess, _, err := client.NewSession(2 * time.Hour)
	if err != nil {
		cleanup()
		t.Fatalf("NewSession: %v", err)
	}
	return sess, func() {
		sess.End() //nolint:errcheck
		cleanup()
	}
}

// hasTagInSlice reports whether the tags slice contains the given tag.
func hasTagInSlice(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}
