package identity

// sign_backend_test.go — Tests proving that Identity.Sign() delegates to the
// configured backend (campfire-fc9: Fix RegisterOnRelay bypasses backend signing).
//
// Done conditions:
//   1. Identity.Sign() with a backend uses the backend key, not PrivateKey.
//   2. Identity.Sign() without a backend uses PrivateKey directly.

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// TestIdentitySign_DelegatesToBackend proves that Identity.Sign delegates to
// the configured backend when one is set (campfire-fc9 Fix 2).
func TestIdentitySign_DelegatesToBackend(t *testing.T) {
	// Generate the backend keypair (this is the "real" key in the ssh-agent).
	backendPub, backendPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Add backend key to an in-process keyring.
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: backendPriv, Comment: "test-backend"}); err != nil {
		t.Fatalf("ring.Add: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(backendPub)
	if err != nil {
		t.Fatalf("gossh.NewPublicKey: %v", err)
	}
	fp := gossh.FingerprintSHA256(sshPub)

	// Create an identity with a different in-memory key but the backend's pubkey.
	// This lets us distinguish: if Sign used PrivateKey, verification against
	// backendPub would fail.
	_, differentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (different): %v", err)
	}
	id := &Identity{
		PublicKey:  backendPub,
		PrivateKey: differentPriv, // deliberately different from backend
		CreatedAt:  0,
	}

	backend, err := NewSSHAgentBackendFromKeyring(ring, fp)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	id.SetBackend(backend)

	msg := []byte("campfire-fc9 backend delegation test")
	sig := id.Sign(msg)

	// Signature must verify against the backend key (not differentPriv).
	if !ed25519.Verify(backendPub, msg, sig) {
		t.Error("Identity.Sign with backend: signature does not verify against backend public key — backend was NOT used")
	}

	// Verify the in-memory key was NOT used (sanity check).
	if ed25519.Verify(differentPriv.Public().(ed25519.PublicKey), msg, sig) {
		// If it accidentally verifies against differentPriv's pub, the keys
		// happened to be the same — this should essentially never happen.
		t.Log("note: differentPriv pubkey also verified (unexpected but not a failure by itself)")
	}
}

// TestIdentitySign_FallsBackToPrivateKey proves that Identity.Sign uses the
// in-memory private key when no backend is configured (campfire-fc9 Fix 2,
// regression guard).
func TestIdentitySign_FallsBackToPrivateKey(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// No backend set.

	msg := []byte("campfire-fc9 fallback test")
	sig := id.Sign(msg)

	if !ed25519.Verify(id.PublicKey, msg, sig) {
		t.Error("Identity.Sign without backend: signature does not verify against in-memory public key")
	}
}
