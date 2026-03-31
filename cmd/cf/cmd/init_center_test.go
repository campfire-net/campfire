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

// TestInitCreatesCenter verifies that cf init creates .campfire/center with a valid campfire ID.
func TestInitCreatesCenter(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := captureInitWithPassphrase(t, tmpDir, "test-passphrase-abc")
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	centerPath := filepath.Join(tmpDir, "center")
	data, readErr := os.ReadFile(centerPath)
	if readErr != nil {
		t.Fatalf(".campfire/center not found: %v", readErr)
	}

	centerID := strings.TrimSpace(string(data))
	if len(centerID) != 64 {
		t.Errorf("center campfire ID should be 64-char hex, got %q (len %d)", centerID, len(centerID))
	}
	// Must be hex only
	for _, c := range centerID {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("center campfire ID contains non-hex character %q in %q", c, centerID)
			break
		}
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

// TestInitQuorumOne verifies that the center campfire has threshold=1 in the store.
func TestInitQuorumOne(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := captureInitWithPassphrase(t, tmpDir, "quorum-test-passphrase")
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	centerPath := filepath.Join(tmpDir, "center")
	data, readErr := os.ReadFile(centerPath)
	if readErr != nil {
		t.Fatalf(".campfire/center not found: %v", readErr)
	}
	centerID := strings.TrimSpace(string(data))

	// Open the store and look up the center campfire membership
	s, openErr := store.Open(store.StorePath(tmpDir))
	if openErr != nil {
		t.Fatalf("opening store: %v", openErr)
	}
	defer s.Close()

	m, getErr := s.GetMembership(centerID)
	if getErr != nil {
		t.Fatalf("getting center membership from store: %v", getErr)
	}

	if m.Threshold != 1 {
		t.Errorf("center campfire threshold = %d, want 1", m.Threshold)
	}
}

// TestInitNoisyOutput verifies the output message format.
func TestInitNoisyOutput(t *testing.T) {
	tmpDir := t.TempDir()

	out, err := captureInitWithPassphrase(t, tmpDir, "noisy-test-passphrase")
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	if !strings.Contains(out, "Created center campfire [fs]") {
		t.Errorf("expected output to contain 'Created center campfire [fs]', got:\n%s", out)
	}

	if !strings.Contains(out, "cf init --remote") {
		t.Errorf("expected output to contain 'cf init --remote', got:\n%s", out)
	}
}

// TestInitRemoteFlag verifies that --remote <url> stores the campfire with http transport type.
func TestInitRemoteFlag(t *testing.T) {
	tmpDir := t.TempDir()

	remoteURL := "https://mcp.getcampfire.dev"
	out, err := captureInitRemote(t, tmpDir, "remote-test-passphrase", remoteURL)
	if err != nil {
		t.Fatalf("cf init --remote failed: %v", err)
	}

	// .campfire/center must be written
	centerPath := filepath.Join(tmpDir, "center")
	data, readErr := os.ReadFile(centerPath)
	if readErr != nil {
		t.Fatalf(".campfire/center not found after --remote init: %v", readErr)
	}
	centerID := strings.TrimSpace(string(data))
	if len(centerID) != 64 {
		t.Errorf("center ID should be 64-char hex, got %q", centerID)
	}

	// Output should say [http] not [fs]
	if !strings.Contains(out, "Created center campfire [http]") {
		t.Errorf("expected 'Created center campfire [http]', got:\n%s", out)
	}

	// Store should record http transport type
	s, openErr := store.Open(store.StorePath(tmpDir))
	if openErr != nil {
		t.Fatalf("opening store: %v", openErr)
	}
	defer s.Close()

	m, getErr := s.GetMembership(centerID)
	if getErr != nil {
		t.Fatalf("getting center membership: %v", getErr)
	}
	if m.TransportType != "http" {
		t.Errorf("center transport type = %q, want 'http'", m.TransportType)
	}
}
