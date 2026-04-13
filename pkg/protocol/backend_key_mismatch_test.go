package protocol_test

// backend_key_mismatch_test.go — Integration tests for campfire-874 12c and 12d.
//
// 12c: NewSigner() routes through the backend when configured.
// 12d: Init returns an error when the ssh-agent backend key does not match
//      the identity file key.

import (
	"crypto/ed25519"
	"net"
	"path/filepath"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// startUnixAgent starts an in-process ssh-agent on a unix socket and returns
// the socket path. The caller should defer the returned stop function.
func startUnixAgent(t *testing.T, ring agent.Agent) (socketPath string, stop func()) {
	t.Helper()
	dir := t.TempDir()
	socketPath = filepath.Join(dir, "agent.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on unix socket: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(ring, conn)
		}
	}()

	stop = func() {
		ln.Close()
		<-done
	}
	return socketPath, stop
}

// TestIdentityNewSigner_WithBackend verifies that NewSigner() returns a signer
// that routes through the backend (campfire-874 12c).
func TestIdentityNewSigner_WithBackend(t *testing.T) {
	ring := agent.NewKeyring()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := ring.Add(agent.AddedKey{PrivateKey: priv, Comment: "test-newsigner"}); err != nil {
		t.Fatalf("ring.Add: %v", err)
	}
	fp, err := identity.FingerprintForKey(pub)
	if err != nil {
		t.Fatalf("FingerprintForKey: %v", err)
	}

	id := &identity.Identity{
		PublicKey:  pub,
		PrivateKey: priv,
	}
	backend, err := identity.NewSSHAgentBackendFromKeyring(ring, fp)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	id.SetBackend(backend)

	signer := id.NewSigner()

	msg := []byte("test message for NewSigner with backend")
	sig, err := signer.Sign(msg)
	if err != nil {
		t.Fatalf("signer.Sign: %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Errorf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(backend.PublicKey(), msg, sig) {
		t.Error("NewSigner signature does not verify against backend public key")
	}
	if !signer.PublicKey().Equal(pub) {
		t.Error("signer.PublicKey() does not match expected public key")
	}
}

// TestIdentityNewSigner_WithoutBackend verifies that NewSigner() without a
// backend uses the in-memory private key (regression guard for campfire-874).
func TestIdentityNewSigner_WithoutBackend(t *testing.T) {
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}

	signer := id.NewSigner()

	msg := []byte("test message for NewSigner without backend")
	sig, err := signer.Sign(msg)
	if err != nil {
		t.Fatalf("signer.Sign: %v", err)
	}
	if !ed25519.Verify(id.PublicKey, msg, sig) {
		t.Error("NewSigner signature (no backend) does not verify")
	}
}

// TestInit_BackendKeyMismatch_ReturnsError verifies that Init returns an error
// when the ssh-agent backend key does not match the identity file key (12d).
//
// Uses an in-process ssh-agent on a unix socket (SSH_AUTH_SOCK) with key B,
// and an identity file with key A. Calling Init with fingerprint(B) must fail.
func TestInit_BackendKeyMismatch_ReturnsError(t *testing.T) {
	dir := t.TempDir()

	// Key A: identity file.
	pubA, privA, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey A: %v", err)
	}
	idA := &identity.Identity{PublicKey: pubA, PrivateKey: privA}
	idPath := filepath.Join(dir, "identity.json")
	if err := idA.Save(idPath); err != nil {
		t.Fatalf("Save identity A: %v", err)
	}

	// Key B: ssh-agent — different from A.
	_, privB, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey B: %v", err)
	}
	pubB := privB.Public().(ed25519.PublicKey)
	fpB, err := identity.FingerprintForKey(pubB)
	if err != nil {
		t.Fatalf("FingerprintForKey B: %v", err)
	}

	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: privB, Comment: "mismatch-key"}); err != nil {
		t.Fatalf("ring.Add B: %v", err)
	}

	sock, stopAgent := startUnixAgent(t, ring)
	defer stopAgent()

	t.Setenv("SSH_AUTH_SOCK", sock)

	_, _, err = protocol.Init(dir,
		protocol.WithBackend("ssh-agent"),
		protocol.WithFingerprint(fpB),
	)
	if err == nil {
		t.Fatal("Init must return an error when backend key does not match identity key")
	}
	t.Logf("key mismatch error (expected): %v", err)

	// Sanity: confirm A and B are actually different.
	sshPubA, _ := gossh.NewPublicKey(pubA)
	sshPubB, _ := gossh.NewPublicKey(pubB)
	if gossh.FingerprintSHA256(sshPubA) == gossh.FingerprintSHA256(sshPubB) {
		t.Fatal("test setup error: key A and key B must be different")
	}

}

// TestInit_BackendKeyMatch_Succeeds verifies that Init succeeds when the
// ssh-agent backend key matches the identity file key (12d happy path).
func TestInit_BackendKeyMatch_Succeeds(t *testing.T) {
	dir := t.TempDir()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	fp, err := identity.FingerprintForKey(pub)
	if err != nil {
		t.Fatalf("FingerprintForKey: %v", err)
	}

	id := &identity.Identity{PublicKey: pub, PrivateKey: priv}
	idPath := filepath.Join(dir, "identity.json")
	if err := id.Save(idPath); err != nil {
		t.Fatalf("Save identity: %v", err)
	}

	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: priv, Comment: "matching-key"}); err != nil {
		t.Fatalf("ring.Add: %v", err)
	}

	sock, stopAgent := startUnixAgent(t, ring)
	defer stopAgent()

	t.Setenv("SSH_AUTH_SOCK", sock)

	c, _, err := protocol.Init(dir,
		protocol.WithBackend("ssh-agent"),
		protocol.WithFingerprint(fp),
	)
	if err != nil {
		t.Fatalf("Init with matching keys must succeed: %v", err)
	}
	defer c.Close()

	if !c.ClientIdentity().HasBackend() {
		t.Error("identity should have a backend configured after Init with ssh-agent")
	}
}
