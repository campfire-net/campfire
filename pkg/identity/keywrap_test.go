package identity

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveWrappedLoadWithToken verifies the round-trip: generate → SaveWrapped → LoadWithToken.
func TestSaveWrappedLoadWithToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	token := []byte("test-session-token")

	if err := id.SaveWrapped(path, token); err != nil {
		t.Fatalf("SaveWrapped: %v", err)
	}

	loaded, err := LoadWithToken(path, token)
	if err != nil {
		t.Fatalf("LoadWithToken: %v", err)
	}

	if loaded.PublicKeyHex() != id.PublicKeyHex() {
		t.Errorf("public key mismatch: got %s, want %s", loaded.PublicKeyHex(), id.PublicKeyHex())
	}
	if !bytes.Equal(loaded.PrivateKey, id.PrivateKey) {
		t.Error("private key mismatch after round-trip")
	}
	if loaded.CreatedAt != id.CreatedAt {
		t.Errorf("created_at mismatch: got %d, want %d", loaded.CreatedAt, id.CreatedAt)
	}
}

// TestSaveWrappedV1StillWorks ensures that a plain (v1) identity still loads
// after the v2 format has been introduced.
func TestSaveWrappedV1StillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := id.Save(path); err != nil {
		t.Fatalf("Save (v1): %v", err)
	}

	// Load via the default Load (no session token).
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load v1: %v", err)
	}
	if loaded.PublicKeyHex() != id.PublicKeyHex() {
		t.Errorf("public key mismatch: got %s, want %s", loaded.PublicKeyHex(), id.PublicKeyHex())
	}

	// Load via LoadWithToken — token should be ignored for v1.
	loaded2, err := LoadWithToken(path, []byte("some-token"))
	if err != nil {
		t.Fatalf("LoadWithToken on v1: %v", err)
	}
	if loaded2.PublicKeyHex() != id.PublicKeyHex() {
		t.Errorf("LoadWithToken v1 public key mismatch")
	}
}

// TestLoadWrappedWrongTokenFails checks that a wrong session token returns an error.
func TestLoadWrappedWrongTokenFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	correctToken := []byte("correct-token")
	wrongToken := []byte("wrong-token")

	if err := id.SaveWrapped(path, correctToken); err != nil {
		t.Fatalf("SaveWrapped: %v", err)
	}

	_, err = LoadWithToken(path, wrongToken)
	if err == nil {
		t.Fatal("LoadWithToken with wrong token: expected error, got nil")
	}
}

// TestLoadWrappedViaEnvVar verifies that a v2 file can be unwrapped using
// the CF_SESSION_TOKEN environment variable.
func TestLoadWrappedViaEnvVar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	token := "env-var-token"

	if err := id.SaveWrapped(path, []byte(token)); err != nil {
		t.Fatalf("SaveWrapped: %v", err)
	}

	t.Setenv("CF_SESSION_TOKEN", token)

	// Load() picks up CF_SESSION_TOKEN automatically.
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load with CF_SESSION_TOKEN: %v", err)
	}
	if loaded.PublicKeyHex() != id.PublicKeyHex() {
		t.Errorf("public key mismatch via env var")
	}
}

// TestLoadWrappedNoTokenFails verifies that a v2 file without a session token
// (and no env var) returns a clear error.
func TestLoadWrappedNoTokenFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := id.SaveWrapped(path, []byte("a-token")); err != nil {
		t.Fatalf("SaveWrapped: %v", err)
	}

	// Ensure env var is unset.
	os.Unsetenv("CF_SESSION_TOKEN")

	_, err = Load(path)
	if err == nil {
		t.Fatal("Load v2 without token: expected error, got nil")
	}
}

// TestSaveWrappedDoesNotStorePrivKeyInPlaintext verifies that the saved v2 file
// does not contain the raw private key bytes. This is a defence-in-depth check
// — if the file were leaked, the raw key should not be directly extractable.
func TestSaveWrappedDoesNotStorePrivKeyInPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := id.SaveWrapped(path, []byte("token")); err != nil {
		t.Fatalf("SaveWrapped: %v", err)
	}

	rawFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	// The file must not contain the "private_key" JSON field (used in v1).
	if bytes.Contains(rawFile, []byte(`"private_key"`)) {
		t.Error("v2 identity file contains 'private_key' field — plain key leaked to disk")
	}
}

// TestSaveWrappedFilePermissions verifies the saved file has mode 0600.
func TestSaveWrappedFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	id, _ := Generate()
	if err := id.SaveWrapped(path, []byte("token")); err != nil {
		t.Fatalf("SaveWrapped: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}

// TestWrappedIdentityCanSignAndVerify ensures the loaded wrapped identity is
// fully functional (not just key bytes — actual signing works).
func TestWrappedIdentityCanSignAndVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	id, _ := Generate()
	token := []byte("sign-test-token")
	id.SaveWrapped(path, token) //nolint:errcheck

	loaded, err := LoadWithToken(path, token)
	if err != nil {
		t.Fatalf("LoadWithToken: %v", err)
	}

	msg := []byte("campfire key wrap test message")
	sig := loaded.Sign(msg)
	if !loaded.Verify(msg, sig) {
		t.Error("loaded wrapped identity: Verify failed for own signature")
	}
	if !id.Verify(msg, sig) {
		t.Error("original identity: Verify failed for wrapped-loaded signature")
	}
}
