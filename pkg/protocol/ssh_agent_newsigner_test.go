package protocol_test

// ssh_agent_newsigner_test.go — integration tests for campfire-c5f.
//
// Verifies that Client.Send routes message signing through Identity.NewSigner,
// which delegates to the configured Backend (SSHAgentBackend) rather than
// bypassing it with the in-memory private key.
//
// Uses an in-process keyring (no SSH_AUTH_SOCK required).

import (
	"fmt"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"golang.org/x/crypto/ssh/agent"
)

// TestSendFilesystem_SSHAgentBackend verifies that Client.Send works correctly
// when the identity has an SSHAgentBackend configured. The message must:
//   - Be created (no error)
//   - Have a valid signature verifiable against the agent's public key
//   - Have a Sender field matching the agent's public key
func TestSendFilesystem_SSHAgentBackend(t *testing.T) {
	// Create an in-process keyring and add a key.
	ring := agent.NewKeyring()
	pub, priv, fp, err := addKeyToKeyringTest(ring, "newsigner-protocol-test")
	if err != nil {
		t.Fatalf("addKeyToKeyringTest: %v", err)
	}
	_ = priv // held by the agent; this process doesn't need it

	// Build the SSHAgentBackend from the keyring.
	b, err := identity.NewSSHAgentBackendFromKeyring(ring, fp)
	if err != nil {
		t.Fatalf("NewSSHAgentBackendFromKeyring: %v", err)
	}
	defer b.Close()

	// Build a synthetic identity: public key from the agent, no private key,
	// Backend configured. This mirrors the production ssh-agent case.
	agentID := &identity.Identity{
		PublicKey: pub,
		Backend:   b,
	}

	// Set up test store and filesystem campfire.
	_, s, transportDir := setupTestEnv(t)

	// We need to add our agentID as the member, not a freshly generated one.
	// Use setupFilesystemCampfire but pass our agentID.
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("ssh-agent backend send test"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send with SSHAgentBackend: %v", err)
	}
	if msg == nil {
		t.Fatal("Send returned nil message")
	}

	// Sender must match the agent's public key.
	if fmt.Sprintf("%x", msg.Sender) != fmt.Sprintf("%x", pub) {
		t.Errorf("sender = %x, want %x", msg.Sender, pub)
	}

	// Signature must be valid.
	if !msg.VerifySignature() {
		t.Error("message signed via SSHAgentBackend has invalid signature")
	}
}

// addKeyToKeyringTest generates an ed25519 key, adds it to the keyring, and
// returns the public key, private key, and SHA256 fingerprint string.
// This mirrors the unexported addKeyToKeyring helper in pkg/identity.
func addKeyToKeyringTest(ring agent.Agent, comment string) (
	pub []byte,
	priv []byte,
	fingerprint string,
	err error,
) {
	// Use identity package helpers to get keys with the correct format.
	id, genErr := identity.Generate()
	if genErr != nil {
		return nil, nil, "", genErr
	}
	if addErr := ring.Add(agent.AddedKey{PrivateKey: id.PrivateKey, Comment: comment}); addErr != nil {
		return nil, nil, "", addErr
	}
	fp, fpErr := identity.FingerprintForKey(id.PublicKey)
	if fpErr != nil {
		return nil, nil, "", fpErr
	}
	return id.PublicKey, id.PrivateKey, fp, nil
}
