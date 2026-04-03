package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/naming"
)

// validCampfireID is a 64-character lowercase hex string used as a valid campfire ID in tests.
const (
	validConsultID  = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	validJoinRootID = "1122334455667788990011223344556677889900112233445566778899001122"
)

// setupJoinPolicyTestEnv creates a temp CF_HOME directory, sets the CF_HOME env var,
// and returns the directory path. No identity init is required — join-policy commands
// only read/write the join-policy.json file.
func setupJoinPolicyTestEnv(t *testing.T) string {
	t.Helper()
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	// Reset the cfHome package-level variable so CFHome() re-reads CF_HOME.
	cfHome = ""
	t.Cleanup(func() { cfHome = "" })
	return cfHomeDir
}

// runJoinPolicyCmd executes the given join-policy subcommand args via rootCmd and captures stdout.
// It resets relevant cobra flags before each run to prevent state leakage between tests.
func runJoinPolicyCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()

	// Reset set subcommand flags to defaults before each run.
	joinPolicySetCmd.Flags().Set("consult", "")     //nolint:errcheck
	joinPolicySetCmd.Flags().Set("fs-walk", "false") //nolint:errcheck
	joinPolicySetCmd.Flags().Set("join-root", "")   //nolint:errcheck

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	fullArgs := append([]string{"join-policy"}, args...)
	rootCmd.SetArgs(fullArgs)
	err := rootCmd.Execute()

	// Restore defaults.
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)

	return buf.String(), err
}

// TestJoinPolicyShow_NoPolicy verifies that `cf join-policy show` when no policy is
// configured prints a human-readable message indicating no policy is set.
func TestJoinPolicyShow_NoPolicy(t *testing.T) {
	setupJoinPolicyTestEnv(t)

	out, err := runJoinPolicyCmd(t, "show")
	if err != nil {
		t.Fatalf("cf join-policy show failed: %v", err)
	}
	if !strings.Contains(out, "No join policy configured") {
		t.Errorf("expected 'No join policy configured' in output, got:\n%s", out)
	}
}

// TestJoinPolicyShow_WithPolicy verifies that `cf join-policy show` displays the
// configured join policy fields when a policy file exists.
func TestJoinPolicyShow_WithPolicy(t *testing.T) {
	cfHomeDir := setupJoinPolicyTestEnv(t)

	// Pre-write a policy file directly so the test doesn't depend on set.
	jp := &naming.JoinPolicy{
		Policy:          "consult",
		ConsultCampfire: validConsultID,
		JoinRoot:        validJoinRootID,
	}
	if err := naming.SaveJoinPolicy(cfHomeDir, jp); err != nil {
		t.Fatalf("SaveJoinPolicy: %v", err)
	}

	out, err := runJoinPolicyCmd(t, "show")
	if err != nil {
		t.Fatalf("cf join-policy show failed: %v", err)
	}
	if !strings.Contains(out, "join policy:") {
		t.Errorf("expected 'join policy:' header in output, got:\n%s", out)
	}
	if !strings.Contains(out, validConsultID) {
		t.Errorf("expected consult campfire ID %q in output, got:\n%s", validConsultID, out)
	}
	if !strings.Contains(out, validJoinRootID) {
		t.Errorf("expected join root ID %q in output, got:\n%s", validJoinRootID, out)
	}
}

// TestJoinPolicySet_ConsultMode verifies that `cf join-policy set --consult <id> --join-root <id>`
// writes the policy to disk and prints a success message.
func TestJoinPolicySet_ConsultMode(t *testing.T) {
	cfHomeDir := setupJoinPolicyTestEnv(t)

	out, err := runJoinPolicyCmd(t, "set", "--consult", validConsultID, "--join-root", validJoinRootID)
	if err != nil {
		t.Fatalf("cf join-policy set --consult failed: %v", err)
	}
	if !strings.Contains(out, "join policy saved") {
		t.Errorf("expected 'join policy saved' in output, got:\n%s", out)
	}
	if !strings.Contains(out, validConsultID) {
		t.Errorf("expected consult ID in output, got:\n%s", out)
	}

	// Verify the file was actually written.
	loaded, err := naming.LoadJoinPolicy(cfHomeDir)
	if err != nil {
		t.Fatalf("LoadJoinPolicy: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadJoinPolicy returned nil after set")
	}
	if loaded.ConsultCampfire != validConsultID {
		t.Errorf("ConsultCampfire = %q, want %q", loaded.ConsultCampfire, validConsultID)
	}
	if loaded.JoinRoot != validJoinRootID {
		t.Errorf("JoinRoot = %q, want %q", loaded.JoinRoot, validJoinRootID)
	}
}

