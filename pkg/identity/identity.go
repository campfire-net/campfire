package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Identity holds an Ed25519 keypair.
type Identity struct {
	PublicKey  ed25519.PublicKey  `json:"public_key"`
	PrivateKey ed25519.PrivateKey `json:"private_key"`
	CreatedAt  int64              `json:"created_at"`
}

// Generate creates a new Ed25519 identity.
func Generate() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating keypair: %w", err)
	}
	return &Identity{
		PublicKey:  pub,
		PrivateKey: priv,
		CreatedAt:  time.Now().UnixNano(),
	}, nil
}

// Save writes the identity to the given path with mode 0600.
func (id *Identity) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing identity: %w", err)
	}
	return nil
}

// Load reads an identity from the given path.
func Load(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading identity: %w", err)
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("parsing identity: %w", err)
	}
	if len(id.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("invalid public key size")
	}
	if len(id.PrivateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid private key size")
	}
	return &id, nil
}

// Exists returns true if an identity file exists at the given path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Sign signs the given message with the identity's private key.
func (id *Identity) Sign(message []byte) []byte {
	return ed25519.Sign(id.PrivateKey, message)
}

// Verify checks a signature against this identity's public key.
func (id *Identity) Verify(message, sig []byte) bool {
	return ed25519.Verify(id.PublicKey, message, sig)
}

// VerifyWith checks a signature against an arbitrary public key.
func VerifyWith(pub ed25519.PublicKey, message, sig []byte) bool {
	return ed25519.Verify(pub, message, sig)
}

// PublicKeyHex returns the hex-encoded public key.
func (id *Identity) PublicKeyHex() string {
	return fmt.Sprintf("%x", id.PublicKey)
}
