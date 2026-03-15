package threshold_test

import (
	"crypto/ed25519"
	"testing"

	"github.com/3dl-dev/campfire/pkg/threshold"
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
