package session_test

// Integration tests for session token signing via CreatorSigner (campfire-874 12b).
//
// Done conditions:
//   1. EncodeToken with CreatorSigner produces a token verifiable against the signer key.
//   2. EncodeToken with CreatorPriv (legacy) still works.
//   3. EncodeToken with a backend-backed signer (SSHAgentBackend via keyring) produces
//      a valid token signed by the backend key.

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/session"
	"golang.org/x/crypto/ssh/agent"
)

// generateSessionID returns a random 32-byte session campfire ID.
func generateSessionID(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return b
}

// TestEncodeToken_CreatorSigner verifies that a token signed via CreatorSigner
// is valid and verifiable with DecodeToken (campfire-874 12b).
func TestEncodeToken_CreatorSigner(t *testing.T) {
	creatorPub, creatorPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating creator keypair: %v", err)
	}
	ephPub, ephPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ephemeral keypair: %v", err)
	}

	// Build a signer function (simulating Identity.NewSigner().Sign).
	signer := func(msg []byte) ([]byte, error) {
		return ed25519.Sign(creatorPriv, msg), nil
	}

	params := session.TokenParams{
		CampfireID:      generateSessionID(t),
		EphemeralPubKey: ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig: []byte(`{"protocol":"filesystem","dir":"/tmp/test"}`),
		TTL:             1 * time.Hour,
		CreatorPub:      creatorPub,
		CreatorSigner:   signer,
		// CreatorPriv intentionally omitted.
	}

	tok, err := session.EncodeToken(params)
	if err != nil {
		t.Fatalf("EncodeToken with CreatorSigner: %v", err)
	}
	if !strings.HasPrefix(tok, "cfs1_") {
		t.Errorf("token missing cfs1_ prefix: got %q", tok[:min(len(tok), 10)])
	}

	decoded, err := session.DecodeToken(tok, creatorPub)
	if err != nil {
		t.Fatalf("DecodeToken: %v", err)
	}
	if !decoded.EphemeralPubKey.Equal(ephPub) {
		t.Error("decoded EphemeralPubKey does not match")
	}
}

// TestEncodeToken_CreatorSignerWithBackend verifies that when an Identity with an
// SSHAgentBackend is used as the CreatorSigner, the resulting token is valid.
// This is the critical path: protocol.NewSession uses c.identity.NewSigner().Sign.
func TestEncodeToken_CreatorSignerWithBackend(t *testing.T) {
	// Create an in-process ssh-agent keyring with a key.
	ring := agent.NewKeyring()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	if err := ring.Add(agent.AddedKey{PrivateKey: priv, Comment: "test-session-key"}); err != nil {
		t.Fatalf("ring.Add: %v", err)
	}

	// Get the fingerprint.
	fp, err := identity.FingerprintForKey(pub)
	if err != nil {
		t.Fatalf("FingerprintForKey: %v", err)
	}

	// Create an Identity with the backend.
	id := &identity.Identity{
		PublicKey:  pub,
		PrivateKey: priv,
	}
	backend, err := identity.NewSSHAgentBackendFromKeyring(ring, fp)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	id.SetBackend(backend)

	ephPub, ephPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ephemeral keypair: %v", err)
	}

	// Use identity.NewSigner().Sign as the CreatorSigner — exactly what
	// protocol.NewSession does after campfire-874.
	params := session.TokenParams{
		CampfireID:      generateSessionID(t),
		EphemeralPubKey: ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig: []byte(`{"protocol":"filesystem","dir":"/tmp/test"}`),
		TTL:             30 * time.Minute,
		CreatorPub:      pub,
		CreatorSigner:   id.NewSigner().Sign,
	}

	tok, err := session.EncodeToken(params)
	if err != nil {
		t.Fatalf("EncodeToken with backend signer: %v", err)
	}

	decoded, err := session.DecodeToken(tok, pub)
	if err != nil {
		t.Fatalf("DecodeToken: %v", err)
	}
	if !decoded.CreatorPubKey.Equal(pub) {
		t.Error("decoded CreatorPubKey does not match")
	}
}

// TestEncodeToken_LegacyCreatorPrivStillWorks verifies that the CreatorPriv
// path is not broken — old code and old test patterns continue to work.
func TestEncodeToken_LegacyCreatorPrivStillWorks(t *testing.T) {
	creatorPub, creatorPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating creator keypair: %v", err)
	}
	ephPub, ephPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ephemeral keypair: %v", err)
	}

	params := session.TokenParams{
		CampfireID:      generateSessionID(t),
		EphemeralPubKey: ephPub,
		EphemeralPrivKey: ephPriv,
		TransportConfig: []byte(`{}`),
		TTL:             1 * time.Hour,
		CreatorPub:      creatorPub,
		CreatorPriv:     creatorPriv,
		// No CreatorSigner — legacy path.
	}

	tok, err := session.EncodeToken(params)
	if err != nil {
		t.Fatalf("EncodeToken legacy: %v", err)
	}

	if _, err := session.DecodeToken(tok, creatorPub); err != nil {
		t.Fatalf("DecodeToken legacy: %v", err)
	}
}

