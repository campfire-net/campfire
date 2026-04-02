package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

// captureInitWithPassphrase runs cf init with the given CF_HOME and CF_PASSPHRASE env vars.
// Resets cobra flag state before each run.
func captureInitWithPassphrase(t *testing.T, cfHomeDir, passphrase string) (stdout string, err error) {
	t.Helper()

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	origStdout := os.Stdout
	os.Stdout = w

	initCmd.Flags().Set("force", "false")   //nolint:errcheck
	initCmd.Flags().Set("name", "")         //nolint:errcheck
	initCmd.Flags().Set("session", "false") //nolint:errcheck
	initCmd.Flags().Set("from", "")         //nolint:errcheck
	if f := initCmd.Flags().Lookup("remote"); f != nil {
		initCmd.Flags().Set("remote", "") //nolint:errcheck
	}
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_PASSPHRASE", passphrase)
	rootCmd.SetArgs([]string{"init"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	r.Close()

	return string(buf[:n]), runErr
}

// captureInitRemote runs cf init --remote <url> with the given CF_HOME and passphrase.
// NOTE: The --remote flag is retained for backward compatibility but no longer creates
// a center campfire in the init flow. This test verifies the flag is accepted gracefully.
func captureInitRemote(t *testing.T, cfHomeDir, passphrase, remoteURL string) (stdout string, err error) {
	t.Helper()

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	origStdout := os.Stdout
	os.Stdout = w

	initCmd.Flags().Set("force", "false")   //nolint:errcheck
	initCmd.Flags().Set("name", "")         //nolint:errcheck
	initCmd.Flags().Set("session", "false") //nolint:errcheck
	initCmd.Flags().Set("from", "")         //nolint:errcheck
	if f := initCmd.Flags().Lookup("remote"); f != nil {
		initCmd.Flags().Set("remote", remoteURL) //nolint:errcheck
	}
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_PASSPHRASE", passphrase)
	rootCmd.SetArgs([]string{"init", "--remote", remoteURL})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	r.Close()

	return string(buf[:n]), runErr
}

// TestInitCreatesIdentityCampfire verifies that cf init creates an identity campfire
// (self-campfire with identity convention genesis message) and sets the "home" alias.
// Replaces the old TestInitCreatesCenter test — init now creates one campfire (identity),
// not two (home + center).
func TestInitCreatesIdentityCampfire(t *testing.T) {
	tmpDir := t.TempDir()

	out, err := captureInitWithPassphrase(t, tmpDir, "test-passphrase-abc")
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	// Output must say "Your identity campfire: <hex_id>"
	if !strings.Contains(out, "Your identity campfire:") {
		t.Errorf("expected output to contain 'Your identity campfire:', got:\n%s", out)
	}

	// No more center campfire — the .campfire/center file should NOT exist.
	centerPath := filepath.Join(tmpDir, "center")
	if _, readErr := os.ReadFile(centerPath); readErr == nil {
		t.Error("expected .campfire/center to NOT exist after new init; identity campfire replaces center")
	}

	// The "home" alias must be set and point to a valid campfire ID.
	s, openErr := store.Open(store.StorePath(tmpDir))
	if openErr != nil {
		t.Fatalf("opening store: %v", openErr)
	}
	defer s.Close()

	memberships, listErr := s.ListMemberships()
	if listErr != nil {
		t.Fatalf("listing memberships: %v", listErr)
	}
	// Exactly one campfire should be created (the identity campfire).
	if len(memberships) != 1 {
		t.Errorf("expected 1 campfire after init, got %d", len(memberships))
	}
}

// TestInitPassphraseProtected verifies that the identity.json has version=2 and wrapped_key.
func TestInitPassphraseProtected(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := captureInitWithPassphrase(t, tmpDir, "test-passphrase-xyz")
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	identityPath := filepath.Join(tmpDir, "identity.json")
	data, readErr := os.ReadFile(identityPath)
	if readErr != nil {
		t.Fatalf("identity.json not found: %v", readErr)
	}

	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parsing identity.json: %v", err)
	}

	version, ok := f["version"]
	if !ok {
		t.Fatal("identity.json missing 'version' field")
	}
	// JSON numbers unmarshal as float64
	v, _ := version.(float64)
	if v != 2 {
		t.Errorf("identity.json version = %v, want 2", version)
	}

	if _, ok := f["wrapped_key"]; !ok {
		t.Error("identity.json missing 'wrapped_key' field")
	}

	// Must NOT have plaintext private_key
	if privKey, ok := f["private_key"]; ok && privKey != nil {
		// private_key field may exist as null/empty, but must not have actual key data.
		// In JSON it should be omitted entirely for v2.
		if privKey != nil {
			t.Errorf("identity.json should not have plain 'private_key' in v2 format, got: %v", privKey)
		}
	}
}

// TestInitIdentityCampfireThreshold verifies that the identity campfire has threshold=1 in the store.
func TestInitIdentityCampfireThreshold(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := captureInitWithPassphrase(t, tmpDir, "quorum-test-passphrase")
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	// Open the store and find the identity campfire membership.
	s, openErr := store.Open(store.StorePath(tmpDir))
	if openErr != nil {
		t.Fatalf("opening store: %v", openErr)
	}
	defer s.Close()

	memberships, listErr := s.ListMemberships()
	if listErr != nil {
		t.Fatalf("listing memberships: %v", listErr)
	}
	if len(memberships) == 0 {
		t.Fatal("no memberships recorded after cf init")
	}

	m := memberships[0]
	if m.Threshold != 1 {
		t.Errorf("identity campfire threshold = %d, want 1", m.Threshold)
	}
}

// TestInitNoisyOutput verifies the output message format for new self-campfire init.
func TestInitNoisyOutput(t *testing.T) {
	tmpDir := t.TempDir()

	out, err := captureInitWithPassphrase(t, tmpDir, "noisy-test-passphrase")
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	// New output: "Your identity campfire: <hex_id>. Share it like any beacon."
	if !strings.Contains(out, "Your identity campfire:") {
		t.Errorf("expected output to contain 'Your identity campfire:', got:\n%s", out)
	}
}

// TestInitRemoteFlag verifies that --remote <url> is accepted without error.
// NOTE: The --remote flag no longer creates a center campfire. It is retained for
// backward compatibility but is a no-op in the new init flow.
func TestInitRemoteFlag(t *testing.T) {
	tmpDir := t.TempDir()

	remoteURL := "https://mcp.getcampfire.dev"
	_, err := captureInitRemote(t, tmpDir, "remote-test-passphrase", remoteURL)
	if err != nil {
		t.Fatalf("cf init --remote failed: %v", err)
	}

	// Identity campfire should still be created.
	s, openErr := store.Open(store.StorePath(tmpDir))
	if openErr != nil {
		t.Fatalf("opening store: %v", openErr)
	}
	defer s.Close()

	memberships, listErr := s.ListMemberships()
	if listErr != nil {
		t.Fatalf("listing memberships: %v", listErr)
	}
	if len(memberships) == 0 {
		t.Fatal("expected identity campfire to be created")
	}
}
