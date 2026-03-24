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

	cfcrypto "github.com/campfire-net/campfire/pkg/crypto"
)

// fileVersion constants distinguish identity file formats.
//
//   Version 1 (default, zero-value): plain Ed25519 private key stored as-is.
//   Version 2: private key is AES-256-GCM wrapped with a KEK derived from a
//              session token (see pkg/crypto.WrapKey / UnwrapKey).
const (
	versionPlain   = 1
	versionWrapped = 2
)

// Identity holds an Ed25519 keypair.
type Identity struct {
	PublicKey  ed25519.PublicKey  `json:"public_key"`
	PrivateKey ed25519.PrivateKey `json:"private_key"`
	CreatedAt  int64              `json:"created_at"`
}

// identityFile is the on-disk JSON representation. Version 0 (absent field)
// and version 1 are both treated as plain (unwrapped) for backward compat.
type identityFile struct {
	Version    int              `json:"version,omitempty"`
	PublicKey  ed25519.PublicKey `json:"public_key"`
	PrivateKey ed25519.PrivateKey `json:"private_key,omitempty"`
	WrappedKey []byte            `json:"wrapped_key,omitempty"`
	CreatedAt  int64             `json:"created_at"`
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
// The file is written in v1 (plain) format. To write a wrapped file,
// use SaveWrapped.
func (id *Identity) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	f := identityFile{
		Version:    versionPlain,
		PublicKey:  id.PublicKey,
		PrivateKey: id.PrivateKey,
		CreatedAt:  id.CreatedAt,
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing identity: %w", err)
	}
	return nil
}

// SaveWrapped writes the identity in v2 (wrapped) format. The private key is
// encrypted with a KEK derived from sessionToken. The plain private key is
// NOT written to disk — only the wrapped blob.
func (id *Identity) SaveWrapped(path string, sessionToken []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	wrapped, err := cfcrypto.WrapKey(id.PrivateKey, sessionToken)
	if err != nil {
		return fmt.Errorf("wrapping private key: %w", err)
	}
	f := identityFile{
		Version:    versionWrapped,
		PublicKey:  id.PublicKey,
		WrappedKey: wrapped,
		CreatedAt:  id.CreatedAt,
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing identity: %w", err)
	}
	return nil
}

// Load reads an identity from the given path.
//
// For v1 (plain) files: loads directly. Backward-compatible with files that
// have no version field (treated as v1).
//
// For v2 (wrapped) files: reads CF_SESSION_TOKEN from the environment and
// unwraps the private key. Returns an error if the env var is absent or wrong.
func Load(path string) (*Identity, error) {
	return LoadWithToken(path, nil)
}

// LoadWithToken reads an identity from the given path. If sessionToken is
// non-nil it is used to unwrap v2 files; otherwise CF_SESSION_TOKEN is read
// from the environment.
//
// For v1 (plain) files sessionToken is ignored.
func LoadWithToken(path string, sessionToken []byte) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading identity: %w", err)
	}
	var f identityFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing identity: %w", err)
	}

	if len(f.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("invalid public key size")
	}

	switch f.Version {
	case 0, versionPlain: // 0 = legacy files without a version field
		if len(f.PrivateKey) != ed25519.PrivateKeySize {
			return nil, errors.New("invalid private key size")
		}
		return &Identity{
			PublicKey:  f.PublicKey,
			PrivateKey: f.PrivateKey,
			CreatedAt:  f.CreatedAt,
		}, nil

	case versionWrapped:
		if len(f.WrappedKey) == 0 {
			return nil, errors.New("v2 identity file has no wrapped_key")
		}
		tok := sessionToken
		if tok == nil {
			env := os.Getenv("CF_SESSION_TOKEN")
			if env == "" {
				return nil, errors.New("identity is wrapped (v2): set CF_SESSION_TOKEN to unwrap")
			}
			tok = []byte(env)
		}
		privKey, err := cfcrypto.UnwrapKey(f.WrappedKey, tok)
		if err != nil {
			return nil, fmt.Errorf("unwrapping identity: %w", err)
		}
		if len(privKey) != ed25519.PrivateKeySize {
			return nil, errors.New("unwrapped key has wrong size")
		}
		return &Identity{
			PublicKey:  f.PublicKey,
			PrivateKey: ed25519.PrivateKey(privKey),
			CreatedAt:  f.CreatedAt,
		}, nil

	default:
		return nil, fmt.Errorf("unknown identity file version %d", f.Version)
	}
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
