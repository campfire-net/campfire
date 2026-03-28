// Package tests contains integration tests for the Campfire swarm lifecycle.
//
// TestSwarmLifecycle_EndToEnd exercises the full swarm workflow:
//
//  1. cf swarm start    — creates root campfire, writes .campfire/root
//  2. cf init --session — creates two distinct agent identities
//  3. auto-join         — Agent A sends without explicit cf join; verifies success
//  4. message exchange  — Agent A sends; Agent B reads it
//  5. sub-campfire      — Agent A creates a sub-campfire; beacon appears in .campfire/beacons/
//  6. cf discover       — finds both root and sub-campfire beacons in the project
//  7. prefix resolution — short prefix of campfire ID resolves in cf read
//  8. cf swarm prompt   — emits template containing root campfire ID
//  9. cf swarm end      — removes .campfire/root
package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cfBinary returns the path to the cf binary used for integration tests.
// It is built once by TestMain (or skips if building fails).
var cfBinary string

func TestMain(m *testing.M) {
	// Build the cf binary into a temp dir accessible to the test.
	tmp, err := os.MkdirTemp("", "cf-integ-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: creating temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	cfBinary = filepath.Join(tmp, "cf")
	cmd := exec.Command("go", "build", "-o", cfBinary, "github.com/campfire-net/campfire/cmd/cf")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: building cf binary: %v\n", err)
		// Build failure is fatal — skip all tests gracefully.
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// cfEnv returns a base environment for test cf invocations.
// cfHome, beaconDir, transportDir must be temp dirs created per-test.
func cfEnv(cfHome, beaconDir, transportDir string) []string {
	env := os.Environ()
	// Override the three directories used by cf.
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "CF_HOME=") ||
			strings.HasPrefix(e, "CF_BEACON_DIR=") ||
			strings.HasPrefix(e, "CF_TRANSPORT_DIR=") {
			continue
		}
		filtered = append(filtered, e)
	}
	filtered = append(filtered,
		"CF_HOME="+cfHome,
		"CF_BEACON_DIR="+beaconDir,
		"CF_TRANSPORT_DIR="+transportDir,
	)
	return filtered
}

