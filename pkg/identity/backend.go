package identity

// backend.go — SigningBackend abstraction for root identity signing.
//
// Only the human-rooted top of the identity tree uses a signing backend.
// Machine daemons, Claude sessions, and workers stay file-based.
//
// Two implementations:
//   - FileBackend: wraps the ed25519 private key in memory (default, current behaviour).
//   - SSHAgentBackend: delegates signing to an ssh-agent via SSH_AUTH_SOCK.
//     The agent holds the private key; this process never sees it.
//
// Wire format note (from spike docs/specs/ssh-agent-signing-notes.md):
//   agent.Sign returns *ssh.Signature where Blob is the bare 64-byte
//   ed25519 signature — identical to ed25519.Sign output. No unwrapping needed.

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SigningBackend is the interface for root-identity signing operations.
// Both FileBackend and SSHAgentBackend satisfy this interface.
type SigningBackend interface {
	// Sign signs message and returns the raw 64-byte ed25519 signature.
	Sign(message []byte) ([]byte, error)

	// PublicKey returns the ed25519 public key corresponding to the signing key.
	PublicKey() ed25519.PublicKey

	// Close releases any resources held by the backend (e.g. socket connections).
	Close() error
}

// ------------------------------------------------------------------ FileBackend

// FileBackend wraps an in-memory ed25519 private key. This is the default
// backend and exactly replicates the previous Identity.Sign behaviour.
type FileBackend struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// NewFileBackend creates a FileBackend from the given ed25519 private key.
func NewFileBackend(priv ed25519.PrivateKey) (*FileBackend, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("identity/backend: FileBackend requires a %d-byte private key, got %d", ed25519.PrivateKeySize, len(priv))
	}
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	copy(pub, priv[ed25519.PublicKeySize:]) // last 32 bytes of ed25519.PrivateKey are the public key
	return &FileBackend{priv: priv, pub: pub}, nil
}

// Sign implements SigningBackend using the in-memory private key.
func (b *FileBackend) Sign(message []byte) ([]byte, error) {
	return ed25519.Sign(b.priv, message), nil
}

// PublicKey implements SigningBackend.
func (b *FileBackend) PublicKey() ed25519.PublicKey {
	return b.pub
}

// Close is a no-op for FileBackend (no external resources).
func (b *FileBackend) Close() error {
	return nil
}

// -------------------------------------------------------------- SSHAgentBackend

// SSHAgentBackend delegates signing to an ssh-agent process.
// The private key never enters this process — only the public key and the
// SHA256 fingerprint are held here.
//
// Wire format: agent.Sign returns *ssh.Signature where Blob is the bare
// 64-byte ed25519 signature. See docs/specs/ssh-agent-signing-notes.md.
type SSHAgentBackend struct {
	fingerprint string // "SHA256:..." format
	pub         ed25519.PublicKey
	sshPub      gossh.PublicKey
	agentConn   net.Conn
	agentClient agent.ExtendedAgent
}

// agentFromRing is a thin wrapper for the in-process keyring path (tests only).
// It stores the agent.Agent directly rather than going through a socket.
type agentBackendDirect struct {
	SSHAgentBackend
	ring agent.Agent
}

func (b *agentBackendDirect) Sign(message []byte) ([]byte, error) {
	sig, err := b.ring.Sign(b.sshPub, message)
	if err != nil {
		return nil, fmt.Errorf("identity/backend: ssh-agent sign: %w", err)
	}
	// Spike answer (A): Blob is the bare 64-byte ed25519 signature.
	if len(sig.Blob) != ed25519.SignatureSize {
		return nil, fmt.Errorf("identity/backend: unexpected signature blob length %d (want %d)", len(sig.Blob), ed25519.SignatureSize)
	}
	// Verify the signature before returning — catches a malfunctioning or
	// malicious agent that returns a syntactically valid but incorrect signature.
	// Consistent with SSHAgentBackend.Sign which performs the same check.
	if !ed25519.Verify(b.pub, message, sig.Blob) {
		return nil, fmt.Errorf("identity/backend: ssh-agent returned invalid signature for key %s", b.fingerprint)
	}
	return sig.Blob, nil
}

func (b *agentBackendDirect) Close() error { return nil }

// NewSSHAgentBackendFromKeyring is the test-friendly constructor: wraps an
// in-process agent.NewKeyring() without a real socket.
func NewSSHAgentBackendFromKeyring(ring agent.Agent, fingerprint string) (SigningBackend, error) {
	if fingerprint == "" {
		return nil, errors.New("identity/backend: SSHAgentBackend requires a non-empty fingerprint")
	}

	keys, err := ring.List()
	if err != nil {
		return nil, fmt.Errorf("identity/backend: listing agent keys: %w", err)
	}

	var matchedSSHPub gossh.PublicKey
	var matchedPub ed25519.PublicKey
	for _, k := range keys {
		fp := gossh.FingerprintSHA256(k)
		if fp == fingerprint {
			parsed, err := gossh.ParsePublicKey(k.Marshal())
			if err != nil {
				return nil, fmt.Errorf("identity/backend: parsing agent key: %w", err)
			}
			cpk, ok := parsed.(gossh.CryptoPublicKey)
			if !ok {
				return nil, fmt.Errorf("identity/backend: key %s does not implement CryptoPublicKey", fingerprint)
			}
			ed, ok := cpk.CryptoPublicKey().(ed25519.PublicKey)
			if !ok {
				return nil, fmt.Errorf("identity/backend: key %s is not an ed25519 key", fingerprint)
			}
			matchedSSHPub = k
			matchedPub = ed
			break
		}
	}

	if matchedSSHPub == nil {
		return nil, fmt.Errorf("identity/backend: no key with fingerprint %s found in agent", fingerprint)
	}

	b := &agentBackendDirect{
		SSHAgentBackend: SSHAgentBackend{
			fingerprint: fingerprint,
			pub:         matchedPub,
			sshPub:      matchedSSHPub,
		},
		ring: ring,
	}
	return b, nil
}

