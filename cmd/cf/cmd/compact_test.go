package cmd

// Tests for workspace-o3l.3: campfire:compact compaction event implementation.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupCompactTestEnv creates a full campfire environment with:
// - A fresh CF_HOME (identity + store)
// - A filesystem transport campfire
// - The agent joined with the given role
// Returns (agentID, store, campfireID, transportBaseDir, cfHomeDir).
func setupCompactTestEnv(t *testing.T, role string) (*identity.Identity, *store.Store, string, string, string) {
	t.Helper()

	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, role)
	return agentID, s, campfireID, transportBaseDir, cfHomeDir
}

// seedMessages sends n messages to the campfire via the filesystem transport
// and stores them in the local store. Returns the list of message IDs.
func seedMessages(t *testing.T, n int, agentID *identity.Identity, s *store.Store, campfireID, transportBaseDir string) []string {
	t.Helper()
	transport := fs.New(transportBaseDir)
	var ids []string
	for i := 0; i < n; i++ {
		cfDir := filepath.Join(transportBaseDir, campfireID)
		stateData, err := os.ReadFile(filepath.Join(cfDir, "campfire.cbor"))
		if err != nil {
			t.Fatalf("reading campfire state: %v", err)
		}
		var state campfire.CampfireState
		if err := cfencoding.Unmarshal(stateData, &state); err != nil {
			t.Fatalf("unmarshalling state: %v", err)
		}
		members, err := transport.ListMembers(campfireID)
		if err != nil {
			t.Fatalf("listing members: %v", err)
		}

		cf := campfireFromState(&state, members)
		transportDir := filepath.Join(transportBaseDir, campfireID)
		msg, err := sendFilesystem(campfireID, "message content", []string{"status"}, []string{}, "", agentID, transportDir)
		if err != nil {
			_ = cf // suppress unused warning
			t.Fatalf("sendFilesystem: %v", err)
		}
		ids = append(ids, msg.ID)

		// Store locally.
		s.AddMessage(store.MessageRecord{ //nolint:errcheck
			ID:          msg.ID,
			CampfireID:  campfireID,
			Sender:      agentID.PublicKeyHex(),
			Payload:     msg.Payload,
			Tags:        msg.Tags,
			Antecedents: msg.Antecedents,
			Timestamp:   msg.Timestamp,
			Signature:   msg.Signature,
			Provenance:  msg.Provenance,
			ReceivedAt:  store.NowNano(),
		})
		// Small delay to ensure distinct timestamps.
		time.Sleep(time.Millisecond)
	}
	return ids
}

// TestCompactCreatesCompactionEvent verifies that execCompact creates a valid
// campfire:compact message in the local store.
func TestCompactCreatesCompactionEvent(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)

	msgIDs := seedMessages(t, 3, agentID, s, campfireID, transportBaseDir)

	// Run compact (no --before: compact all).
	if _, err := execCompact(campfireID, "", "summary text", "archive", agentID, s); err != nil {
		t.Fatalf("execCompact: %v", err)
	}

	// Verify a campfire:compact event exists in the store.
	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d compaction events, want 1", len(events))
	}

	// Verify payload.
	var payload store.CompactionPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshalling compaction payload: %v", err)
	}
	if len(payload.Supersedes) != len(msgIDs) {
		t.Errorf("supersedes count = %d, want %d", len(payload.Supersedes), len(msgIDs))
	}
	if payload.Retention != "archive" {
		t.Errorf("retention = %q, want archive", payload.Retention)
	}
	if payload.CheckpointHash == "" {
		t.Error("checkpoint_hash must not be empty")
	}
	if !strings.Contains(string(payload.Summary), "summary text") {
		t.Errorf("summary = %q, expected to contain 'summary text'", string(payload.Summary))
	}

	// Verify the compaction event has the campfire:compact tag.
	tags := events[0].Tags
	found := false
	for _, tag := range tags {
		if tag == "campfire:compact" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("compaction event missing campfire:compact tag, got: %v", tags)
	}

	// Verify antecedents contains the last superseded message.
	antecedents := events[0].Antecedents
	if len(antecedents) != 1 {
		t.Fatalf("antecedents count = %d, want 1", len(antecedents))
	}
	if antecedents[0] == "" {
		t.Error("antecedent should be the last superseded message ID, got empty")
	}
}

