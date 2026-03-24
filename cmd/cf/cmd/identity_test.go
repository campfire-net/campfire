package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
)

// captureIdentityWrap runs cf identity wrap with the given CF_HOME and token,
// capturing stdout output.
func captureIdentityWrap(t *testing.T, cfHomeDir, token string) (stdout string, err error) {
	t.Helper()

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	origStdout := os.Stdout
	os.Stdout = w

	t.Setenv("CF_HOME", cfHomeDir)
	identityWrapCmd.Flags().Set("token", token) //nolint:errcheck
	rootCmd.SetArgs([]string{"identity", "wrap"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	r.Close()

	// Reset flag for next test.
	identityWrapCmd.Flags().Set("token", "") //nolint:errcheck

	return string(buf[:n]), runErr
}

// TestIdentityWrapCommand creates a plain identity, wraps it via the CLI,
// then verifies the wrapped file can be unwrapped with the correct token.
func TestIdentityWrapCommand(t *testing.T) {
	dir := t.TempDir()

	// Create a plain identity first.
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	token := "wrap-cmd-test-token"
	out, err := captureIdentityWrap(t, dir, token)
	if err != nil {
		t.Fatalf("cf identity wrap: %v", err)
	}

	// Output should mention "wrapped".
	if !strings.Contains(out, "wrapped") {
		t.Errorf("expected 'wrapped' in output, got: %q", out)
	}

	// Wrapped file should now load with the token and produce the same identity.
	loaded, err := identity.LoadWithToken(filepath.Join(dir, "identity.json"), []byte(token))
	if err != nil {
		t.Fatalf("LoadWithToken after wrap command: %v", err)
	}
	if loaded.PublicKeyHex() != id.PublicKeyHex() {
		t.Errorf("public key mismatch: got %s, want %s", loaded.PublicKeyHex(), id.PublicKeyHex())
	}
}

// TestIdentityWrapCommandNoToken ensures the command returns an error when no
// session token is provided.
func TestIdentityWrapCommandNoToken(t *testing.T) {
	dir := t.TempDir()

	id, _ := identity.Generate()
	id.Save(filepath.Join(dir, "identity.json")) //nolint:errcheck

	// No token, no env var.
	os.Unsetenv("CF_SESSION_TOKEN")
	_, err := captureIdentityWrap(t, dir, "")
	if err == nil {
		t.Fatal("cf identity wrap with no token: expected error, got nil")
	}
}
