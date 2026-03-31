package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureInitSession runs cf init --session and captures stdout output.
func captureInitSession(t *testing.T) (stdout string, err error) {
	t.Helper()

	// Redirect os.Stdout to a pipe to capture fmt.Println output
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	origStdout := os.Stdout
	os.Stdout = w

	// Reset flag state
	initCmd.Flags().Set("force", "false")   //nolint:errcheck
	initCmd.Flags().Set("name", "")         //nolint:errcheck
	initCmd.Flags().Set("session", "false") //nolint:errcheck
	rootCmd.SetArgs([]string{"init", "--session"})
	runErr := rootCmd.Execute()

	// Restore stdout and read captured output
	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	r.Close()

	return string(buf[:n]), runErr
}

// captureInitDefault runs cf init (no flags) with a custom CF_HOME.
func captureInitDefault(t *testing.T, cfHomeDir string) (stdout string, err error) {
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
	t.Setenv("CF_HOME", cfHomeDir)
	rootCmd.SetArgs([]string{"init"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	r.Close()

	return string(buf[:n]), runErr
}

// TestInitSession_CreatesTempDir verifies that --session creates an identity
// in a temp dir (not ~/.campfire/) and prints the path on line 1.
func TestInitSession_CreatesTempDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	campfireHome := filepath.Join(home, ".campfire")

	out, err := captureInitSession(t)
	if err != nil {
		t.Fatalf("cf init --session failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines of output, got: %q", out)
	}

	cfHomeLine := strings.TrimSpace(lines[0])
	displayName := strings.TrimSpace(lines[1])

	// Must be a real directory
	info, statErr := os.Stat(cfHomeLine)
	if statErr != nil {
		t.Fatalf("CF_HOME path %q does not exist: %v", cfHomeLine, statErr)
	}
	if !info.IsDir() {
		t.Fatalf("CF_HOME path %q is not a directory", cfHomeLine)
	}

	// Must NOT be inside ~/.campfire/
	if strings.HasPrefix(cfHomeLine, campfireHome) {
		t.Errorf("session identity should not be in ~/.campfire/, got %q", cfHomeLine)
	}

	// Display name must start with "agent:"
	if !strings.HasPrefix(displayName, "agent:") {
		t.Errorf("display name should start with 'agent:', got %q", displayName)
	}

	// Display name hex portion should be 6 chars
	hex := strings.TrimPrefix(displayName, "agent:")
	if len(hex) != 6 {
		t.Errorf("display name hex portion should be 6 chars, got %q (len %d)", hex, len(hex))
	}

	// Identity file must exist in the printed path
	identityFile := filepath.Join(cfHomeLine, "identity.json")
	if _, statErr := os.Stat(identityFile); statErr != nil {
		t.Errorf("identity.json not found at %q: %v", identityFile, statErr)
	}
}

// TestInitSession_TwoCallsProduceDifferentIdentities verifies uniqueness.
func TestInitSession_TwoCallsProduceDifferentIdentities(t *testing.T) {
	out1, err := captureInitSession(t)
	if err != nil {
		t.Fatalf("first cf init --session failed: %v", err)
	}
	out2, err := captureInitSession(t)
	if err != nil {
		t.Fatalf("second cf init --session failed: %v", err)
	}

	lines1 := strings.Split(strings.TrimSpace(out1), "\n")
	lines2 := strings.Split(strings.TrimSpace(out2), "\n")

	if len(lines1) < 2 || len(lines2) < 2 {
		t.Fatalf("expected 2 lines each, got %q and %q", out1, out2)
	}

	// Paths must differ
	path1 := strings.TrimSpace(lines1[0])
	path2 := strings.TrimSpace(lines2[0])
	if path1 == path2 {
		t.Errorf("two --session calls produced the same path: %q", path1)
	}

	// Both produce valid display names
	for _, line := range []string{lines1[1], lines2[1]} {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "agent:") {
			t.Errorf("display name missing 'agent:' prefix: %q", line)
		}
	}
}