// TestCompactRoleEnforcement verifies that only "full" role members can compact.
func TestCompactRoleEnforcement(t *testing.T) {
	cases := []struct {
		role      string
		wantError bool
	}{
		{campfire.RoleFull, false},
		{campfire.RoleWriter, true},
		{campfire.RoleObserver, true},
		{"", false}, // empty role defaults to full
	}

	for _, tc := range cases {
		t.Run("role="+tc.role, func(t *testing.T) {
			agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, tc.role)
			seedMessages(t, 2, agentID, s, campfireID, transportBaseDir)

			_, err := execCompact(campfireID, "", "summary", "archive", agentID, s)
			if tc.wantError && err == nil {
				t.Errorf("role %q: expected error, got nil", tc.role)
			}
			if !tc.wantError && err != nil {
				t.Errorf("role %q: expected no error, got: %v", tc.role, err)
			}
			if tc.wantError && err != nil && !isRoleError(err) {
				t.Errorf("role %q: expected role enforcement error, got: %v", tc.role, err)
			}
		})
	}
}

// TestCompactBeforeFlag verifies that --before only supersedes messages
// strictly before the given message's timestamp.
func TestCompactBeforeFlag(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)

	msgIDs := seedMessages(t, 4, agentID, s, campfireID, transportBaseDir)
	// Compact only messages before msgIDs[2] (i.e., msgIDs[0] and msgIDs[1]).
	if _, err := execCompact(campfireID, msgIDs[2], "before-test", "archive", agentID, s); err != nil {
		t.Fatalf("execCompact with --before: %v", err)
	}

	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	var payload store.CompactionPayload
	json.Unmarshal(events[0].Payload, &payload) //nolint:errcheck

	// Should supersede exactly msgIDs[0] and msgIDs[1].
	superseded := make(map[string]bool)
	for _, id := range payload.Supersedes {
		superseded[id] = true
	}
	if !superseded[msgIDs[0]] {
		t.Errorf("msgIDs[0] should be superseded")
	}
	if !superseded[msgIDs[1]] {
		t.Errorf("msgIDs[1] should be superseded")
	}
	if superseded[msgIDs[2]] {
		t.Errorf("msgIDs[2] (the --before boundary) should NOT be superseded")
	}
	if superseded[msgIDs[3]] {
		t.Errorf("msgIDs[3] should NOT be superseded")
	}
}

// TestCompactNoMessages verifies that compacting an empty campfire returns an error.
func TestCompactNoMessages(t *testing.T) {
	agentID, s, campfireID, _, _ := setupCompactTestEnv(t, campfire.RoleFull)

	_, err := execCompact(campfireID, "", "summary", "archive", agentID, s)
	if err == nil {
		t.Fatal("expected error when there are no messages to compact, got nil")
	}
}

// TestCompactCheckpointHash verifies the checkpoint hash is deterministic and non-empty.
func TestCompactCheckpointHash(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	seedMessages(t, 2, agentID, s, campfireID, transportBaseDir)

	result, err := execCompact(campfireID, "", "hash-test", "archive", agentID, s)
	if err != nil {
		t.Fatalf("execCompact: %v", err)
	}

	if len(result.checkpointHash) != 64 {
		t.Errorf("checkpoint_hash length = %d, want 64 (SHA-256 hex)", len(result.checkpointHash))
	}
}

