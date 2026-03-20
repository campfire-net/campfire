package threshold_test

import (
	"crypto/ed25519"
	"testing"

	"github.com/campfire-net/campfire/pkg/threshold"
)

// TestDKG3of3 verifies that a 3-participant DKG completes successfully and all
// participants produce the same group public key.
func TestDKG3of3(t *testing.T) {
	participantIDs := []uint32{1, 2, 3}
	results, err := threshold.RunDKG(participantIDs, 2)
	if err != nil {
		t.Fatalf("RunDKG failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// All participants must agree on the group public key.
	var groupKey ed25519.PublicKey
	for id, r := range results {
		if r.SecretShare == nil {
			t.Errorf("participant %d: nil SecretShare", id)
		}
		if r.Public == nil {
			t.Errorf("participant %d: nil Public", id)
			continue
		}
		k := r.GroupPublicKey()
		if len(k) != ed25519.PublicKeySize {
			t.Errorf("participant %d: group public key has wrong length %d", id, len(k))
		}
		if groupKey == nil {
			groupKey = k
		} else if string(groupKey) != string(k) {
			t.Errorf("participant %d: group public key mismatch", id)
		}
	}
}

// TestSign2of3 verifies the core threshold property: 2 of 3 participants can
// produce a valid Ed25519 signature verifiable with the group public key.
func TestSign2of3(t *testing.T) {
	participantIDs := []uint32{1, 2, 3}
	results, err := threshold.RunDKG(participantIDs, 2)
	if err != nil {
		t.Fatalf("RunDKG failed: %v", err)
	}

	groupKey := results[1].GroupPublicKey()
	message := []byte("campfire threshold signing test")

	// Sign with participants 1 and 2 (exactly threshold signers).
	signerIDs := []uint32{1, 2}
	sig, err := threshold.Sign(results, signerIDs, message)
	if err != nil {
		t.Fatalf("Sign(2-of-3) failed: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("expected 64-byte signature, got %d bytes", len(sig))
	}
	if !ed25519.Verify(groupKey, message, sig) {
		t.Fatal("ed25519.Verify returned false for 2-of-3 threshold signature")
	}
}

// TestSign3of3 verifies that all 3 participants can also produce a valid signature.
func TestSign3of3(t *testing.T) {
	participantIDs := []uint32{1, 2, 3}
	results, err := threshold.RunDKG(participantIDs, 2)
	if err != nil {
		t.Fatalf("RunDKG failed: %v", err)
	}

	groupKey := results[1].GroupPublicKey()
	message := []byte("all three signers")

	sig, err := threshold.Sign(results, []uint32{1, 2, 3}, message)
	if err != nil {
		t.Fatalf("Sign(3-of-3) failed: %v", err)
	}
	if !ed25519.Verify(groupKey, message, sig) {
		t.Fatal("ed25519.Verify returned false for 3-of-3 threshold signature")
	}
}

// TestSign1of3MustFail verifies that a single participant cannot produce a
// valid signature when threshold is 2. This is the split-prevention guarantee.
// With threshold=2 (FROST polynomial degree=1), at least 2 parties are required
// to reconstruct the signing key. With only 1 signer, the signature is invalid.
func TestSign1of3MustFail(t *testing.T) {
	participantIDs := []uint32{1, 2, 3}
	results, err := threshold.RunDKG(participantIDs, 2)
	if err != nil {
		t.Fatalf("RunDKG failed: %v", err)
	}

	groupKey := results[1].GroupPublicKey()
	message := []byte("single signer attempt")

	// Sign with only 1 participant. The FROST protocol will run to completion
	// with a single signer but the resulting signature will fail verification
	// (the Lagrange interpolation is wrong with fewer than threshold signers).
	sig, err := threshold.Sign(results, []uint32{1}, message)
	if err != nil {
		// An error here is also acceptable — FROST may detect the invalidity.
		t.Logf("Sign(1-of-3) returned error (acceptable): %v", err)
		return
	}
	// If we got a signature, it must NOT verify against the group key.
	if ed25519.Verify(groupKey, message, sig) {
		t.Fatal("1-of-3 signature should NOT be valid but ed25519.Verify returned true")
	}
	t.Logf("Sign(1-of-3) produced a signature that correctly fails ed25519.Verify")
}

// --- workspace-zqc: UnmarshalResult error paths ---

// TestUnmarshalResultCorruptData verifies that UnmarshalResult returns an
// error (not a panic) when given corrupt bytes.
func TestUnmarshalResultCorruptData(t *testing.T) {
	corrupt := []byte("not valid json {{{")
	_, _, err := threshold.UnmarshalResult(corrupt)
	if err == nil {
		t.Fatal("expected error for corrupt data, got nil")
	}
}

// TestUnmarshalResultTruncatedData verifies that UnmarshalResult returns an
// error (not a panic) when given truncated JSON (syntactically invalid).
func TestUnmarshalResultTruncatedData(t *testing.T) {
	truncated := []byte(`{"participant_id":1,"secret_share":`)
	_, _, err := threshold.UnmarshalResult(truncated)
	if err == nil {
		t.Fatal("expected error for truncated data, got nil")
	}
}

// TestUnmarshalResultCorruptSecretShare verifies that UnmarshalResult returns
// an error when the outer JSON is valid but the embedded secret share bytes
// are corrupt (not valid JSON for the expected type).
func TestUnmarshalResultCorruptSecretShare(t *testing.T) {
	// Syntactically valid outer JSON, but secret_share and public contain
	// JSON string literals rather than valid FROST JSON objects.
	data := []byte(`{"participant_id":1,"secret_share":"bm90dmFsaWQ=","public":"bm90dmFsaWQ="}`)
	_, _, err := threshold.UnmarshalResult(data)
	if err == nil {
		t.Fatal("expected error for corrupt secret share bytes, got nil")
	}
}

// TestUnmarshalResultEmptyBytes verifies that UnmarshalResult returns an
// error for empty input.
func TestUnmarshalResultEmptyBytes(t *testing.T) {
	_, _, err := threshold.UnmarshalResult([]byte{})
	if err == nil {
		t.Fatal("expected error for empty data, got nil")
	}
}

// --- workspace-7yb: threshold edge cases ---

// TestDKG2of2 verifies that a 2-participant DKG completes successfully and
// both participants can sign (full-threshold, minimum participants). This is
// the most common production case: a 2-of-2 shared key where both parties
// must cooperate to sign.
func TestDKG2of2(t *testing.T) {
	participantIDs := []uint32{1, 2}
	results, err := threshold.RunDKG(participantIDs, 2)
	if err != nil {
		t.Fatalf("RunDKG(2-of-2) failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Both participants must agree on the group public key.
	key1 := results[1].GroupPublicKey()
	key2 := results[2].GroupPublicKey()
	if string(key1) != string(key2) {
		t.Fatal("2-of-2: participants disagree on group public key")
	}

	message := []byte("campfire 2-of-2 signing test")
	sig, err := threshold.Sign(results, []uint32{1, 2}, message)
	if err != nil {
		t.Fatalf("Sign(2-of-2) failed: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("expected 64-byte signature, got %d bytes", len(sig))
	}
	if !ed25519.Verify(key1, message, sig) {
		t.Fatal("ed25519.Verify returned false for 2-of-2 threshold signature")
	}
}

// TestNewDKGParticipantZeroThreshold verifies that NewDKGParticipant returns
// a clear error when threshold=0. Zero threshold is nonsensical (no signers
// required) and is explicitly rejected before reaching the FROST library.
func TestNewDKGParticipantZeroThreshold(t *testing.T) {
	_, err := threshold.NewDKGParticipant(1, []uint32{1, 2, 3}, 0)
	if err == nil {
		t.Fatal("expected error for threshold=0, got nil")
	}
}

// TestRunDKGZeroThreshold verifies that RunDKG propagates the threshold=0
// error from NewDKGParticipant and fails cleanly.
func TestRunDKGZeroThreshold(t *testing.T) {
	_, err := threshold.RunDKG([]uint32{1, 2, 3}, 0)
	if err == nil {
		t.Fatal("expected error for threshold=0, got nil")
	}
}

// TestSignSignerNotInDKG verifies that Sign returns an error when a signerID
// is not present in the DKG result set. The Sign function must detect this
// before attempting to construct a signing session with an unknown participant.
func TestSignSignerNotInDKG(t *testing.T) {
	participantIDs := []uint32{1, 2, 3}
	results, err := threshold.RunDKG(participantIDs, 2)
	if err != nil {
		t.Fatalf("RunDKG failed: %v", err)
	}

	// Participant 99 was never part of the DKG.
	_, err = threshold.Sign(results, []uint32{1, 99}, []byte("test message"))
	if err == nil {
		t.Fatal("expected error when signer 99 is not in DKG result set, got nil")
	}
	t.Logf("Sign with unknown signer correctly returned error: %v", err)
}

// TestDKGMultipleMessages verifies DKG works with non-sequential IDs.
func TestDKGNonSequentialIDs(t *testing.T) {
	participantIDs := []uint32{10, 20, 42}
	results, err := threshold.RunDKG(participantIDs, 2)
	if err != nil {
		t.Fatalf("RunDKG with non-sequential IDs failed: %v", err)
	}

	message := []byte("non-sequential id test")
	groupKey := results[10].GroupPublicKey()

	sig, err := threshold.Sign(results, []uint32{10, 42}, message)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if !ed25519.Verify(groupKey, message, sig) {
		t.Fatal("signature verification failed for non-sequential IDs")
	}
}
