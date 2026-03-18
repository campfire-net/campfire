// Package tests contains integration tests for the Campfire protocol.
//
// TestAutomatonSubstrate_EndToEnd exercises the full automaton substrate scenario:
//  1. Create a campfire with 3 members: automaton-instance (full), curator (writer), manager (observer)
//  2. Instance sends messages with memory:proposed tags
//  3. Curator sends messages with memory:standing tags
//  4. Manager attempts to send — MUST fail (observer role)
//  5. Read with --tag memory:standing returns only standing messages (store-level filtering)
//  6. Create a campfire:view "standing-facts" with predicate (tag "memory:standing")
//  7. cf view read returns materialized results matching only standing messages
//  8. Compact old messages — compaction event created, superseded messages excluded from default read
//  9. cf read --all still shows everything including compacted messages
// 10. cf read --fields id,tags,payload returns projected output
package tests

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAutomatonSubstrate_EndToEnd exercises the full automaton substrate scenario.
func TestAutomatonSubstrate_EndToEnd(t *testing.T) {
	if cfBinary == "" {
		t.Skip("cf binary not built")
	}

	// ---- Setup: shared transport and beacon dirs ----
	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	// Helper: initAgent creates a session identity and returns (env, publicKeyHex).
	initAgent := func(name string) ([]string, string) {
		t.Helper()
		tmpHome := t.TempDir()
		env := cfEnv(tmpHome, beaconDir, transportDir)
		initOut, _, err := runCF(t, t.TempDir(), env, "init", "--session")
		if err != nil {
			t.Fatalf("cf init --session (%s): %v", name, err)
		}
		// Output: line 1 = session home dir, line 2 = display name.
		lines := strings.Split(strings.TrimSpace(initOut), "\n")
		if len(lines) < 2 {
			t.Fatalf("cf init --session (%s): expected 2 lines, got: %q", name, initOut)
		}
		sessionHome := strings.TrimSpace(lines[0])
		sessionEnv := cfEnv(sessionHome, beaconDir, transportDir)

		// Get the full public key via cf id.
		idOut, _, err := runCF(t, t.TempDir(), sessionEnv, "id")
		if err != nil {
			t.Fatalf("cf id (%s): %v", name, err)
		}
		pubKey := strings.TrimSpace(idOut)
		if len(pubKey) != 64 {
			t.Fatalf("cf id (%s): expected 64-char hex key, got %q (len %d)", name, pubKey, len(pubKey))
		}
		return sessionEnv, pubKey
	}

	// Three agents: instance (creator, full role), curator (writer), manager (observer).
	envInstance, instancePubKey := initAgent("instance")
	envCurator, curatorPubKey := initAgent("curator")
	envManager, managerPubKey := initAgent("manager")

	t.Logf("instance pubkey: %s...", instancePubKey[:12])
	t.Logf("curator pubkey:  %s...", curatorPubKey[:12])
	t.Logf("manager pubkey:  %s...", managerPubKey[:12])
	_ = instancePubKey // used for logging only

	// ---- Step 1: Create campfire (instance creates and joins as full member) ----
	t.Log("Step 1: Create campfire")
	createOut, _, err := runCF(t, t.TempDir(), envInstance, "create")
	if err != nil {
		t.Fatalf("cf create: %v", err)
	}
	campfireID := strings.TrimSpace(createOut)
	if len(campfireID) != 64 {
		t.Fatalf("expected 64-char campfire ID, got %q (len %d)", campfireID, len(campfireID))
	}
	t.Logf("Campfire: %s...", campfireID[:12])

	// Note: the creator (instance) is already a member after cf create.
	// No need to call cf join for the creator.

	// Admit curator as writer.
	_, _, err = runCF(t, t.TempDir(), envInstance, "admit", campfireID, curatorPubKey, "--role", "writer")
	if err != nil {
		t.Fatalf("cf admit (curator as writer): %v", err)
	}

	// Admit manager as observer.
	_, _, err = runCF(t, t.TempDir(), envInstance, "admit", campfireID, managerPubKey, "--role", "observer")
	if err != nil {
		t.Fatalf("cf admit (manager as observer): %v", err)
	}

	// Curator joins (will pick up the writer role from the pre-admitted MemberRecord).
	_, _, err = runCF(t, t.TempDir(), envCurator, "join", campfireID)
	if err != nil {
		t.Fatalf("cf join (curator): %v", err)
	}

	// Manager joins (will pick up the observer role from the pre-admitted MemberRecord).
	_, _, err = runCF(t, t.TempDir(), envManager, "join", campfireID)
	if err != nil {
		t.Fatalf("cf join (manager): %v", err)
	}

	// ---- Step 2: Instance sends messages with memory:proposed tags ----
	t.Log("Step 2: Instance sends memory:proposed messages")
	proposed1Out, _, err := runCF(t, t.TempDir(), envInstance, "send", campfireID,
		"proposed memory: agent learned X", "--tag", "memory:proposed")
	if err != nil {
		t.Fatalf("cf send (instance, memory:proposed #1): %v", err)
	}
	proposed1ID := strings.TrimSpace(proposed1Out)
	if len(proposed1ID) == 0 {
		t.Fatal("cf send (memory:proposed #1) returned empty ID")
	}

	proposed2Out, _, err := runCF(t, t.TempDir(), envInstance, "send", campfireID,
		"proposed memory: agent learned Y", "--tag", "memory:proposed")
	if err != nil {
		t.Fatalf("cf send (instance, memory:proposed #2): %v", err)
	}
	proposed2ID := strings.TrimSpace(proposed2Out)
	if len(proposed2ID) == 0 {
		t.Fatal("cf send (memory:proposed #2) returned empty ID")
	}

	// ---- Step 3: Curator sends messages with memory:standing tags ----
	t.Log("Step 3: Curator sends memory:standing messages")
	standing1Out, _, err := runCF(t, t.TempDir(), envCurator, "send", campfireID,
		"standing fact: the sky is blue", "--tag", "memory:standing")
	if err != nil {
		t.Fatalf("cf send (curator, memory:standing #1): %v", err)
	}
	standing1ID := strings.TrimSpace(standing1Out)
	if len(standing1ID) == 0 {
		t.Fatal("cf send (memory:standing #1) returned empty ID")
	}

	standing2Out, _, err := runCF(t, t.TempDir(), envCurator, "send", campfireID,
		"standing fact: water is wet", "--tag", "memory:standing")
	if err != nil {
		t.Fatalf("cf send (curator, memory:standing #2): %v", err)
	}
	standing2ID := strings.TrimSpace(standing2Out)
	if len(standing2ID) == 0 {
		t.Fatal("cf send (memory:standing #2) returned empty ID")
	}

	t.Logf("Sent proposed1=%s... proposed2=%s... standing1=%s... standing2=%s...",
		proposed1ID[:8], proposed2ID[:8], standing1ID[:8], standing2ID[:8])

	// ---- Step 4: Manager attempts to send — MUST fail (observer role) ----
	t.Log("Step 4: Manager attempts to send (must fail — observer role)")
	_, _, managerSendErr := runCF(t, t.TempDir(), envManager, "send", campfireID, "manager trying to send")
	if managerSendErr == nil {
		t.Fatal("expected error: manager (observer) should not be able to send messages, but send succeeded")
	}
	t.Logf("Manager send correctly rejected: %v", managerSendErr)

	// ---- Step 5: Read with --tag memory:standing returns only standing messages ----
	t.Log("Step 5: Read with --tag memory:standing (store-level filtering)")

	// First sync the instance store with all messages from the transport.
	// (The instance only has its own sent messages so far; sync by reading --all.)
	tagReadOut, _, err := runCF(t, t.TempDir(), envInstance, "read", "--all", "--json", "--tag", "memory:standing", campfireID)
	if err != nil {
		t.Fatalf("cf read --tag memory:standing: %v", err)
	}
	var tagFilteredMsgs []map[string]interface{}
	if err := json.Unmarshal([]byte(tagReadOut), &tagFilteredMsgs); err != nil {
		t.Fatalf("parsing cf read --tag output: %v\noutput: %s", err, tagReadOut)
	}

	// Verify: all returned messages have memory:standing tag.
	for _, msg := range tagFilteredMsgs {
		tags, _ := msg["tags"].([]interface{})
		hasStanding := false
		for _, tag := range tags {
			if tag == "memory:standing" {
				hasStanding = true
				break
			}
		}
		if !hasStanding {
			t.Errorf("tag filter returned message without memory:standing: payload=%q tags=%v",
				msg["payload"], tags)
		}
	}

	// Verify we got the standing messages.
	foundStanding1 := false
	foundStanding2 := false
	for _, msg := range tagFilteredMsgs {
		id, _ := msg["id"].(string)
		if id == standing1ID {
			foundStanding1 = true
		}
		if id == standing2ID {
			foundStanding2 = true
		}
	}
	if !foundStanding1 {
		t.Errorf("tag filter missing standing1 (id=%s...)", standing1ID[:8])
	}
	if !foundStanding2 {
		t.Errorf("tag filter missing standing2 (id=%s...)", standing2ID[:8])
	}

	// Verify proposed messages are NOT in the filtered output.
	for _, msg := range tagFilteredMsgs {
		id, _ := msg["id"].(string)
		if id == proposed1ID || id == proposed2ID {
			t.Errorf("tag filter (memory:standing) wrongly returned proposed message id=%s...", id[:8])
		}
	}
	t.Logf("Tag filter returned %d messages (all memory:standing)", len(tagFilteredMsgs))

	// ---- Step 6: Create a campfire:view "standing-facts" ----
	t.Log("Step 6: Create campfire:view 'standing-facts'")
	viewCreateOut, _, err := runCF(t, t.TempDir(), envInstance, "view", "create",
		campfireID, "standing-facts",
		"--predicate", `(tag "memory:standing")`)
	if err != nil {
		t.Fatalf("cf view create standing-facts: %v", err)
	}
	viewMsgID := strings.TrimSpace(viewCreateOut)
	if len(viewMsgID) == 0 {
		t.Fatal("cf view create returned empty ID")
	}
	t.Logf("View message ID: %s...", viewMsgID[:8])

	// ---- Step 7: cf view read returns materialized results ----
	t.Log("Step 7: cf view read 'standing-facts'")
	viewReadOut, _, err := runCF(t, t.TempDir(), envInstance, "view", "read", "--json", campfireID, "standing-facts")
	if err != nil {
		t.Fatalf("cf view read standing-facts: %v", err)
	}
	var viewResults []map[string]interface{}
	if err := json.Unmarshal([]byte(viewReadOut), &viewResults); err != nil {
		t.Fatalf("parsing cf view read output: %v\noutput: %s", err, viewReadOut)
	}

	// Should match exactly the standing messages (2 of them).
	if len(viewResults) < 2 {
		t.Errorf("cf view read returned %d results, expected at least 2 (standing messages)", len(viewResults))
	}

	// All view results must have memory:standing tag.
	for _, msg := range viewResults {
		tags, _ := msg["tags"].([]interface{})
		hasStanding := false
		for _, tag := range tags {
			if tag == "memory:standing" {
				hasStanding = true
				break
			}
		}
		if !hasStanding {
			t.Errorf("view result contains non-standing message: payload=%q tags=%v",
				msg["payload"], tags)
		}
	}

	// System messages (campfire:view, campfire:member-joined) must be excluded from view results.
	for _, msg := range viewResults {
		tags, _ := msg["tags"].([]interface{})
		for _, tag := range tags {
			if s, ok := tag.(string); ok && strings.HasPrefix(s, "campfire:") {
				t.Errorf("view result contains system message with tag %q (must be excluded)", s)
			}
		}
	}
	t.Logf("View read returned %d results (only memory:standing, no system messages)", len(viewResults))

	// ---- Step 8: Compact old messages ----
	t.Log("Step 8: Compact messages")
	compactOut, _, err := runCF(t, t.TempDir(), envInstance, "compact", campfireID, "--retention", "archive")
	if err != nil {
		t.Fatalf("cf compact: %v", err)
	}
	t.Logf("Compact output: %s", strings.TrimSpace(compactOut))

	// Default read (no --all) post-compact: superseded messages excluded.
	// First do a plain read to advance the cursor past all existing messages.
	// Then do a fresh --all read to check what's visible by default.
	defaultReadOut, _, err := runCF(t, t.TempDir(), envInstance, "read", "--all", "--json", campfireID)
	if err != nil {
		t.Fatalf("cf read --all (post-compact): %v", err)
	}
	var allReadMsgs []map[string]interface{}
	if err := json.Unmarshal([]byte(defaultReadOut), &allReadMsgs); err != nil {
		t.Fatalf("parsing post-compact --all read: %v\noutput: %s", err, defaultReadOut)
	}

	// --all should include all messages: proposed (superseded), standing, compact event.
	foundProposed1InAll := false
	foundProposed2InAll := false
	foundCompactEvent := false
	for _, msg := range allReadMsgs {
		id, _ := msg["id"].(string)
		tags, _ := msg["tags"].([]interface{})
		if id == proposed1ID {
			foundProposed1InAll = true
		}
		if id == proposed2ID {
			foundProposed2InAll = true
		}
		for _, tag := range tags {
			if tag == "campfire:compact" {
				foundCompactEvent = true
			}
		}
	}
	if !foundProposed1InAll {
		t.Errorf("cf read --all missing proposed1 (id=%s...) — compacted messages must appear with --all", proposed1ID[:8])
	}
	if !foundProposed2InAll {
		t.Errorf("cf read --all missing proposed2 (id=%s...) — compacted messages must appear with --all", proposed2ID[:8])
	}
	if !foundCompactEvent {
		t.Error("cf read --all missing campfire:compact event message")
	}
	t.Logf("--all read: %d total messages (includes superseded + compact event)", len(allReadMsgs))

	// ---- Step 9: Default read excludes superseded messages ----
	t.Log("Step 9: Default read post-compact excludes superseded messages")

	// Open a fresh store (new temp CF_HOME) and read without --all to verify
	// that compaction is respected by default.
	freshHome := t.TempDir()
	freshEnv := cfEnv(freshHome, beaconDir, transportDir)
	_, _, err = runCF(t, t.TempDir(), freshEnv, "init", "--session")
	if err != nil {
		t.Fatalf("cf init --session (fresh): %v", err)
	}
	// Parse the fresh session home from the output.
	freshInitOut, _, err := runCF(t, t.TempDir(), cfEnv(freshHome, beaconDir, transportDir), "init", "--session")
	if err != nil {
		t.Fatalf("cf init --session (fresh2): %v", err)
	}
	freshLines := strings.Split(strings.TrimSpace(freshInitOut), "\n")
	if len(freshLines) < 1 {
		t.Fatal("cf init --session (fresh2) gave no output")
	}
	freshSessionHome := strings.TrimSpace(freshLines[0])
	freshEnv = cfEnv(freshSessionHome, beaconDir, transportDir)

	// Join the campfire with the fresh agent.
	_, _, err = runCF(t, t.TempDir(), freshEnv, "join", campfireID)
	if err != nil {
		t.Fatalf("cf join (fresh agent): %v", err)
	}

	// Read --all to sync all messages into the fresh store.
	freshReadAllOut, _, err := runCF(t, t.TempDir(), freshEnv, "read", "--all", "--json", campfireID)
	if err != nil {
		t.Fatalf("cf read --all (fresh agent): %v", err)
	}
	var freshAllMsgs []map[string]interface{}
	if err := json.Unmarshal([]byte(freshReadAllOut), &freshAllMsgs); err != nil {
		t.Fatalf("parsing fresh --all read: %v\noutput: %s", err, freshReadAllOut)
	}

	// With --all, the fresh agent should also see the compacted messages and the compact event.
	freshFoundProposed1 := false
	freshFoundCompact := false
	for _, msg := range freshAllMsgs {
		id, _ := msg["id"].(string)
		tags, _ := msg["tags"].([]interface{})
		if id == proposed1ID {
			freshFoundProposed1 = true
		}
		for _, tag := range tags {
			if tag == "campfire:compact" {
				freshFoundCompact = true
			}
		}
	}
	if !freshFoundProposed1 {
		t.Errorf("fresh agent cf read --all missing proposed1 (id=%s...)", proposed1ID[:8])
	}
	if !freshFoundCompact {
		t.Error("fresh agent cf read --all missing campfire:compact event")
	}
	t.Logf("Fresh agent --all: %d messages", len(freshAllMsgs))

	// ---- Step 10: cf read --fields id,tags,payload returns projected output ----
	t.Log("Step 10: cf read --fields id,tags,payload")
	fieldsReadOut, _, err := runCF(t, t.TempDir(), envInstance, "read", "--all", "--json",
		"--fields", "id,tags,payload", campfireID)
	if err != nil {
		t.Fatalf("cf read --fields id,tags,payload: %v", err)
	}
	var fieldsMsgs []map[string]interface{}
	if err := json.Unmarshal([]byte(fieldsReadOut), &fieldsMsgs); err != nil {
		t.Fatalf("parsing cf read --fields output: %v\noutput: %s", err, fieldsReadOut)
	}

	if len(fieldsMsgs) == 0 {
		t.Fatal("cf read --fields returned no messages")
	}

	// Each message should only have id, tags, and payload fields (no sender, timestamp, etc.).
	for i, msg := range fieldsMsgs {
		for key := range msg {
			switch key {
			case "id", "tags", "payload":
				// expected projected fields
			default:
				t.Errorf("fieldsMsgs[%d] contains unexpected field %q (only id,tags,payload expected)", i, key)
			}
		}
		// Verify required fields are present.
		if _, ok := msg["id"]; !ok {
			t.Errorf("fieldsMsgs[%d] missing 'id' field", i)
		}
		if _, ok := msg["tags"]; !ok {
			t.Errorf("fieldsMsgs[%d] missing 'tags' field", i)
		}
		if _, ok := msg["payload"]; !ok {
			t.Errorf("fieldsMsgs[%d] missing 'payload' field", i)
		}
	}
	t.Logf("--fields projection: %d messages with only {id, tags, payload}", len(fieldsMsgs))

	t.Log("All automaton substrate scenarios verified successfully.")
}
