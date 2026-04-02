package message_test

import (
	"crypto/ed25519"
	"fmt"
	"testing"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	. "github.com/campfire-net/campfire/pkg/message"
)

// TestSenderCampfireID_RoundTripCBOR verifies that a Message with SenderCampfireID
// encodes to CBOR and decodes back with the field intact.
func TestSenderCampfireID_RoundTripCBOR(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	// Generate a fake campfire ID (32 bytes, like an Ed25519 public key).
	campfirePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	msg, err := NewMessage(priv, pub, []byte("hello"), []string{"test-tag"}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	// Set SenderCampfireID (setter pattern — not in NewMessage signature).
	msg.SenderCampfireID = []byte(campfirePub)

	// Encode to CBOR.
	data, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Decode back.
	var decoded Message
	if err := cfencoding.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// SenderCampfireID must survive the round-trip.
	if string(decoded.SenderCampfireID) != string(msg.SenderCampfireID) {
		t.Errorf("SenderCampfireID: got %x, want %x", decoded.SenderCampfireID, msg.SenderCampfireID)
	}
	if decoded.ID != msg.ID {
		t.Errorf("ID: got %s, want %s", decoded.ID, msg.ID)
	}
	if string(decoded.Payload) != string(msg.Payload) {
		t.Errorf("Payload: got %s, want %s", decoded.Payload, msg.Payload)
	}
}

// TestSenderCampfireID_OmittedForLegacyMessages verifies that a Message without
// SenderCampfireID (old format) decodes correctly — field is nil/empty.
func TestSenderCampfireID_OmittedForLegacyMessages(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	msg, err := NewMessage(priv, pub, []byte("legacy"), []string{"tag"}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	// Do NOT set SenderCampfireID — simulating a legacy message.

	data, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded Message
	if err := cfencoding.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// SenderCampfireID must be nil/empty for legacy messages.
	if len(decoded.SenderCampfireID) != 0 {
		t.Errorf("expected SenderCampfireID to be empty for legacy message, got %x", decoded.SenderCampfireID)
	}
}

// TestSenderCampfireID_NotInSignInput verifies that SenderCampfireID is NOT
// part of the message signature — signature verification passes when SenderCampfireID
// is set after the message is signed.
func TestSenderCampfireID_NotInSignInput(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	campfirePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	msg, err := NewMessage(priv, pub, []byte("signed"), []string{"tag"}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	// Verify signature before setting SenderCampfireID.
	if !msg.VerifySignature() {
		t.Fatal("VerifySignature failed before SenderCampfireID set")
	}

	// Set SenderCampfireID AFTER signing.
	msg.SenderCampfireID = []byte(campfirePub)

	// Signature must still verify — SenderCampfireID is not in sign input.
	if !msg.VerifySignature() {
		t.Error("VerifySignature failed after SenderCampfireID set — SenderCampfireID must NOT be in sign input")
	}
}

// TestSenderCampfireID_VerifiedAgainstSender verifies that signature verification
// uses Sender (agent pubkey), not SenderCampfireID.
func TestSenderCampfireID_VerifiedAgainstSender(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	campfirePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	msg, err := NewMessage(priv, pub, []byte("test"), []string{"tag"}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	msg.SenderCampfireID = []byte(campfirePub)

	// VerifySignature checks Sender (pub), not SenderCampfireID (campfirePub).
	// If it checked campfirePub, it would fail (different key).
	if !msg.VerifySignature() {
		t.Error("VerifySignature failed — must verify against Sender (agent pubkey), not SenderCampfireID")
	}
}

// TestSenderIdentity_PrefersID verifies SenderIdentity() returns campfire ID when set.
func TestSenderIdentity_PrefersID(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	campfirePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}

	msg, err := NewMessage(priv, pub, []byte("test"), []string{"tag"}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	msg.SenderCampfireID = []byte(campfirePub)

	got := msg.SenderIdentity()
	want := fmt.Sprintf("%x", campfirePub)
	if got != want {
		t.Errorf("SenderIdentity: got %q, want %q", got, want)
	}
}

// TestSenderIdentity_FallsBackToPubkey verifies SenderIdentity() falls back to
// hex pubkey when SenderCampfireID is absent.
func TestSenderIdentity_FallsBackToPubkey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	msg, err := NewMessage(priv, pub, []byte("test"), []string{"tag"}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	// SenderCampfireID is NOT set.

	got := msg.SenderIdentity()
	want := fmt.Sprintf("%x", pub)
	if got != want {
		t.Errorf("SenderIdentity: got %q, want %q", got, want)
	}
}