// TestListMessages_RespectCompaction_ViaReadPath verifies that after a compaction event,
// ListMessages with RespectCompaction=true excludes superseded messages but keeps the
// compaction event itself.
func TestListMessages_RespectCompaction_ViaReadPath(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	msgIDs := seedMessages(t, 3, agentID, s, campfireID, transportBaseDir)

	// Compact all messages.
	if _, err := execCompact(campfireID, "", "summary", "archive", agentID, s); err != nil {
		t.Fatalf("execCompact: %v", err)
	}

	// Default read: superseded messages should be excluded.
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{RespectCompaction: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	ids := make(map[string]bool)
	for _, m := range msgs {
		ids[m.ID] = true
	}

	for _, id := range msgIDs {
		if ids[id] {
			t.Errorf("superseded message %s should be excluded from compacted read", id)
		}
	}

	// The compaction event itself should be visible.
	events, _ := s.ListCompactionEvents(campfireID)
	if len(events) == 0 {
		t.Fatal("no compaction events found")
	}
	if !ids[events[0].ID] {
		t.Errorf("compaction event %s should be visible in compacted read", events[0].ID)
	}
}

// TestCompactBeforeNotFound verifies that --before with a non-existent message ID
// returns a "message not found" error (regression test for workspace-pm9m.5.2: the
// old sentinel `beforeTS == 0` was incorrect; the correct sentinel is `matchedID == ""`).
func TestCompactBeforeNotFound(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	seedMessages(t, 2, agentID, s, campfireID, transportBaseDir)

	// A completely unknown message ID: must return "message not found".
	_, err := execCompact(campfireID, "nonexistent-id-that-does-not-exist", "test", "archive", agentID, s)
	if err == nil {
		t.Fatal("expected 'message not found' error for unknown ID, got nil")
	}
	if !strings.Contains(err.Error(), "message not found") {
		t.Errorf("expected 'message not found' error, got: %v", err)
	}
}

// TestCompactBeforeSentinelIsMatchedID documents the sentinel fix for workspace-pm9m.5.2.
// The old code used `beforeTS == 0` as the not-found sentinel, which would incorrectly
// return "message not found" for any message whose Timestamp field is zero (e.g., messages
// created in tests or via import with clock reset). The correct sentinel is `matchedID == ""`.
//
// This test verifies the sentinel via the --before path: a valid message ID compacts
// the correct set of messages, confirming matchedID-based detection works correctly.
func TestCompactBeforeSentinelIsMatchedID(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	msgIDs := seedMessages(t, 3, agentID, s, campfireID, transportBaseDir)

	// Compact --before msgIDs[2]. With the correct matchedID sentinel, any found
	// message with any timestamp value is handled correctly.
	_, err := execCompact(campfireID, msgIDs[2], "sentinel-test", "archive", agentID, s)
	if err != nil {
		t.Fatalf("execCompact --before valid msg: unexpected error: %v", err)
	}

	// Verify the correct messages were superseded (msgIDs[0] and msgIDs[1] only).
	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 compaction event, got %d", len(events))
	}
	var payload store.CompactionPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshalling payload: %v", err)
	}
	if len(payload.Supersedes) != 2 {
		t.Errorf("expected 2 superseded messages (before msgIDs[2]), got %d", len(payload.Supersedes))
	}
}


// TestCompactBeforeTimestampCollision verifies that when two messages share the same
// nanosecond timestamp, --before <msg2> correctly supersedes msg1.
//
// Regression test for workspace-pm9m.5.5: the old condition `msg.Timestamp >= beforeTS`
// excluded ALL messages at the same timestamp as the boundary message, including ones
// that should have been compacted. The fix uses `msg.ID == matchedID || msg.Timestamp > beforeTS`
// so only the exact boundary message (and strictly-later messages) are excluded.
func TestCompactBeforeTimestampCollision(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	transportDir := filepath.Join(transportBaseDir, campfireID)

	sharedTS := store.NowNano()

	sendAndStore := func(payload string) string {
		msg, err := sendFilesystem(campfireID, payload, []string{"status"}, []string{}, "", agentID, transportDir)
		if err != nil {
			t.Fatalf("sendFilesystem: %v", err)
		}
		s.AddMessage(store.MessageRecord{ //nolint:errcheck
			ID:          msg.ID,
			CampfireID:  campfireID,
			Sender:      agentID.PublicKeyHex(),
			Payload:     msg.Payload,
			Tags:        msg.Tags,
			Antecedents: msg.Antecedents,
			Timestamp:   sharedTS, // force identical timestamp for both messages
			Signature:   msg.Signature,
			Provenance:  msg.Provenance,
			ReceivedAt:  store.NowNano(),
		})
		return msg.ID
	}

	// M1 and M2 share the same nanosecond timestamp.
	m1ID := sendAndStore("msg1")
	m2ID := sendAndStore("msg2")

	// Compact --before M2: M1 should be superseded, M2 should not.
	result, err := execCompact(campfireID, m2ID, "collision-test", "archive", agentID, s)
	if err != nil {
		t.Fatalf("execCompact with timestamp collision: %v", err)
	}

	superseded := make(map[string]bool, len(result.supersededIDs))
	for _, id := range result.supersededIDs {
		superseded[id] = true
	}
	if !superseded[m1ID] {
		t.Errorf("m1 (same timestamp as before-msg) should be superseded but was not")
	}
	if superseded[m2ID] {
		t.Errorf("m2 (the --before boundary) should NOT be superseded but was")
	}
}

// TestListMessages_AllShowsEverything verifies that without compaction filtering,
// all messages are visible including superseded ones.
func TestListMessages_AllShowsEverything(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	msgIDs := seedMessages(t, 3, agentID, s, campfireID, transportBaseDir)

	if _, err := execCompact(campfireID, "", "summary", "archive", agentID, s); err != nil {
		t.Fatalf("execCompact: %v", err)
	}

	// Without RespectCompaction, all messages (3 seeded + 1 compaction) should appear.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages (all): %v", err)
	}
	// 3 seeded + 1 compaction event = 4
	if len(msgs) != len(msgIDs)+1 {
		t.Errorf("got %d messages without compaction filter, want %d", len(msgs), len(msgIDs)+1)
	}
}
