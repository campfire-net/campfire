package message

import (
	"crypto/ed25519"
	"fmt"
)

// Signer abstracts message signing. The default implementation wraps an
// Ed25519 keypair; future implementations can delegate to hardware tokens
// or an agent daemon without changing the message layer.
type Signer interface {
	// Sign returns a signature over the given message bytes.
	Sign(message []byte) ([]byte, error)
	// PublicKey returns the Ed25519 public key corresponding to the signing key.
	PublicKey() ed25519.PublicKey
}

// Ed25519Signer is the default Signer implementation, wrapping a raw Ed25519
// keypair loaded from disk (the standard campfire identity model).
type Ed25519Signer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// MustNewEd25519Signer constructs an Ed25519Signer from an existing keypair and
// panics if the keys are invalid. Intended for use in tests and program
// initialization paths where invalid keys represent a programming error.
func MustNewEd25519Signer(priv ed25519.PrivateKey, pub ed25519.PublicKey) *Ed25519Signer {
	s, err := NewEd25519Signer(priv, pub)
	if err != nil {
		panic(fmt.Sprintf("MustNewEd25519Signer: %v", err))
	}
	return s
}

// NewEd25519Signer constructs an Ed25519Signer from an existing keypair.
func NewEd25519Signer(priv ed25519.PrivateKey, pub ed25519.PublicKey) (*Ed25519Signer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key length: %d", len(priv))
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length: %d", len(pub))
	}
	return &Ed25519Signer{priv: priv, pub: pub}, nil
}

// Sign signs message with the Ed25519 private key.
func (s *Ed25519Signer) Sign(message []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, message), nil
}

// PublicKey returns the Ed25519 public key.
func (s *Ed25519Signer) PublicKey() ed25519.PublicKey {
	return s.pub
}