// runCF executes the cf binary with the given args, env, and working dir.
// Returns stdout, stderr, and error.
func runCF(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command(cfBinary, args...)
	cmd.Dir = dir
	cmd.Env = env
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// TestSwarmLifecycle_EndToEnd exercises the full swarm lifecycle.
func TestSwarmLifecycle_EndToEnd(t *testing.T) {
	if cfBinary == "" {
		t.Skip("cf binary not built")
	}

	// ---- Setup: two agents (A and B), isolated temp dirs ----

	projectDir := t.TempDir()

	// Shared transport dir so messages are visible across agents.
	transportDir := t.TempDir()

	// Shared beacon dir so cf discover sees beacons published by all agents.
	sharedBeaconDir := t.TempDir()

	// Agent A: init --session before swarm start (swarm start needs an identity).
	cfHomeA := t.TempDir()
	envA := cfEnv(cfHomeA, sharedBeaconDir, transportDir)

	// Agent B directories.
	cfHomeB := t.TempDir()
	envB := cfEnv(cfHomeB, sharedBeaconDir, transportDir)

	// ---- Step 2 (init first): Session identities ----
	t.Log("Step 2: cf init --session for two agents")

	initOutA, _, err := runCF(t, t.TempDir(), envA, "init", "--session")
	if err != nil {
		t.Fatalf("cf init --session (agent A): %v", err)
	}
	linesA := strings.Split(strings.TrimSpace(initOutA), "\n")
	if len(linesA) < 2 {
		t.Fatalf("expected 2 lines from cf init --session, got: %q", initOutA)
	}
	sessionHomeA := strings.TrimSpace(linesA[0])
	displayA := strings.TrimSpace(linesA[1])
	if !strings.HasPrefix(displayA, "agent:") {
		t.Errorf("expected display name like agent:<hex>, got %q", displayA)
	}
	// Switch agent A's CF_HOME to the session home.
	envA = cfEnv(sessionHomeA, sharedBeaconDir, transportDir)

	initOutB, _, err := runCF(t, t.TempDir(), envB, "init", "--session")
	if err != nil {
		t.Fatalf("cf init --session (agent B): %v", err)
	}
	linesB := strings.Split(strings.TrimSpace(initOutB), "\n")
	if len(linesB) < 2 {
		t.Fatalf("expected 2 lines from cf init --session, got: %q", initOutB)
	}
	sessionHomeB := strings.TrimSpace(linesB[0])
	displayB := strings.TrimSpace(linesB[1])
	envB = cfEnv(sessionHomeB, sharedBeaconDir, transportDir)

	// Verify distinct identities.
	pubKeyA := strings.TrimPrefix(displayA, "agent:")
	pubKeyB := strings.TrimPrefix(displayB, "agent:")
	if pubKeyA == pubKeyB {
		t.Errorf("expected distinct agent public keys; both got prefix %q", pubKeyA)
	}

	// ---- Step 1: cf swarm start ----
	t.Log("Step 1: cf swarm start")
	swarmOut, _, err := runCF(t, projectDir, envA, "swarm", "start")
	if err != nil {
		t.Fatalf("cf swarm start: %v", err)
	}
	rootCampfireID := strings.TrimSpace(swarmOut)
	if len(rootCampfireID) != 64 {
		t.Fatalf("expected 64-char campfire ID from swarm start, got %q (len %d)", rootCampfireID, len(rootCampfireID))
	}

	// Verify .campfire/root was written.
	rootFile := filepath.Join(projectDir, ".campfire", "root")
	data, err := os.ReadFile(rootFile)
	if err != nil {
		t.Fatalf(".campfire/root not created: %v", err)
	}
	storedID := strings.TrimSpace(string(data))
	if storedID != rootCampfireID {
		t.Errorf(".campfire/root contains %q, want %q", storedID, rootCampfireID)
	}

	// Verify it is valid hex.
	for _, c := range rootCampfireID {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			t.Errorf("campfire ID %q contains non-hex character %q", rootCampfireID, c)
			break
		}
	}

	// ---- Step 3: Auto-join via send (Agent A sends without explicit join) ----
	t.Log("Step 3: auto-join via cf send (no explicit cf join)")
	sendOut, _, err := runCF(t, projectDir, envA, "send", rootCampfireID, "agent A auto-join test")
	if err != nil {
		t.Fatalf("cf send (auto-join): %v", err)
	}
	// Output should be a message ID (UUID-like).
	msgID := strings.TrimSpace(sendOut)
	if len(msgID) == 0 {
		t.Error("cf send returned empty message ID")
	}

	// ---- Step 4: Agent A sends a status message; Agent B reads it ----
	t.Log("Step 4: message exchange")

	// Agent B must join the root campfire first (it's open protocol).
	_, joinStderr, err := runCF(t, projectDir, envB, "join", rootCampfireID)
	if err != nil {
		t.Fatalf("cf join (agent B): %v\nstderr: %s", err, joinStderr)
	}

	// Agent A sends a tagged status message.
	statusPayload := "agent A status: doing integration test work"
	sendOut2, _, err := runCF(t, projectDir, envA, "send", rootCampfireID, statusPayload, "--tag", "status")
	if err != nil {
		t.Fatalf("cf send (status): %v", err)
	}
	statusMsgID := strings.TrimSpace(sendOut2)
	if len(statusMsgID) == 0 {
		t.Error("cf send (status) returned empty ID")
	}

	// Agent B reads all messages from root campfire.
	readOut, _, err := runCF(t, projectDir, envB, "read", "--all", "--json", rootCampfireID)
	if err != nil {
		t.Fatalf("cf read (agent B): %v", err)
	}

	var messages []map[string]interface{}
	if err := json.Unmarshal([]byte(readOut), &messages); err != nil {
		t.Fatalf("parsing cf read JSON: %v\noutput: %s", err, readOut)
	}

	// Verify Agent B sees Agent A's status message.
	foundStatus := false
	for _, msg := range messages {
		payload, _ := msg["payload"].(string)
		if payload == statusPayload {
			foundStatus = true
			// Verify it has the status tag.
			tags, _ := msg["tags"].([]interface{})
			hasStatusTag := false
			for _, tag := range tags {
				if tag == "status" {
					hasStatusTag = true
					break
				}
			}
			if !hasStatusTag {
				t.Errorf("status message found but missing 'status' tag; tags: %v", tags)
			}
			break
		}
	}
	if !foundStatus {
		t.Errorf("agent B did not find agent A's status message in read output (%d messages)", len(messages))
		for i, m := range messages {
			t.Logf("  msg[%d]: payload=%q", i, m["payload"])
		}
	}

	// ---- Step 5: Agent A creates a sub-campfire ----
	t.Log("Step 5: sub-campfire creation")
	createOut, _, err := runCF(t, projectDir, envA, "create", "--description", "sub-campfire for integration test")
	if err != nil {
		t.Fatalf("cf create (sub-campfire): %v", err)
	}
	subCampfireID := strings.TrimSpace(createOut)
	if len(subCampfireID) != 64 {
		t.Fatalf("expected 64-char sub-campfire ID, got %q (len %d)", subCampfireID, len(subCampfireID))
	}

	// Verify beacon appears in .campfire/beacons/.
	beaconsDir := filepath.Join(projectDir, ".campfire", "beacons")
	entries, err := os.ReadDir(beaconsDir)
	if err != nil {
		t.Fatalf("reading .campfire/beacons/: %v", err)
	}
	foundBeacon := false
	expectedBeaconFile := subCampfireID + ".beacon"
	for _, e := range entries {
		if e.Name() == expectedBeaconFile {
			foundBeacon = true
			break
		}
	}
	if !foundBeacon {
		t.Errorf("expected beacon file %q in .campfire/beacons/; found: %v", expectedBeaconFile, entries)
	}

	// Verify announcement message in root campfire (Agent B reads to pick it up).
	readOut2, _, err := runCF(t, projectDir, envB, "read", "--all", "--json", rootCampfireID)
	if err != nil {
		t.Fatalf("cf read after sub-campfire creation: %v", err)
	}
	var messagesAfterSub []map[string]interface{}
	if err := json.Unmarshal([]byte(readOut2), &messagesAfterSub); err != nil {
		t.Fatalf("parsing cf read JSON after sub-campfire: %v", err)
	}
	subShortID := subCampfireID[:12]
	foundAnnouncement := false
	for _, msg := range messagesAfterSub {
		tags, _ := msg["tags"].([]interface{})
		for _, tag := range tags {
			if tag == "campfire:sub-created" {
				payload, _ := msg["payload"].(string)
				if strings.Contains(payload, subShortID) {
					foundAnnouncement = true
				}
				break
			}
		}
	}
	if !foundAnnouncement {
		t.Errorf("expected campfire:sub-created announcement in root campfire; got %d messages", len(messagesAfterSub))
	}

	// ---- Step 6: cf discover finds both beacons ----
	t.Log("Step 6: cf discover")
	discoverOut, _, err := runCF(t, projectDir, envA, "discover", "--json")
	if err != nil {
		t.Fatalf("cf discover: %v", err)
	}
	var beaconList []map[string]interface{}
	if err := json.Unmarshal([]byte(discoverOut), &beaconList); err != nil {
		t.Fatalf("parsing cf discover JSON: %v\noutput: %s", err, discoverOut)
	}

	foundRoot := false
	foundSub := false
	for _, b := range beaconList {
		id, _ := b["campfire_id"].(string)
		if id == rootCampfireID {
			foundRoot = true
		}
		if id == subCampfireID {
			foundSub = true
		}
	}
	if !foundRoot {
		t.Errorf("cf discover did not find root campfire %s; got %d beacons", rootCampfireID[:12], len(beaconList))
	}
	if !foundSub {
		t.Errorf("cf discover did not find sub-campfire %s; got %d beacons", subCampfireID[:12], len(beaconList))
	}

	// ---- Step 7: Prefix resolution ----
	t.Log("Step 7: prefix resolution")
	shortPrefix := rootCampfireID[:12]
	prefixReadOut, _, err := runCF(t, projectDir, envA, "read", "--all", "--json", shortPrefix)
	if err != nil {
		t.Fatalf("cf read with prefix %q: %v", shortPrefix, err)
	}
	var prefixMessages []map[string]interface{}
	if err := json.Unmarshal([]byte(prefixReadOut), &prefixMessages); err != nil {
		t.Fatalf("parsing cf read (prefix) JSON: %v\noutput: %s", err, prefixReadOut)
	}
	// Should have found messages in the root campfire.
	if len(prefixMessages) == 0 {
		t.Error("prefix resolution: expected messages in root campfire, got 0")
	}

	// ---- Step 8: cf swarm prompt ----
	t.Log("Step 8: cf swarm prompt")
	promptOut, _, err := runCF(t, projectDir, envA, "swarm", "prompt")
	if err != nil {
		t.Fatalf("cf swarm prompt: %v", err)
	}
	if !strings.Contains(promptOut, rootCampfireID) {
		t.Errorf("cf swarm prompt does not contain root campfire ID %q", rootCampfireID)
	}
	if !strings.Contains(promptOut, "Campfire Coordination") {
		t.Errorf("cf swarm prompt missing 'Campfire Coordination' header")
	}

	// ---- Step 9: cf swarm end ----
	t.Log("Step 9: cf swarm end")
	endOut, _, err := runCF(t, projectDir, envA, "swarm", "end")
	if err != nil {
		t.Fatalf("cf swarm end: %v", err)
	}
	_ = endOut // "swarm ended"

	// Verify .campfire/root is removed.
	if _, err := os.Stat(rootFile); err == nil {
		t.Error(".campfire/root still exists after cf swarm end")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking .campfire/root: %v", err)
	}
}
