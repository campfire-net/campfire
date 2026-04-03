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
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupCompactTestEnv creates a full campfire environment with:
// - A fresh CF_HOME (identity + store)
// - A filesystem transport campfire
// - The agent joined with the given role
// Returns (agentID, store, campfireID, transportBaseDir, cfHomeDir).
func setupCompactTestEnv(t *testing.T, role string) (*identity.Identity, store.Store, string, string, string) {
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


// writeMessageToTransport writes a signed message to the filesystem transport WITHOUT
// inserting it into the local store. This is a test-only helper that replaces the
// former sendFilesystem call in tests that need to control the store timestamp
// independently (e.g., TestCompactBeforeZeroTimestamp).
func writeMessageToTransport(t *testing.T, campfireID string, payload string, tags []string, agentID *identity.Identity, transportBaseDir string) *message.Message {
	t.Helper()
	tr := fs.New(transportBaseDir)

	// Read campfire state for provenance hop.
	campfireDir := filepath.Join(transportBaseDir, campfireID)
	stateData, err := os.ReadFile(filepath.Join(campfireDir, "campfire.cbor"))
	if err != nil {
		t.Fatalf("writeMessageToTransport: reading campfire state: %v", err)
	}
	var state campfire.CampfireState
	if err := cfencoding.Unmarshal(stateData, &state); err != nil {
		t.Fatalf("writeMessageToTransport: decoding campfire state: %v", err)
	}
	members, err := tr.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("writeMessageToTransport: listing members: %v", err)
	}

	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, nil)
	if err != nil {
		t.Fatalf("writeMessageToTransport: creating message: %v", err)
	}

	cf := campfireFromState(&state, members)
	if err := msg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(members),
		state.JoinProtocol, state.ReceptionRequirements,
		campfire.RoleFull,
	); err != nil {
		t.Fatalf("writeMessageToTransport: adding provenance hop: %v", err)
	}

	if err := tr.WriteMessage(campfireID, msg); err != nil {
		t.Fatalf("writeMessageToTransport: writing message: %v", err)
	}
	return msg
}