// TestInitSession_InheritsJoinPolicyAndOperatorRoot verifies that --session copies
// join-policy.json and operator-root.json from the parent CF_HOME, does not copy
// aliases.json, and silently skips missing files.
func TestInitSession_InheritsJoinPolicyAndOperatorRoot(t *testing.T) {
	parentHome := t.TempDir()
	t.Setenv("CF_HOME", parentHome)

	// Write policy files into parent CF_HOME
	joinPolicy := []byte(`{"policy":"test"}`)
	operatorRoot := []byte(`{"root":"test"}`)
	if err := os.WriteFile(filepath.Join(parentHome, "join-policy.json"), joinPolicy, 0600); err != nil {
		t.Fatalf("writing join-policy.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentHome, "operator-root.json"), operatorRoot, 0600); err != nil {
		t.Fatalf("writing operator-root.json: %v", err)
	}

	out, err := captureInitSession(t)
	if err != nil {
		t.Fatalf("cf init --session failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 1 {
		t.Fatalf("expected at least 1 line of output, got: %q", out)
	}
	sessionDir := strings.TrimSpace(lines[0])

	// join-policy.json must be present and match
	got, readErr := os.ReadFile(filepath.Join(sessionDir, "join-policy.json"))
	if readErr != nil {
		t.Errorf("join-policy.json not found in session dir: %v", readErr)
	} else if string(got) != string(joinPolicy) {
		t.Errorf("join-policy.json content mismatch: got %q want %q", got, joinPolicy)
	}

	// operator-root.json must be present and match
	got, readErr = os.ReadFile(filepath.Join(sessionDir, "operator-root.json"))
	if readErr != nil {
		t.Errorf("operator-root.json not found in session dir: %v", readErr)
	} else if string(got) != string(operatorRoot) {
		t.Errorf("operator-root.json content mismatch: got %q want %q", got, operatorRoot)
	}

	// aliases.json must NOT be present
	if _, statErr := os.Stat(filepath.Join(sessionDir, "aliases.json")); statErr == nil {
		t.Errorf("aliases.json should not be copied to session dir")
	}
}

// TestInitSession_MissingParentFilesSkipped verifies that --session succeeds even
// when the parent CF_HOME has no join-policy.json or operator-root.json.
func TestInitSession_MissingParentFilesSkipped(t *testing.T) {
	parentHome := t.TempDir()
	t.Setenv("CF_HOME", parentHome)
	// No policy files in parentHome

	out, err := captureInitSession(t)
	if err != nil {
		t.Fatalf("cf init --session failed with missing parent files: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 1 {
		t.Fatalf("expected at least 1 line of output, got: %q", out)
	}
	sessionDir := strings.TrimSpace(lines[0])

	// Neither file should exist — silently skipped
	for _, fname := range []string{"join-policy.json", "operator-root.json"} {
		if _, statErr := os.Stat(filepath.Join(sessionDir, fname)); statErr == nil {
			t.Errorf("%s should not exist when parent has none", fname)
		}
	}
}

// TestInitPersistent_UnchangedBehavior verifies that cf init (no flags) still
// writes identity.json to the configured CF_HOME.
func TestInitPersistent_UnchangedBehavior(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := captureInitDefault(t, tmpDir)
	if err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	// Identity must exist in the CF_HOME we set
	identityFile := filepath.Join(tmpDir, "identity.json")
	if _, statErr := os.Stat(identityFile); statErr != nil {
		t.Errorf("identity.json not found at %q: %v", identityFile, statErr)
	}
}


// TestInheritAgentConfig_CopiesAllFiles verifies that inheritAgentConfig copies
// join-policy.json, operator-root.json, and aliases.json from parent to agent home,
// and writes meta.json with the correct fields.
func TestInheritAgentConfig_CopiesAllFiles(t *testing.T) {
	parentDir := t.TempDir()
	agentHome := t.TempDir()

	// Write all three config files to parent
	files := map[string]string{
		"join-policy.json":    `{"join_policy":"consult","consult_campfire":"abc123","join_root":""}`,
		"operator-root.json":  `{"name":"testop","campfire_id":"deadbeef0123456789"}`,
		"aliases.json":        `{"home":"cafebabe0123456789","other":"feed0001"}`,
	}
	for fname, content := range files {
		if err := os.WriteFile(filepath.Join(parentDir, fname), []byte(content), 0600); err != nil {
			t.Fatalf("writing %s: %v", fname, err)
		}
	}

	if err := inheritAgentConfig(parentDir, agentHome, "test-worker"); err != nil {
		t.Fatalf("inheritAgentConfig: %v", err)
	}

	// Verify all three files were copied with correct content
	for fname, want := range files {
		got, err := os.ReadFile(filepath.Join(agentHome, fname))
		if err != nil {
			t.Errorf("expected %s to be copied: %v", fname, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s content mismatch: got %q, want %q", fname, got, want)
		}
	}

	// Verify meta.json
	metaData, err := os.ReadFile(filepath.Join(agentHome, "meta.json"))
	if err != nil {
		t.Fatalf("meta.json not written: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("meta.json parse error: %v", err)
	}
	if meta["name"] != "test-worker" {
		t.Errorf("meta.json name = %v, want test-worker", meta["name"])
	}
	if meta["parent_cf_home"] != parentDir {
		t.Errorf("meta.json parent_cf_home = %v, want %v", meta["parent_cf_home"], parentDir)
	}
	if _, ok := meta["created_at"]; !ok {
		t.Error("meta.json missing created_at field")
	}
}

// TestInheritAgentConfig_MissingParentFilesSkipped verifies that missing source
// files are silently skipped and meta.json is still written.
func TestInheritAgentConfig_MissingParentFilesSkipped(t *testing.T) {
	parentDir := t.TempDir() // empty — no config files
	agentHome := t.TempDir()

	if err := inheritAgentConfig(parentDir, agentHome, "sparse-worker"); err != nil {
		t.Fatalf("inheritAgentConfig with empty parent: %v", err)
	}

	// None of the config files should be present
	for _, fname := range []string{"join-policy.json", "operator-root.json", "aliases.json"} {
		if _, err := os.Stat(filepath.Join(agentHome, fname)); err == nil {
			t.Errorf("did not expect %s to be copied from empty parent", fname)
		}
	}

	// meta.json must be written regardless
	if _, err := os.Stat(filepath.Join(agentHome, "meta.json")); err != nil {
		t.Errorf("meta.json should exist even with empty parent: %v", err)
	}
}

// TestInheritAgentConfig_PartialParentFiles verifies that only existing files are copied.
func TestInheritAgentConfig_PartialParentFiles(t *testing.T) {
	parentDir := t.TempDir()
	agentHome := t.TempDir()

	// Only write join-policy.json
	if err := os.WriteFile(filepath.Join(parentDir, "join-policy.json"), []byte(`{"join_policy":"consult","consult_campfire":"abc","join_root":""}`), 0600); err != nil {
		t.Fatalf("writing join-policy.json: %v", err)
	}

	if err := inheritAgentConfig(parentDir, agentHome, "partial-worker"); err != nil {
		t.Fatalf("inheritAgentConfig with partial parent: %v", err)
	}

	// join-policy.json must be present
	if _, err := os.Stat(filepath.Join(agentHome, "join-policy.json")); err != nil {
		t.Errorf("join-policy.json should be copied: %v", err)
	}
	// operator-root.json and aliases.json must not be present
	for _, fname := range []string{"operator-root.json", "aliases.json"} {
		if _, err := os.Stat(filepath.Join(agentHome, fname)); err == nil {
			t.Errorf("%s should not be copied when absent from parent", fname)
		}
	}
}

// TestInitNamed_WithFromFlag verifies the end-to-end path: cf init --name <agent> --from <parent>
// creates an agent with inherited config. We call inheritAgentConfig directly to avoid
// cobra global-state issues with named agent paths, but verify the full function contract.
func TestInitNamed_WithFromFlag_InheritsMeta(t *testing.T) {
	parentDir := t.TempDir()
	agentHome := t.TempDir()

	if err := os.WriteFile(filepath.Join(parentDir, "join-policy.json"), []byte(`{"join_policy":"consult","consult_campfire":"aaa","join_root":""}`), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := inheritAgentConfig(parentDir, agentHome, "worker-1"); err != nil {
		t.Fatalf("inheritAgentConfig: %v", err)
	}

	// meta.json parent_cf_home must point to the --from path
	metaData, err := os.ReadFile(filepath.Join(agentHome, "meta.json"))
	if err != nil {
		t.Fatalf("meta.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}
	if meta["parent_cf_home"] != parentDir {
		t.Errorf("meta parent_cf_home = %v, want %v", meta["parent_cf_home"], parentDir)
	}
	if meta["name"] != "worker-1" {
		t.Errorf("meta name = %v, want worker-1", meta["name"])
	}
}
