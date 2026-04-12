package identity

// SSH-agent wire-format spike.
//
// Question: does agent.Sign() for an ed25519 key return a bare 64-byte
// signature in Signature.Blob, or an SSH wire-format blob that must be
// unwrapped?
//
// This test uses an in-process keyring (golang.org/x/crypto/ssh/agent) so it
// works in any environment, including CI with no real ssh-agent binary.
//
// Run with: go test ./pkg/identity/ -run TestSSHAgentSignWireFormat -v

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestSSHAgentSignWireFormat(t *testing.T) {
	// 1. Generate an ed25519 keypair.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// 2. Add key to an in-process keyring (equivalent to ssh-add).
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: priv, Comment: "spike-test"}); err != nil {
		t.Fatalf("add key: %v", err)
	}

	// 3. Get the SSH public key handle used to request signing.
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}

	// 4. Sign some data via the agent.
	data := []byte("hello campfire wire-format spike")
	agentSig, err := ring.Sign(sshPub, data)
	if err != nil {
		t.Fatalf("agent Sign: %v", err)
	}

	// 5. Sign the same data directly with ed25519 (the ground truth).
	directSig := ed25519.Sign(priv, data)

	// 6. Log both for comparison.
	t.Logf("agent Signature.Format : %s", agentSig.Format)
	t.Logf("agent Signature.Blob   : len=%d  hex=%s", len(agentSig.Blob), hex.EncodeToString(agentSig.Blob))
	t.Logf("direct ed25519.Sign    : len=%d  hex=%s", len(directSig), hex.EncodeToString(directSig))

	// 7. Determine answer.
	if string(agentSig.Blob) == string(directSig) {
		t.Log("RESULT: Signature.Blob == bare ed25519.Sign output  =>  ANSWER (A)")
	} else {
		// Try to parse as SSH wire-format: uint32(len) || string
		// ssh.Signature wire = [uint32 format_len][format bytes][uint32 blob_len][blob bytes]
		// But agent.Sign() has already called ssh.Unmarshal on the wire blob,
		// so Blob should already be unwrapped.
		t.Logf("RESULT: Signature.Blob != bare sig — checking if it wraps the direct sig")
		wrapped := fmt.Sprintf("%s", agentSig.Blob)
		_ = wrapped
		t.Errorf("UNEXPECTED: Signature.Blob does not match bare ed25519 output — investigation needed")
		return
	}

	// 8. Verify that Signature.Format is "ssh-ed25519".
	if agentSig.Format != gossh.KeyAlgoED25519 {
		t.Errorf("unexpected Format %q, want %q", agentSig.Format, gossh.KeyAlgoED25519)
	}

	// 9. Verify that the bare Blob passes stdlib ed25519.Verify directly
	//    (proving Blob is truly bare, not SSH-wrapped).
	if !ed25519.Verify(pub, data, agentSig.Blob) {
		t.Error("ed25519.Verify(pub, data, agentSig.Blob) failed — Blob is NOT bare")
	} else {
		t.Log("ed25519.Verify(pub, data, agentSig.Blob) PASSED — Blob is bare 64 bytes")
	}

	// 10. Verify Signature.Blob length is exactly 64.
	if len(agentSig.Blob) != 64 {
		t.Errorf("Blob length = %d, want 64", len(agentSig.Blob))
	}
}