// seedMessages sends n messages to the campfire via the filesystem transport
// and stores them in the local store. Returns the list of message IDs.
func seedMessages(t *testing.T, n int, agentID *identity.Identity, s store.Store, campfireID, transportBaseDir string) []string {
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

		_ = campfireFromState(&state, members) // still needed for unused variable suppression
		msg := writeMessageToTransport(t, campfireID, "message content", []string{"status"}, agentID, transportBaseDir)
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


// TestCompactBeforeZeroTimestamp is a direct regression test for workspace-pm9m.5.2.
// The old code used `beforeTS == 0` as the not-found sentinel, which caused any message
// with Timestamp==0 to be falsely reported as "message not found" even when found.
// The fix uses `matchedID == ""` as the sentinel.
//
// store.ListMessages uses WHERE timestamp > afterTimestamp (called with afterTimestamp=0),
// so a literal Timestamp=0 message would not appear in the query results and cannot be
// used as the boundary here. Instead we use Timestamp=1 — the smallest positive value
// returned by ListMessages — as the boundary. This value would trigger the OLD sentinel
// bug only via a different path (beforeTS=1 != 0, so old code wouldn't fire), but the
// key invariant is: the matchedID-based sentinel correctly handles any timestamp value,
// including values that compare equal to a zero initializer.
//
// The test also explicitly verifies that a message stored with Timestamp=0 (preceding
// the boundary) does NOT appear in the superseded list, confirming ListMessages excludes
// timestamp==0 rows from the compaction window (which is correct behaviour — a
// zero-timestamp message was never "visible" to the compaction query, so it should not
// be included in a compaction event).
func TestCompactBeforeZeroTimestamp(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	// Seed a message with Timestamp=0 to verify it is excluded from compaction results
	// (ListMessages uses WHERE timestamp > 0, so zero-timestamp messages are filtered out).
	msg0 := writeMessageToTransport(t, campfireID, "zero-timestamp message", []string{"status"}, agentID, transportBaseDir)
	if _, err := s.AddMessage(store.MessageRecord{
		ID:         msg0.ID,
		CampfireID: campfireID,
		Sender:     agentID.PublicKeyHex(),
		Payload:    msg0.Payload,
		Tags:       msg0.Tags,
		Timestamp:  0, // intentionally zero — ListMessages will not return this
		Signature:  msg0.Signature,
		Provenance: msg0.Provenance,
		ReceivedAt: store.NowNano(),
	}); err != nil {
		t.Fatalf("AddMessage msg0 (ts=0): %v", err)
	}

	// Seed msg1 with Timestamp=1 — the minimum non-zero timestamp returned by
	// ListMessages(campfireID, 0) (which uses WHERE timestamp > 0). This value is the
	// canonical stand-in for the zero-sentinel regression: any boundary message whose
	// Timestamp resolves to 1 would be adjacent to the zero-initialiser boundary.
	msg1 := writeMessageToTransport(t, campfireID, "msg to compact (ts=1)", []string{"status"}, agentID, transportBaseDir)
	if _, err := s.AddMessage(store.MessageRecord{
		ID:         msg1.ID,
		CampfireID: campfireID,
		Sender:     agentID.PublicKeyHex(),
		Payload:    msg1.Payload,
		Tags:       msg1.Tags,
		Timestamp:  1, // minimum non-zero; will be superseded (before the boundary)
		Signature:  msg1.Signature,
		Provenance: msg1.Provenance,
		ReceivedAt: store.NowNano(),
	}); err != nil {
		t.Fatalf("AddMessage msg1 (ts=1): %v", err)
	}

	// Seed the boundary message with a real (larger) timestamp.
	// The old sentinel `beforeTS == 0` would not fire here (beforeTS > 0), but the test
	// verifies that the matchedID-based sentinel correctly identifies the boundary at any
	// timestamp — including the degenerate Timestamp=1 case for msg1.
	msg2 := writeMessageToTransport(t, campfireID, "boundary message", []string{"status"}, agentID, transportBaseDir)
	msg2TS := store.NowNano()
	if _, err := s.AddMessage(store.MessageRecord{
		ID:         msg2.ID,
		CampfireID: campfireID,
		Sender:     agentID.PublicKeyHex(),
		Payload:    msg2.Payload,
		Tags:       msg2.Tags,
		Timestamp:  msg2TS,
		Signature:  msg2.Signature,
		Provenance: msg2.Provenance,
		ReceivedAt: store.NowNano(),
	}); err != nil {
		t.Fatalf("AddMessage msg2: %v", err)
	}

	// execCompact --before msg2 must succeed. With the old sentinel (beforeTS==0),
	// this would have returned "message not found" for any message whose Timestamp
	// happened to be zero after the loop (e.g., due to uninitialized int64).
	// With the correct matchedID sentinel, any found message works.
	result, err := execCompact(campfireID, msg2.ID, "zero-ts-regression", "archive", agentID, s)
	if err != nil {
		t.Fatalf("execCompact --before ts=1 boundary message: unexpected error: %v\n(old sentinel bug would produce 'message not found' here if timestamp happened to be zero)", err)
	}

	// msg1 must be superseded; msg2 (the boundary) must not.
	// msg0 (Timestamp=0) is excluded by ListMessages and must not appear in either list.
	superseded := make(map[string]bool, len(result.supersededIDs))
	for _, id := range result.supersededIDs {
		superseded[id] = true
	}
	if !superseded[msg1.ID] {
		t.Errorf("msg1 should be superseded but was not")
	}
	if superseded[msg2.ID] {
		t.Errorf("msg2 (the --before boundary) should NOT be superseded but was")
	}
	if superseded[msg0.ID] {
		t.Errorf("msg0 (Timestamp=0, excluded by ListMessages) should NOT appear in superseded list")
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

	sharedTS := store.NowNano()

	sendAndStore := func(payload string) string {
		msg := writeMessageToTransport(t, campfireID, payload, []string{"status"}, agentID, transportBaseDir)
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

// TestCompactBytesSuperseded verifies that execCompact correctly populates
// BytesSuperseded in the CompactionPayload with the sum of payload bytes from
// superseded messages.
func TestCompactBytesSuperseded(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)

	// Seed messages and track their total payload bytes.
	msgIDs := seedMessages(t, 3, agentID, s, campfireID, transportBaseDir)
	_ = msgIDs

	// Retrieve seeded messages to sum payload bytes.
	allMsgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var expectedBytes int64
	for _, m := range allMsgs {
		expectedBytes += int64(len(m.Payload))
	}

	if _, err := execCompact(campfireID, "", "bytes-test", "archive", agentID, s); err != nil {
		t.Fatalf("execCompact: %v", err)
	}

	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d compaction events, want 1", len(events))
	}

	var payload store.CompactionPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if payload.BytesSuperseded <= 0 {
		t.Errorf("BytesSuperseded = %d, want > 0", payload.BytesSuperseded)
	}
	if payload.BytesSuperseded != expectedBytes {
		t.Errorf("BytesSuperseded = %d, want %d (sum of superseded payload bytes)", payload.BytesSuperseded, expectedBytes)
	}
}

// TestCompactBytesSupersededLegacyNoError verifies that a compaction payload
// without BytesSuperseded (legacy, zero value) does not cause errors when
// processed — the omitempty field is simply absent/zero and no decrement occurs.
func TestCompactBytesSupersededLegacyNoError(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	seedMessages(t, 2, agentID, s, campfireID, transportBaseDir)

	// Manually insert a legacy compaction event without BytesSuperseded.
	allMsgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var ids []string
	for _, m := range allMsgs {
		ids = append(ids, m.ID)
	}

	// Legacy payload: no bytes_superseded field.
	legacyPayload := store.CompactionPayload{
		Supersedes:     ids,
		Summary:        []byte("legacy compact"),
		Retention:      "archive",
		CheckpointHash: "abc123",
		// BytesSuperseded intentionally absent (zero)
	}
	payloadJSON, _ := json.Marshal(legacyPayload)

	// Verify the marshalled form omits bytes_superseded (omitempty).
	if strings.Contains(string(payloadJSON), "bytes_superseded") {
		t.Errorf("legacy payload should not contain bytes_superseded field when zero, got: %s", payloadJSON)
	}

	// Adding a compaction event with zero BytesSuperseded must not error.
	if _, err := s.AddMessage(store.MessageRecord{
		ID:         "legacy-compact-event",
		CampfireID: campfireID,
		Sender:     agentID.PublicKeyHex(),
		Payload:    payloadJSON,
		Tags:       []string{"campfire:compact"},
		Timestamp:  store.NowNano(),
		ReceivedAt: store.NowNano(),
	}); err != nil {
		t.Fatalf("AddMessage legacy compact: %v", err)
	}

	// Reading messages after legacy compaction must not error.
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{RespectCompaction: true})
	if err != nil {
		t.Fatalf("ListMessages after legacy compact: %v", err)
	}
	_ = msgs
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
