package identity

import (
	"crypto/ed25519"
	"os"
	"testing"

	"golang.org/x/crypto/ssh/agent"
)

// TestFileBackend_Sign verifies that FileBackend produces a valid ed25519 signature.
func TestFileBackend_Sign(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	b, err := NewFileBackend(id.PrivateKey)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}

	msg := []byte("campfire file backend sign test")
	sig, err := b.Sign(msg)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if len(sig) != ed25519.SignatureSize {
		t.Errorf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}

	if !ed25519.Verify(b.PublicKey(), msg, sig) {
		t.Error("signature does not verify against FileBackend public key")
	}

	// Also verify against the original identity public key.
	if !ed25519.Verify(id.PublicKey, msg, sig) {
		t.Error("signature does not verify against original identity public key")
	}
}

// TestFileBackend_Close verifies that Close returns nil.
func TestFileBackend_Close(t *testing.T) {
	id, _ := Generate()
	b, err := NewFileBackend(id.PrivateKey)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// TestSSHAgentBackend_MissingSocket verifies that NewSSHAgentBackend fails
// when SSH_AUTH_SOCK is not set.
func TestSSHAgentBackend_MissingSocket(t *testing.T) {
	// Unset SSH_AUTH_SOCK for this test.
	prev := os.Getenv("SSH_AUTH_SOCK")
	os.Unsetenv("SSH_AUTH_SOCK")
	defer func() {
		if prev != "" {
			os.Setenv("SSH_AUTH_SOCK", prev)
		}
	}()

	_, err := NewSSHAgentBackend("SHA256:somefingerprint")
	if err == nil {
		t.Fatal("NewSSHAgentBackend should fail when SSH_AUTH_SOCK is not set")
	}
}

// TestSSHAgentBackend_MissingFingerprint verifies that both constructors
// reject an empty fingerprint.
func TestSSHAgentBackend_MissingFingerprint(t *testing.T) {
	// NewSSHAgentBackend (socket-based)
	_, err := NewSSHAgentBackend("")
	if err == nil {
		t.Error("NewSSHAgentBackend should fail with empty fingerprint")
	}

	// NewSSHAgentBackendFromKeyring (in-process)
	ring := agent.NewKeyring()
	_, err = NewSSHAgentBackendFromKeyring(ring, "")
	if err == nil {
		t.Error("NewSSHAgentBackendFromKeyring should fail with empty fingerprint")
	}
}

// TestSSHAgentBackend_RoundTrip uses an in-process keyring (no SSH_AUTH_SOCK
// required) to test the full sign/verify round-trip.
func TestSSHAgentBackend_RoundTrip(t *testing.T) {
	ring := agent.NewKeyring()

	// Generate a key and add it to the in-process keyring.
	pub, _, fp, err := addKeyToKeyring(ring, "test-key")
	if err != nil {
		t.Fatalf("addKeyToKeyring: %v", err)
	}

	// Build backend from keyring.
	b, err := NewSSHAgentBackendFromKeyring(ring, fp)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	defer b.Close()

	// Verify PublicKey matches.
	if !b.PublicKey().Equal(pub) {
		t.Error("backend public key does not match generated key")
	}

	msg := []byte("campfire ssh-agent round trip test")
	sig, err := b.Sign(msg)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if len(sig) != ed25519.SignatureSize {
		t.Errorf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}

	if !ed25519.Verify(pub, msg, sig) {
		t.Error("signature does not verify against the original public key")
	}
}

// TestSSHAgentBackend_WrongFingerprint verifies that the backend returns an
// error when no key matches the given fingerprint.
func TestSSHAgentBackend_WrongFingerprint(t *testing.T) {
	ring := agent.NewKeyring()

	// Add a key to the keyring, but request a different fingerprint.
	_, _, _, err := addKeyToKeyring(ring, "real-key")
	if err != nil {
		t.Fatalf("addKeyToKeyring: %v", err)
	}

	_, err = NewSSHAgentBackendFromKeyring(ring, "SHA256:doesnotexist")
	if err == nil {
		t.Fatal("NewSSHAgentBackendFromKeyring should fail when fingerprint is not found")
	}
}

// TestIdentitySignWithBackend_File verifies that SignWithBackend delegates to
// the FileBackend when one is configured.
func TestIdentitySignWithBackend_File(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	fb, err := NewFileBackend(id.PrivateKey)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}
	id.Backend = fb

	msg := []byte("test file backend via Identity.SignWithBackend")
	sig, err := id.SignWithBackend(msg)
	if err != nil {
		t.Fatalf("SignWithBackend: %v", err)
	}
	if !id.Verify(msg, sig) {
		t.Error("SignWithBackend (FileBackend) signature does not verify")
	}
}

// TestIdentitySignWithBackend_SSHAgent verifies that SignWithBackend delegates
// to SSHAgentBackend when configured with an in-process keyring.
func TestIdentitySignWithBackend_SSHAgent(t *testing.T) {
	ring := agent.NewKeyring()

	pub, _, fp, err := addKeyToKeyring(ring, "agent-key")
	if err != nil {
		t.Fatalf("addKeyToKeyring: %v", err)
	}

	b, err := NewSSHAgentBackendFromKeyring(ring, fp)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	defer b.Close()

	// Build a synthetic Identity with only the public key and the backend set.
	// (In practice, ssh-agent-backed identities still have a public key from the
	// identity file; the private key may be empty/zeroed since the agent holds it.)
	id := &Identity{
		PublicKey: pub,
		Backend:   b,
	}

	msg := []byte("test ssh-agent backend via Identity.SignWithBackend")
	sig, err := id.SignWithBackend(msg)
	if err != nil {
		t.Fatalf("SignWithBackend: %v", err)
	}
	if !ed25519.Verify(pub, msg, sig) {
		t.Error("SSHAgentBackend signature does not verify")
	}
}

// TestIdentitySignWithBackend_NilBackend verifies that SignWithBackend falls
// back to Sign (in-memory key) when no Backend is configured.
func TestIdentitySignWithBackend_NilBackend(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// No Backend set — should use id.Sign internally.
	msg := []byte("nil backend fallback test")
	sig, err := id.SignWithBackend(msg)
	if err != nil {
		t.Fatalf("SignWithBackend (nil backend): %v", err)
	}
	if !id.Verify(msg, sig) {
		t.Error("nil-backend fallback signature does not verify")
	}
}
