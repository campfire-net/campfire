package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerate(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if len(id.PublicKey) != 32 {
		t.Errorf("public key length = %d, want 32", len(id.PublicKey))
	}
	if len(id.PrivateKey) != 64 {
		t.Errorf("private key length = %d, want 64", len(id.PrivateKey))
	}
	if id.CreatedAt == 0 {
		t.Error("created_at should be non-zero")
	}
}

func TestSignVerify(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	msg := []byte("hello campfire")
	sig := id.Sign(msg)

	if !id.Verify(msg, sig) {
		t.Error("signature should verify")
	}
	if id.Verify([]byte("wrong message"), sig) {
		t.Error("signature should not verify for wrong message")
	}
	if !VerifyWith(id.PublicKey, msg, sig) {
		t.Error("VerifyWith should verify valid signature")
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if err := id.Save(path); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Check file permissions
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.PublicKeyHex() != id.PublicKeyHex() {
		t.Errorf("loaded public key = %s, want %s", loaded.PublicKeyHex(), id.PublicKeyHex())
	}
	if loaded.CreatedAt != id.CreatedAt {
		t.Errorf("loaded created_at = %d, want %d", loaded.CreatedAt, id.CreatedAt)
	}

	// Verify loaded identity can sign/verify
	msg := []byte("round trip test")
	sig := loaded.Sign(msg)
	if !loaded.Verify(msg, sig) {
		t.Error("loaded identity should sign and verify")
	}
	if !id.Verify(msg, sig) {
		t.Error("original identity should verify signature from loaded identity")
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	if Exists(path) {
		t.Error("Exists should return false for non-existent file")
	}

	id, _ := Generate()
	id.Save(path)

	if !Exists(path) {
		t.Error("Exists should return true for existing file")
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "identity.json")

	id, _ := Generate()
	if err := id.Save(path); err != nil {
		t.Fatalf("Save() should create nested directories: %v", err)
	}
	if !Exists(path) {
		t.Error("file should exist after save")
	}
}

func TestLoadInvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	// Invalid JSON
	os.WriteFile(path, []byte("not json"), 0600)
	_, err := Load(path)
	if err == nil {
		t.Error("Load should fail on invalid JSON")
	}

	// Missing file
	_, err = Load(filepath.Join(dir, "nonexistent.json"))
	if err == nil {
		t.Error("Load should fail on missing file")
	}
}

func TestPublicKeyHex(t *testing.T) {
	id, _ := Generate()
	hex := id.PublicKeyHex()
	if len(hex) != 64 {
		t.Errorf("hex public key length = %d, want 64", len(hex))
	}
}

// Tests for workspace-yzx: identity.Load invalid key sizes not tested.

func TestLoadInvalidPublicKeySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	// Write JSON with a short public key (not 32 bytes).
	data := `{"public_key":"AAEC","private_key":"` + validPrivKeyBase64() + `","created_at":1}`
	os.WriteFile(path, []byte(data), 0600)

	_, err := Load(path)
	if err == nil {
		t.Error("Load should fail when public key is not 32 bytes")
	}
}

func TestLoadInvalidPrivateKeySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	// Write JSON with a short private key (not 64 bytes).
	data := `{"public_key":"` + validPubKeyBase64() + `","private_key":"AAEC","created_at":1}`
	os.WriteFile(path, []byte(data), 0600)

	_, err := Load(path)
	if err == nil {
		t.Error("Load should fail when private key is not 64 bytes")
	}
}

func TestLoadValidKeySizes(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")
	if err := id.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load should succeed with valid key sizes: %v", err)
	}
	if len(loaded.PublicKey) != 32 {
		t.Errorf("loaded public key size = %d, want 32", len(loaded.PublicKey))
	}
	if len(loaded.PrivateKey) != 64 {
		t.Errorf("loaded private key size = %d, want 64", len(loaded.PrivateKey))
	}
}

// validPubKeyBase64 returns a base64-encoded 32-byte zeroed public key for test JSON.
func validPubKeyBase64() string {
	return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
}

// validPrivKeyBase64 returns a base64-encoded 64-byte zeroed private key for test JSON.
func validPrivKeyBase64() string {
	return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
}
