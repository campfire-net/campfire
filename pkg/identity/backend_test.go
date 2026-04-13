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
	id.SetBackend(fb)

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
	id := &Identity{PublicKey: pub}
	id.SetBackend(b)

	msg := []byte("test ssh-agent backend via Identity.SignWithBackend")
	sig, err := id.SignWithBackend(msg)
	if err != nil {
		t.Fatalf("SignWithBackend: %v", err)
	}
	if !ed25519.Verify(pub, msg, sig) {
		t.Error("SSHAgentBackend signature does not verify")
	}
}

// TestAgentBackendDirect_Sign_InvalidSignatureRejected is a regression test for
// campfire-cb1: agentBackendDirect.Sign must verify the returned signature and
// return an error when it is invalid, consistent with SSHAgentBackend.Sign.
//
// We simulate a malfunctioning agent by constructing a backend whose stored
// public key (b.pub) does NOT match the key the agent actually signs with.
// The agent returns a valid signature for its own key, but ed25519.Verify
// against the mismatched b.pub will fail — exactly the case that the missing
// check would have silently passed before.
func TestAgentBackendDirect_Sign_InvalidSignatureRejected(t *testing.T) {
	ring := agent.NewKeyring()

	// Add signing key to the ring.
	_, _, fp, err := addKeyToKeyring(ring, "signer-key")
	if err != nil {
		t.Fatalf("addKeyToKeyring: %v", err)
	}

	// Generate a DIFFERENT ed25519 public key (the "wrong" key we'll inject).
	wrongPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Build a normal backend for the signing key.
	b, err := NewSSHAgentBackendFromKeyring(ring, fp)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	defer b.Close()

	// Cast to the concrete type (we're in the same package) and replace b.pub
	// with the wrong public key. Now the agent signs with the real key but
	// Verify checks against wrongPub — it must fail.
	direct, ok := b.(*agentBackendDirect)
	if !ok {
		t.Fatalf("expected *agentBackendDirect, got %T", b)
	}
	direct.pub = wrongPub

	msg := []byte("campfire cb1 regression: direct backend sign verify mismatch")
	_, signErr := direct.Sign(msg)
	if signErr == nil {
		t.Fatal("agentBackendDirect.Sign must return an error when the signature does not verify (regression: cb1)")
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

// --- Identity.NewSigner tests (campfire-c5f) ---

// TestNewSigner_FileKey verifies that NewSigner works with a plain file-backed
// identity (no Backend). Sign and PublicKey must match the identity's key.
func TestNewSigner_FileKey(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	signer := id.NewSigner()
	if signer == nil {
		t.Fatal("NewSigner returned nil")
	}

	// PublicKey must match.
	if !signer.PublicKey().Equal(id.PublicKey) {
		t.Error("NewSigner().PublicKey() does not match identity.PublicKey")
	}

	msg := []byte("campfire newsigner file-key test")
	sig, err := signer.Sign(msg)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Errorf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(id.PublicKey, msg, sig) {
		t.Error("NewSigner signature does not verify against identity public key")
	}
}

// TestNewSigner_SSHAgentBackend verifies that NewSigner delegates to the
// SSHAgentBackend when one is configured. Uses an in-process keyring — no
// SSH_AUTH_SOCK required.
func TestNewSigner_SSHAgentBackend(t *testing.T) {
	ring := agent.NewKeyring()

	pub, _, fp, err := addKeyToKeyring(ring, "newsigner-agent-key")
	if err != nil {
		t.Fatalf("addKeyToKeyring: %v", err)
	}

	b, err := NewSSHAgentBackendFromKeyring(ring, fp)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	defer b.Close()

	// Construct a synthetic identity with only the public key + backend set
	// (mirrors the production case where the agent holds the private key).
	id := &Identity{PublicKey: pub}
	id.SetBackend(b)

	signer := id.NewSigner()

	// PublicKey must come from the backend.
	if !signer.PublicKey().Equal(pub) {
		t.Error("NewSigner().PublicKey() does not match agent key")
	}

	msg := []byte("campfire newsigner ssh-agent test")
	sig, err := signer.Sign(msg)
	if err != nil {
		t.Fatalf("Sign (ssh-agent backend): %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Errorf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub, msg, sig) {
		t.Error("NewSigner (ssh-agent backend) signature does not verify")
	}
}

// TestNewSigner_PublicKeyBackendMismatch verifies that when a Backend is set,
// NewSigner().PublicKey() returns the backend's public key (not id.PublicKey),
// ensuring callers get a consistent view even if id.PublicKey is zeroed.
func TestNewSigner_PublicKeyBackendMismatch(t *testing.T) {
	ring := agent.NewKeyring()

	pub, _, fp, err := addKeyToKeyring(ring, "mismatch-test-key")
	if err != nil {
		t.Fatalf("addKeyToKeyring: %v", err)
	}

	b, err := NewSSHAgentBackendFromKeyring(ring, fp)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	defer b.Close()

	// Identity has a different (dummy) public key but backend is set.
	id := &Identity{PublicKey: make(ed25519.PublicKey, ed25519.PublicKeySize)} // all zeros
	id.SetBackend(b)

	signer := id.NewSigner()

	// When backend is set, PublicKey must come from the backend.
	if !signer.PublicKey().Equal(pub) {
		t.Error("NewSigner().PublicKey() should return backend public key when backend is configured")
	}
}