// TestJoinPolicySet_FSWalkMode verifies that `cf join-policy set --fs-walk --join-root <id>`
// writes the policy with the fs-walk sentinel and prints the expected output.
func TestJoinPolicySet_FSWalkMode(t *testing.T) {
	cfHomeDir := setupJoinPolicyTestEnv(t)

	out, err := runJoinPolicyCmd(t, "set", "--fs-walk", "--join-root", validJoinRootID)
	if err != nil {
		t.Fatalf("cf join-policy set --fs-walk failed: %v", err)
	}
	if !strings.Contains(out, "join policy saved") {
		t.Errorf("expected 'join policy saved' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "fs-walk") {
		t.Errorf("expected 'fs-walk' in output, got:\n%s", out)
	}

	// Verify the file was written with the FSWalkSentinel.
	loaded, err := naming.LoadJoinPolicy(cfHomeDir)
	if err != nil {
		t.Fatalf("LoadJoinPolicy: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadJoinPolicy returned nil after set")
	}
	if loaded.ConsultCampfire != naming.FSWalkSentinel {
		t.Errorf("ConsultCampfire = %q, want %q", loaded.ConsultCampfire, naming.FSWalkSentinel)
	}
	if loaded.JoinRoot != validJoinRootID {
		t.Errorf("JoinRoot = %q, want %q", loaded.JoinRoot, validJoinRootID)
	}
}

// TestJoinPolicySet_MutualExclusion verifies that specifying both --consult and --fs-walk
// returns an error.
func TestJoinPolicySet_MutualExclusion(t *testing.T) {
	setupJoinPolicyTestEnv(t)

	_, err := runJoinPolicyCmd(t, "set", "--consult", validConsultID, "--fs-walk", "--join-root", validJoinRootID)
	if err == nil {
		t.Error("expected error when both --consult and --fs-walk given, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' in error, got: %v", err)
	}
}

// TestJoinPolicySet_RequiresModeFlag verifies that omitting both --consult and --fs-walk
// returns an error.
func TestJoinPolicySet_RequiresModeFlag(t *testing.T) {
	setupJoinPolicyTestEnv(t)

	_, err := runJoinPolicyCmd(t, "set", "--join-root", validJoinRootID)
	if err == nil {
		t.Error("expected error when neither --consult nor --fs-walk given, got nil")
	}
}

// TestJoinPolicySet_RequiresJoinRoot verifies that omitting --join-root returns an error.
func TestJoinPolicySet_RequiresJoinRoot(t *testing.T) {
	setupJoinPolicyTestEnv(t)

	_, err := runJoinPolicyCmd(t, "set", "--fs-walk")
	if err == nil {
		t.Error("expected error when --join-root is omitted, got nil")
	}
	if !strings.Contains(err.Error(), "--join-root") {
		t.Errorf("expected '--join-root' in error, got: %v", err)
	}
}

// TestJoinPolicySet_RoundTrip verifies that set followed by show displays the saved policy.
func TestJoinPolicySet_RoundTrip(t *testing.T) {
	setupJoinPolicyTestEnv(t)

	// Set the policy.
	_, err := runJoinPolicyCmd(t, "set", "--consult", validConsultID, "--join-root", validJoinRootID)
	if err != nil {
		t.Fatalf("cf join-policy set failed: %v", err)
	}

	// Show the policy.
	out, err := runJoinPolicyCmd(t, "show")
	if err != nil {
		t.Fatalf("cf join-policy show failed: %v", err)
	}

	if !strings.Contains(out, validConsultID) {
		t.Errorf("show after set: expected consult ID %q in output, got:\n%s", validConsultID, out)
	}
	if !strings.Contains(out, validJoinRootID) {
		t.Errorf("show after set: expected join root ID %q in output, got:\n%s", validJoinRootID, out)
	}
}

// TestJoinPolicySet_PolicyFileLocation verifies that the policy file is written to
// the CF_HOME directory, not some other location.
func TestJoinPolicySet_PolicyFileLocation(t *testing.T) {
	cfHomeDir := setupJoinPolicyTestEnv(t)

	_, err := runJoinPolicyCmd(t, "set", "--fs-walk", "--join-root", validJoinRootID)
	if err != nil {
		t.Fatalf("cf join-policy set failed: %v", err)
	}

	policyPath := filepath.Join(cfHomeDir, "join-policy.json")
	if _, err := os.Stat(policyPath); os.IsNotExist(err) {
		t.Errorf("join-policy.json not found at %s", policyPath)
	}
}
