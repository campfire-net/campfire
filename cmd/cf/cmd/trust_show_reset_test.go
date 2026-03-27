package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/trust"
)

// setupTrustTestEnv creates a temp CF_HOME with an initialized identity.
// Returns the cfHomeDir for use in tests.
func setupTrustTestEnv(t *testing.T) string {
	t.Helper()

	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", filepath.Join(cfHomeDir, "beacons"))

	// Reset init flags to defaults.
	initCmd.Flags().Set("force", "false")   //nolint:errcheck
	initCmd.Flags().Set("name", "")         //nolint:errcheck
	initCmd.Flags().Set("session", "false") //nolint:errcheck

	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	return cfHomeDir
}

// runTrustShow executes `cf trust show` and returns captured stdout.
func runTrustShow(t *testing.T, extraArgs ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = false
	args := append([]string{"trust", "show"}, extraArgs...)
	rootCmd.SetArgs(args)
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	return buf.String(), runErr
}

// runTrustReset executes `cf trust reset` and returns captured stdout.
func runTrustReset(t *testing.T, extraArgs ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	origStdin := os.Stdin

	// Provide "y" on stdin for --all confirmation if not using --yes.
	stdinR, stdinW, _ := os.Pipe()
	stdinW.WriteString("y\n")
	stdinW.Close()
	os.Stdin = stdinR

	// Reset flags to defaults between tests (cobra flags persist between Execute calls).
	trustResetCmd.Flags().Set("campfire", "")   //nolint:errcheck
	trustResetCmd.Flags().Set("convention", "") //nolint:errcheck
	trustResetCmd.Flags().Set("all", "false")   //nolint:errcheck
	trustResetCmd.Flags().Set("yes", "false")   //nolint:errcheck

	jsonOutput = false
	args := append([]string{"trust", "reset"}, extraArgs...)
	rootCmd.SetArgs(args)
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout
	os.Stdin = origStdin
	stdinR.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	return buf.String(), runErr
}

// TestTrustShow_FreshInit verifies that `cf trust show` succeeds on a fresh identity.
// With no home campfire configured, adopted conventions should be empty but the
// command must not error.
func TestTrustShow_FreshInit(t *testing.T) {
	setupTrustTestEnv(t)

	out, err := runTrustShow(t)
	if err != nil {
		t.Fatalf("cf trust show failed: %v", err)
	}

	// Output should mention "Trust policy" header.
	if !strings.Contains(out, "Trust policy") {
		t.Errorf("expected Trust policy header in output, got:\n%s", out)
	}

	// With no home campfire, adopted is empty.
	if !strings.Contains(out, "none") {
		t.Errorf("expected 'none' for empty adopted list, got:\n%s", out)
	}
}

// TestTrustShow_JSONOutput verifies that `cf trust show --json` outputs valid JSON
// with the expected top-level keys.
func TestTrustShow_JSONOutput(t *testing.T) {
	setupTrustTestEnv(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = true
	rootCmd.SetArgs([]string{"trust", "show", "--json"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout
	jsonOutput = false

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	if runErr != nil {
		t.Fatalf("cf trust show --json failed: %v", runErr)
	}

	var out map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON output is invalid: %v\nOutput: %s", err, buf.String())
	}

	if _, ok := out["initialized"]; !ok {
		t.Error("JSON output missing 'initialized' key")
	}
	if _, ok := out["adopted"]; !ok {
		t.Error("JSON output missing 'adopted' key")
	}
	if _, ok := out["pins"]; !ok {
		t.Error("JSON output missing 'pins' key")
	}
}

// TestTrustReset_RequiresScope verifies that `cf trust reset` with no scope flags errors.
func TestTrustReset_RequiresScope(t *testing.T) {
	setupTrustTestEnv(t)

	_, err := runTrustReset(t)
	if err == nil {
		t.Error("expected error when no scope flag given, got nil")
	}
}

// TestTrustReset_MutuallyExclusiveFlags verifies that specifying both --campfire and
// --convention produces an error.
func TestTrustReset_MutuallyExclusiveFlags(t *testing.T) {
	setupTrustTestEnv(t)

	_, err := runTrustReset(t, "--campfire", "abc123", "--convention", "trust")
	if err == nil {
		t.Error("expected error when both --campfire and --convention given, got nil")
	}
}

// TestTrustReset_AllWithYesFlag verifies that `cf trust reset --all --yes` clears pins.
func TestTrustReset_AllWithYesFlag(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	// Seed some pins into the store.
	agentID, err := loadIdentity()
	if err != nil {
		t.Fatalf("loading identity: %v", err)
	}
	pinsPath := filepath.Join(cfHomeDir, "pins.json")
	ps, err := trust.NewPinStore(pinsPath, agentID.PrivateKey)
	if err != nil {
		t.Fatalf("creating pin store: %v", err)
	}
	ps.SetPin("abc123def456abc123def456abc123def456abc123def456abc123def456abc1", "trust", "verify",
		&trust.Pin{
			ContentHash: "sha256:aabbcc",
			SignerKey:   "pubkey",
			SignerType:  trust.SignerMemberKey,
			TrustStatus: trust.TrustAdopted,
		})
	if err := ps.Save(); err != nil {
		t.Fatalf("saving pin store: %v", err)
	}

	out, err := runTrustReset(t, "--all", "--yes")
	if err != nil {
		t.Fatalf("cf trust reset --all --yes failed: %v", err)
	}

	if !strings.Contains(out, "Cleared all pins") {
		t.Errorf("expected 'Cleared all pins' in output, got:\n%s", out)
	}
}

// TestTrustReset_ConventionScope verifies that `cf trust reset --convention <slug>`
// clears only pins for that convention, leaving others intact.
func TestTrustReset_ConventionScope(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	agentID, err := loadIdentity()
	if err != nil {
		t.Fatalf("loading identity: %v", err)
	}
	pinsPath := filepath.Join(cfHomeDir, "pins.json")
	ps, err := trust.NewPinStore(pinsPath, agentID.PrivateKey)
	if err != nil {
		t.Fatalf("creating pin store: %v", err)
	}

	// Add pins for two different conventions.
	campfire := "abc123def456abc123def456abc123def456abc123def456abc123def456abc1"
	ps.SetPin(campfire, "trust", "verify", &trust.Pin{
		ContentHash: "sha256:aa",
		SignerKey:   "k",
		SignerType:  trust.SignerMemberKey,
		TrustStatus: trust.TrustAdopted,
	})
	ps.SetPin(campfire, "social", "post", &trust.Pin{
		ContentHash: "sha256:bb",
		SignerKey:   "k",
		SignerType:  trust.SignerMemberKey,
		TrustStatus: trust.TrustAdopted,
	})
	if err := ps.Save(); err != nil {
		t.Fatalf("saving pin store: %v", err)
	}

	out, err := runTrustReset(t, "--convention", "trust")
	if err != nil {
		t.Fatalf("cf trust reset --convention trust failed: %v", err)
	}

	// Output should mention the convention and how many were cleared.
	if !strings.Contains(out, "trust") {
		t.Errorf("expected convention name in output, got:\n%s", out)
	}
	if !strings.Contains(out, "1 removed") {
		t.Errorf("expected '1 removed' in output, got:\n%s", out)
	}

	// Verify that the social:post pin was NOT cleared.
	ps2, err := trust.NewPinStore(pinsPath, agentID.PrivateKey)
	if err != nil {
		t.Fatalf("re-loading pin store: %v", err)
	}
	remaining := ps2.ListPins()
	if len(remaining) != 1 {
		t.Errorf("expected 1 pin remaining after convention reset, got %d", len(remaining))
	}
}
