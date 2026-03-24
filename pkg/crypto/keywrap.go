package crypto

import "fmt"

// WrapKey encrypts privKey with a KEK derived from sessionToken using
// HKDF-SHA256 (info="campfire-key-wrap-v1") and AES-256-GCM.
//
// Returns nonce (12 bytes) || ciphertext || GCM tag.
//
// The session token is treated as opaque bytes; its format is not validated.
// Domain separation is provided by the fixed info string "campfire-key-wrap-v1".
func WrapKey(privKey, sessionToken []byte) ([]byte, error) {
	kek, err := HKDFSha256ZeroSalt(sessionToken, "campfire-key-wrap-v1")
	if err != nil {
		return nil, fmt.Errorf("WrapKey: deriving KEK: %w", err)
	}
	wrapped, err := AESGCMEncrypt(kek, privKey)
	if err != nil {
		return nil, fmt.Errorf("WrapKey: encrypting: %w", err)
	}
	return wrapped, nil
}

// UnwrapKey decrypts a wrapped key blob produced by WrapKey using the same
// session token. Returns the original private key bytes.
//
// Returns an error if the session token is wrong or the blob is corrupted.
func UnwrapKey(wrapped, sessionToken []byte) ([]byte, error) {
	kek, err := HKDFSha256ZeroSalt(sessionToken, "campfire-key-wrap-v1")
	if err != nil {
		return nil, fmt.Errorf("UnwrapKey: deriving KEK: %w", err)
	}
	privKey, err := AESGCMDecrypt(kek, wrapped)
	if err != nil {
		return nil, fmt.Errorf("UnwrapKey: decryption failed (wrong session token or corrupted blob): %w", err)
	}
	return privKey, nil
}