// NewSSHAgentBackend connects to the ssh-agent at SSH_AUTH_SOCK and finds
// the key matching the given SHA256 fingerprint (e.g. "SHA256:abc...").
//
// Returns an error if:
//   - SSH_AUTH_SOCK is not set
//   - The socket is unreachable
//   - No key with the given fingerprint exists in the agent
//   - The matching key is not an ed25519 key
func NewSSHAgentBackend(fingerprint string) (*SSHAgentBackend, error) {
	if fingerprint == "" {
		return nil, errors.New("identity/backend: fingerprint is required for ssh-agent backend")
	}

	sockPath := os.Getenv("SSH_AUTH_SOCK")
	if sockPath == "" {
		return nil, errors.New("identity/backend: SSH_AUTH_SOCK is not set")
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("identity/backend: connecting to ssh-agent at %s: %w", sockPath, err)
	}

	ac := agent.NewClient(conn)

	keys, err := ac.List()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("identity/backend: listing agent keys: %w", err)
	}

	var matchedSSHPub gossh.PublicKey
	var matchedPub ed25519.PublicKey
	for _, k := range keys {
		fp := gossh.FingerprintSHA256(k)
		if fp == fingerprint {
			parsed, parseErr := gossh.ParsePublicKey(k.Marshal())
			if parseErr != nil {
				conn.Close()
				return nil, fmt.Errorf("identity/backend: parsing agent key: %w", parseErr)
			}
			cpk, ok := parsed.(gossh.CryptoPublicKey)
			if !ok {
				conn.Close()
				return nil, fmt.Errorf("identity/backend: key %s does not implement CryptoPublicKey", fingerprint)
			}
			ed, ok := cpk.CryptoPublicKey().(ed25519.PublicKey)
			if !ok {
				conn.Close()
				return nil, fmt.Errorf("identity/backend: key %s is not an ed25519 key", fingerprint)
			}
			matchedSSHPub = k
			matchedPub = ed
			break
		}
	}

	if matchedSSHPub == nil {
		conn.Close()
		return nil, fmt.Errorf("identity/backend: no key with fingerprint %s found in agent", fingerprint)
	}

	return &SSHAgentBackend{
		fingerprint: fingerprint,
		pub:         matchedPub,
		sshPub:      matchedSSHPub,
		agentConn:   conn,
		agentClient: ac,
	}, nil
}

// Sign implements SigningBackend by delegating to the ssh-agent.
// Returns the bare 64-byte ed25519 signature (Answer A from spike).
func (b *SSHAgentBackend) Sign(message []byte) ([]byte, error) {
	sig, err := b.agentClient.Sign(b.sshPub, message)
	if err != nil {
		return nil, fmt.Errorf("identity/backend: ssh-agent sign: %w", err)
	}
	// Spike answer (A): sig.Blob is the bare 64-byte ed25519 signature.
	if len(sig.Blob) != ed25519.SignatureSize {
		return nil, fmt.Errorf("identity/backend: unexpected signature blob length %d (want %d)", len(sig.Blob), ed25519.SignatureSize)
	}
	// Verify the signature before returning — catches a malfunctioning or
	// malicious agent that returns a syntactically valid but incorrect signature.
	if !ed25519.Verify(b.pub, message, sig.Blob) {
		return nil, fmt.Errorf("identity/backend: ssh-agent returned invalid signature for key %s", b.fingerprint)
	}
	return sig.Blob, nil
}

// PublicKey implements SigningBackend.
func (b *SSHAgentBackend) PublicKey() ed25519.PublicKey {
	return b.pub
}

// Close closes the connection to the ssh-agent.
func (b *SSHAgentBackend) Close() error {
	if b.agentConn != nil {
		return b.agentConn.Close()
	}
	return nil
}

// ------------------------------------------------------------ helpers (testing)

// FingerprintForKey returns the SHA256 fingerprint string for an ed25519 public key.
// Useful in tests to derive the fingerprint before creating the backend.
func FingerprintForKey(pub ed25519.PublicKey) (string, error) {
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("identity/backend: converting to ssh public key: %w", err)
	}
	return gossh.FingerprintSHA256(sshPub), nil
}

// addKeyToKeyring is a test helper that adds a generated ed25519 key to a
// keyring and returns the fingerprint. Not exported — test-only use.
func addKeyToKeyring(ring agent.Agent, comment string) (ed25519.PublicKey, ed25519.PrivateKey, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, "", err
	}
	if err := ring.Add(agent.AddedKey{PrivateKey: priv, Comment: comment}); err != nil {
		return nil, nil, "", err
	}
	fp, err := FingerprintForKey(pub)
	if err != nil {
		return nil, nil, "", err
	}
	return pub, priv, fp, nil
}
