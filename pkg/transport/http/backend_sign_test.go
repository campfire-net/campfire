package http

// Integration tests for signRequest routing through the identity backend.
//
// Done conditions (campfire-874, 12a):
//   1. signRequest with a backend-equipped identity sets X-Campfire-Signature
//      signed by the backend key.
//   2. The resulting signature verifies against the backend's public key.
//   3. Without a backend, signRequest falls back to the in-memory private key.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/campfire-net/campfire/pkg/identity"
)

// addTestKeyToKeyring is a test-local helper: generates an ed25519 keypair,
// adds it to ring, and returns the public key and SHA256 fingerprint.
func addTestKeyToKeyring(t *testing.T, ring agent.Agent, comment string) (pub ed25519.PublicKey, fp string) {
	t.Helper()
	var priv ed25519.PrivateKey
	var err error
	pub, priv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := ring.Add(agent.AddedKey{PrivateKey: priv, Comment: comment}); err != nil {
		t.Fatalf("ring.Add: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("gossh.NewPublicKey: %v", err)
	}
	fp = gossh.FingerprintSHA256(sshPub)
	return pub, fp
}

// TestSignRequestUsesBackend verifies that signRequest delegates to the
// configured SigningBackend and sets correct headers (campfire-874 12a).
func TestSignRequestUsesBackend(t *testing.T) {
	// Create an in-process ssh-agent keyring.
	ring := agent.NewKeyring()
	backendPub, fingerprint := addTestKeyToKeyring(t, ring, "test-backend-key")

	// Create an identity whose public key matches the backend key.
	_, identPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating identity key: %v", err)
	}
	id := &identity.Identity{
		PublicKey:  backendPub,
		PrivateKey: identPriv,
	}

	backend, err := identity.NewSSHAgentBackendFromKeyring(ring, fingerprint)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	id.SetBackend(backend)

	// Build a minimal HTTP request.
	req, err := http.NewRequest(http.MethodPost, "http://example.com/test", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	body := []byte(`{"test":"payload"}`)

	// Call signRequest (internal function in package http).
	signRequest(req, id, body)

	// Verify headers are present.
	sender := req.Header.Get("X-Campfire-Sender")
	nonce := req.Header.Get("X-Campfire-Nonce")
	timestamp := req.Header.Get("X-Campfire-Timestamp")
	sigB64 := req.Header.Get("X-Campfire-Signature")

	if sender == "" {
		t.Fatal("X-Campfire-Sender header not set")
	}
	if nonce == "" {
		t.Fatal("X-Campfire-Nonce header not set")
	}
	if timestamp == "" {
		t.Fatal("X-Campfire-Timestamp header not set")
	}
	if sigB64 == "" {
		t.Fatal("X-Campfire-Signature header not set")
	}

	// Decode the signature.
	sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}

	// Verify sender matches the backend public key.
	expectedSender := hex.EncodeToString(backendPub)
	if sender != expectedSender {
		t.Errorf("X-Campfire-Sender = %s, want %s", sender, expectedSender)
	}

	// Reconstruct the signed payload and verify against the backend key.
	signedPayload := buildSignedPayload(timestamp, nonce, body)
	if !ed25519.Verify(backendPub, signedPayload, sigBytes) {
		t.Error("signature does not verify against backend public key")
	}
}

// TestSignRequestFallsBackToPrivateKey verifies that signRequest without a
// backend uses the in-memory private key (campfire-874 12a, regression guard).
func TestSignRequestFallsBackToPrivateKey(t *testing.T) {
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	// No backend configured.

	req, err := http.NewRequest(http.MethodGet, "http://example.com/sync", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	body := []byte{}
	signRequest(req, id, body)

	sigB64 := req.Header.Get("X-Campfire-Signature")
	nonce := req.Header.Get("X-Campfire-Nonce")
	timestamp := req.Header.Get("X-Campfire-Timestamp")

	sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}

	signedPayload := buildSignedPayload(timestamp, nonce, body)
	if !ed25519.Verify(id.PublicKey, signedPayload, sigBytes) {
		t.Error("fallback signature does not verify against identity public key")
	}
}
