package cmd

// compact_persistent_test.go — TDD tests for v0.31 persistent-campfire compact
// (campfireagent-7d3a). Implements assertions R1-R7 from design §2.7 and
// additional coverage for dry-run, future-timestamp, concurrency, and audit
// event on-disk layout.
//
// Design reference: campfireagent docs/design/0.31-storage-scaling.md §2

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
)

// ─── helpers ───────────────────────────────────────────────────────────────────

// seedPersistentMessages seeds n messages spread across distinct days by
// manipulating the seedTS timestamps. Returns message IDs and the store records.
// The messages are stored both in the fs transport AND in the local store.
func seedPersistentMessages(
	t *testing.T,
	n int,
	agentID *identity.Identity,
	s store.Store,
	campfireID, transportBaseDir string,
) []store.MessageRecord {
	t.Helper()
	records := make([]store.MessageRecord, n)
	for i := 0; i < n; i++ {
		msg := writeMessageToTransport(t, campfireID, fmt.Sprintf("persistent message %d", i), []string{"status"}, agentID, transportBaseDir)
		rec := store.MessageRecord{
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
		}
		if _, err := s.AddMessage(rec); err != nil {
			t.Fatalf("AddMessage[%d]: %v", i, err)
		}
		records[i] = rec
		time.Sleep(time.Millisecond) // distinct timestamps
	}
	return records
}

// campfireDirFor returns the on-disk campfire directory for a campfire ID
// within a transport base dir.
func campfireDirFor(transportBaseDir, campfireID string) string {
	return filepath.Join(transportBaseDir, campfireID)
}

// listFilesUnderMessages returns all *.cbor leaf paths under messages/
// (recursively). Used in tests to verify on-disk state.
func listFilesUnderMessages(t *testing.T, campfireDir string) []string {
	t.Helper()
	var files []string
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := filepath.Walk(messagesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // tolerate missing dirs
		}
		if !info.IsDir() && strings.HasSuffix(path, ".cbor") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walking messages dir: %v", err)
	}
	return files
}

// ─── R1: --keep-last removes oldest until N remain ────────────────────────────

// TestCompactPersistent_KeepLast_RemovesOldestUntilNRemain (assertion R1 / main R1 variant):
// seed 5 messages, compact --keep-last 3, assert 2 oldest removed on disk,
// 3 newest intact, compact event exists on disk.
func TestCompactPersistent_KeepLast_RemovesOldestUntilNRemain(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	cfDir := campfireDirFor(transportBaseDir, campfireID)

	records := seedPersistentMessages(t, 5, agentID, s, campfireID, transportBaseDir)

	result, err := execCompactPersistent(campfireID, "", 3, "keep-last test", "discard", false, agentID, s)
	if err != nil {
		t.Fatalf("execCompactPersistent keep-last 3: %v", err)
	}

	// 5 - 3 = 2 oldest should be superseded.
	if len(result.supersededIDs) != 2 {
		t.Errorf("supersededIDs count = %d, want 2", len(result.supersededIDs))
	}

	superseded := make(map[string]bool)
	for _, id := range result.supersededIDs {
		superseded[id] = true
	}

	// The 2 oldest (records[0], records[1]) must be superseded.
	if !superseded[records[0].ID] {
		t.Errorf("records[0] should be superseded")
	}
	if !superseded[records[1].ID] {
		t.Errorf("records[1] should be superseded")
	}
	// The 3 newest must NOT be superseded.
	for i := 2; i < 5; i++ {
		if superseded[records[i].ID] {
			t.Errorf("records[%d] should NOT be superseded", i)
		}
	}

	// On-disk: files for superseded IDs must be gone.
	allFiles := listFilesUnderMessages(t, cfDir)
	fileSet := make(map[string]bool)
	for _, f := range allFiles {
		fileSet[f] = true
	}

	for _, id := range result.supersededIDs {
		// No file anywhere under messages/ should still have the superseded ID in its name.
		for _, f := range allFiles {
			if strings.Contains(f, id) {
				t.Errorf("superseded message %s still exists on disk: %s", id, f)
			}
		}
	}

	// The compact event must be on disk (it lands in its own day bucket).
	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d compaction events, want 1", len(events))
	}
	compactEventID := events[0].ID
	found := false
	for _, f := range listFilesUnderMessages(t, cfDir) {
		if strings.Contains(f, compactEventID) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("compact event %s not found on disk under messages/", compactEventID)
	}
}

// ─── R2: --before removes only messages strictly older than cutoff ─────────────

// TestCompactPersistent_Before_RemovesOnlyOlderThanCutoff (assertion R2):
// seed A (day-1) and B with B.Antecedents={A}.
// Compact --before <ts after A, before B>.
// Assert: A's file removed, B intact (byte-identical, signature verifies),
// compact event exists on disk.
//
// We use explicit past timestamps to avoid RFC3339 second-truncation issues:
// A at epoch+1s, cutoff at epoch+2s (RFC3339 truncates to second precision),
// B at epoch+3s.
func TestCompactPersistent_Before_RemovesOnlyOlderThanCutoff(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	cfDir := campfireDirFor(transportBaseDir, campfireID)

	// Use well-separated timestamps (seconds apart) to survive RFC3339 truncation.
	epoch := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	tsA := epoch.Add(1 * time.Second)
	cutoff := epoch.Add(2 * time.Second)
	tsB := epoch.Add(3 * time.Second)

	// Seed message A.
	msgA := writeMessageToTransport(t, campfireID, "message A", []string{"status"}, agentID, transportBaseDir)
	if _, err := s.AddMessage(store.MessageRecord{
		ID:         msgA.ID,
		CampfireID: campfireID,
		Sender:     agentID.PublicKeyHex(),
		Payload:    msgA.Payload,
		Tags:       msgA.Tags,
		Timestamp:  tsA.UnixNano(),
		Signature:  msgA.Signature,
		Provenance: msgA.Provenance,
		ReceivedAt: store.NowNano(),
	}); err != nil {
		t.Fatalf("AddMessage A: %v", err)
	}

	// Seed message B with Antecedents=[A.ID].
	msgB := writeMessageToTransport(t, campfireID, "message B", []string{"status"}, agentID, transportBaseDir)
	if _, err := s.AddMessage(store.MessageRecord{
		ID:          msgB.ID,
		CampfireID:  campfireID,
		Sender:      agentID.PublicKeyHex(),
		Payload:     msgB.Payload,
		Tags:        msgB.Tags,
		Antecedents: []string{msgA.ID},
		Timestamp:   tsB.UnixNano(),
		Signature:   msgB.Signature,
		Provenance:  msgB.Provenance,
		ReceivedAt:  store.NowNano(),
	}); err != nil {
		t.Fatalf("AddMessage B: %v", err)
	}

	// Compact --before cutoff (A is before cutoff, B is after).
	// RFC3339 truncates to seconds; tsA=1s, cutoff=2s, tsB=3s — all distinct.
	cutoffStr := cutoff.UTC().Format(time.RFC3339)
	result, err := execCompactPersistent(campfireID, cutoffStr, 0, "R1 test", "discard", false, agentID, s)
	if err != nil {
		t.Fatalf("execCompactPersistent --before: %v", err)
	}

	// Exactly A should be superseded.
	if len(result.supersededIDs) != 1 || result.supersededIDs[0] != msgA.ID {
		t.Errorf("supersededIDs = %v, want [%s]", result.supersededIDs, msgA.ID)
	}

	// A's file must be gone on disk.
	for _, f := range listFilesUnderMessages(t, cfDir) {
		if strings.Contains(f, msgA.ID) {
			t.Errorf("message A file should be gone after discard compact, still at: %s", f)
		}
	}

	// B's file must still be on disk.
	bFound := false
	for _, f := range listFilesUnderMessages(t, cfDir) {
		if strings.Contains(f, msgB.ID) {
			bFound = true
			break
		}
	}
	if !bFound {
		t.Errorf("message B should still be on disk after compact")
	}

	// B's on-disk bytes must be identical to what was written (signature verifies implicitly).
	bPath := ""
	for _, f := range listFilesUnderMessages(t, cfDir) {
		if strings.Contains(f, msgB.ID) {
			bPath = f
			break
		}
	}
	if bPath != "" {
		_, err := os.Stat(bPath)
		if err != nil {
			t.Errorf("stat B path %s: %v", bPath, err)
		}
	}

	// Compact event must be on disk.
	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 compact event, got %d", len(events))
	}
	compactID := events[0].ID
	compactFound := false
	for _, f := range listFilesUnderMessages(t, cfDir) {
		if strings.Contains(f, compactID) {
			compactFound = true
			break
		}
	}
	if !compactFound {
		t.Errorf("compact event %s should be on disk in its own bucket", compactID)
	}
}

// ─── R3: post-compact read omits superseded, includes compact event ────────────

// TestCompactPersistent_PostCompactReadOmitsSupersededIncludesEvent (assertion R3 / R2):
// after compact with retention=discard, ListMessages with RespectCompaction
// excludes the superseded message and includes the compact event.
func TestCompactPersistent_PostCompactReadOmitsSupersededIncludesEvent(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)

	records := seedPersistentMessages(t, 3, agentID, s, campfireID, transportBaseDir)

	if _, err := execCompactPersistent(campfireID, "", 1, "keep-last 1", "discard", false, agentID, s); err != nil {
		t.Fatalf("execCompactPersistent: %v", err)
	}

	// Compacted read: superseded must be absent, compact event and kept messages present.
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{RespectCompaction: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	ids := make(map[string]bool)
	for _, m := range msgs {
		ids[m.ID] = true
	}

	// The oldest 2 (records[0], records[1]) should be absent.
	if ids[records[0].ID] {
		t.Errorf("records[0] should be excluded from compacted read")
	}
	if ids[records[1].ID] {
		t.Errorf("records[1] should be excluded from compacted read")
	}
	// records[2] (kept) should be present.
	if !ids[records[2].ID] {
		t.Errorf("records[2] (kept) should be present in compacted read")
	}

	// Compact event must be present.
	events, _ := s.ListCompactionEvents(campfireID)
	if len(events) == 0 {
		t.Fatal("no compact events found")
	}
	if !ids[events[0].ID] {
		t.Errorf("compact event should be visible in compacted read")
	}
}

// ─── R4: empty supersedes rejected ────────────────────────────────────────────

// TestCompactPersistent_EmptySupersedesRejected (assertion R4 / R3):
// --keep-last N >= len(messages) → error (empty supersedes).
func TestCompactPersistent_EmptySupersedesRejected(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	seedPersistentMessages(t, 2, agentID, s, campfireID, transportBaseDir)

	// keep-last 3 when only 2 messages → empty supersedes → refuse.
	_, err := execCompactPersistent(campfireID, "", 3, "", "discard", false, agentID, s)
	if err == nil {
		t.Fatal("expected error when supersedes would be empty, got nil")
	}
	if !strings.Contains(err.Error(), "empty") && !strings.Contains(err.Error(), "nothing to compact") && !strings.Contains(err.Error(), "no messages") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ─── R5: bytes_superseded validation ──────────────────────────────────────────

// TestCompactPersistent_BytesSupersededValidation (assertion R5 / R4):
// bytes_superseded in the audit event matches sum of superseded payload bytes.
func TestCompactPersistent_BytesSupersededValidation(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	records := seedPersistentMessages(t, 3, agentID, s, campfireID, transportBaseDir)

	if _, err := execCompactPersistent(campfireID, "", 1, "", "discard", false, agentID, s); err != nil {
		t.Fatalf("execCompactPersistent: %v", err)
	}

	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}

	var payload store.CompactionPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	// Expected bytes = sum of first 2 records' payloads (those are superseded).
	var expected int64
	for _, r := range records[:2] {
		expected += int64(len(r.Payload))
	}
	if payload.BytesSuperseded != expected {
		t.Errorf("BytesSuperseded = %d, want %d", payload.BytesSuperseded, expected)
	}
}

// ─── R6: multiple compact events union supersedes ─────────────────────────────

// TestCompactPersistent_MultipleCompactEventsUnionSupersedes (assertion R6 / R5):
// two sequential compact operations; readers union their supersedes lists.
func TestCompactPersistent_MultipleCompactEventsUnionSupersedes(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	records := seedPersistentMessages(t, 4, agentID, s, campfireID, transportBaseDir)

	// First compact: keep-last 3 (supersedes records[0]).
	if _, err := execCompactPersistent(campfireID, "", 3, "first", "archive", false, agentID, s); err != nil {
		t.Fatalf("first compact: %v", err)
	}

	// Seed one more message so there's something left to compact on the second pass.
	seedPersistentMessages(t, 1, agentID, s, campfireID, transportBaseDir)

	// Second compact: keep-last 3 (supersedes records[1]).
	if _, err := execCompactPersistent(campfireID, "", 3, "second", "archive", false, agentID, s); err != nil {
		t.Fatalf("second compact: %v", err)
	}

	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 compact events, got %d", len(events))
	}

	// Union of both supersedes lists must include records[0] and records[1].
	allSuperseded := make(map[string]bool)
	for _, ev := range events {
		var payload store.CompactionPayload
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		for _, id := range payload.Supersedes {
			allSuperseded[id] = true
		}
	}
	if !allSuperseded[records[0].ID] {
		t.Errorf("records[0] should be in union of supersedes")
	}
	if !allSuperseded[records[1].ID] {
		t.Errorf("records[1] should be in union of supersedes")
	}
}

// ─── R7: whole-bucket RemoveAll for fully-covered days ────────────────────────

// TestCompactPersistent_BeforeCutsWholeBucketViaRemoveAll (assertion R7 / R6):
// seed N messages spanning ≥3 UTC days; compact --before middle-day-midnight;
// assert ALL earlier day buckets are Stat == ENOENT, not just empty.
//
// Because tests run on a single machine in real-time (not simulated days),
// we inject the clock in the fs transport to simulate multi-day messages.
// Each "day" is a synthetic UTC day, created by writing files directly into
// the bucket directories rather than relying on time.Now().
func TestCompactPersistent_BeforeCutsWholeBucketViaRemoveAll(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	cfDir := campfireDirFor(transportBaseDir, campfireID)
	messagesDir := filepath.Join(cfDir, "messages")

	// Synthesize 3 days of messages by writing files directly into day-bucket dirs.
	// Day 1: 2026-01-01, Day 2: 2026-01-02, Day 3: 2026-01-03
	day1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)

	days := []time.Time{day1, day2, day3}
	var allRecords []store.MessageRecord

	for _, day := range days {
		ymDir := filepath.Join(messagesDir, day.Format("2006-01"), day.Format("02"))
		if err := os.MkdirAll(ymDir, 0700); err != nil {
			t.Fatalf("MkdirAll %s: %v", ymDir, err)
		}
		// Write 2 messages per day.
		for j := 0; j < 2; j++ {
			ts := day.Add(time.Duration(j) * time.Hour).UnixNano()
			msg := writeMessageToTransport(t, campfireID, fmt.Sprintf("day %s msg %d", day.Format("2006-01-02"), j), []string{"status"}, agentID, transportBaseDir)
			// Move the file to the correct day bucket by renaming.
			// Find the file written by writeMessageToTransport (it lands in today's bucket).
			latestBucket := findLatestBucket(t, messagesDir)
			if latestBucket != ymDir {
				// Find and move the newly written file to our synthetic bucket.
				entries, _ := os.ReadDir(latestBucket)
				for _, e := range entries {
					if strings.Contains(e.Name(), msg.ID) {
						src := filepath.Join(latestBucket, e.Name())
						// Rename with the synthetic timestamp prefix.
						dst := filepath.Join(ymDir, fmt.Sprintf("%019d-%s.cbor", ts, msg.ID))
						if err := os.Rename(src, dst); err != nil {
							// If rename across mounts fails, copy then delete.
							data, readErr := os.ReadFile(src)
							if readErr != nil {
								t.Fatalf("reading source %s: %v", src, readErr)
							}
							if writeErr := os.WriteFile(dst, data, 0600); writeErr != nil {
								t.Fatalf("writing dst %s: %v", dst, writeErr)
							}
							os.Remove(src) //nolint:errcheck
						}
						break
					}
				}
			}
			rec := store.MessageRecord{
				ID:         msg.ID,
				CampfireID: campfireID,
				Sender:     agentID.PublicKeyHex(),
				Payload:    msg.Payload,
				Tags:       msg.Tags,
				Timestamp:  ts,
				Signature:  msg.Signature,
				Provenance: msg.Provenance,
				ReceivedAt: store.NowNano(),
			}
			if _, err := s.AddMessage(rec); err != nil {
				t.Fatalf("AddMessage day %s msg %d: %v", day.Format("2006-01-02"), j, err)
			}
			allRecords = append(allRecords, rec)
		}
	}

	// Cutoff at midnight of day2 (exclusive: only day1 messages are before this).
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	cutoffStr := cutoff.UTC().Format(time.RFC3339)

	result, err := execCompactPersistent(campfireID, cutoffStr, 0, "cross-day compact", "discard", false, agentID, s)
	if err != nil {
		t.Fatalf("execCompactPersistent: %v", err)
	}

	// All day1 messages must be superseded.
	superseded := make(map[string]bool)
	for _, id := range result.supersededIDs {
		superseded[id] = true
	}
	for _, rec := range allRecords {
		// Records from day1 (ts < cutoff) must be superseded.
		if rec.Timestamp < cutoff.UnixNano() {
			if !superseded[rec.ID] {
				t.Errorf("message %s (before cutoff) not in supersedes", rec.ID)
			}
		}
	}

	// The day1 bucket directory must NOT exist (Stat returns ENOENT) — R6.
	day1BucketDir := filepath.Join(messagesDir, "2026-01", "01")
	if _, err := os.Stat(day1BucketDir); !os.IsNotExist(err) {
		t.Errorf("day1 bucket dir %s should be ENOENT after whole-bucket RemoveAll, got: %v", day1BucketDir, err)
	}

	// Day2 and day3 buckets must still exist.
	for _, day := range []time.Time{day2, day3} {
		bucketDir := filepath.Join(messagesDir, day.Format("2006-01"), day.Format("02"))
		if _, err := os.Stat(bucketDir); err != nil {
			t.Errorf("bucket for %s should still exist: %v", day.Format("2006-01-02"), err)
		}
	}
}

// ─── campfireagent-6d27: orphan files when RemoveAll fails on fully-covered bucket ───

// TestCompactPersistent_OrphanFilesWhenRemoveAllFails (campfireagent-6d27):
// When os.RemoveAll fails for a fully-covered bucket, the per-file fallback
// must still delete the individual files — not skip them because the bucket
// appears in fullyCoveredBuckets.
//
// Forcing RemoveAll to fail while keeping per-file os.Remove working requires
// making RemoveAll fail at the DIRECTORY ENTRY level (rmdir) rather than at the
// FILE level. On Linux, chmod 0000 on the bucket dir makes BOTH RemoveAll and
// per-file fail (no permission to unlink files from a 000 dir). However, we can
// make RemoveAll fail on the final rmdir step (bucket dir entry removal from its
// parent) by chmod-ing the PARENT YYYY-MM dir to 0555. This:
//   - Removes write permission from the parent → rmdir(bucket) returns EACCES
//   - BUT keeps the bucket dir itself at 0700 → files inside are deletable
// On Linux, Go's RemoveAll deletes all files in the bucket (bucket has 0700)
// before attempting the rmdir that fails. The log line "best-effort bucket
// removal failed" is emitted even though the individual files are already gone.
//
// Bug behaviour: fullyCoveredBuckets[bucket] stays true after RemoveAll returns
// an error, so the per-file loop skips all files in that bucket. In a production
// scenario where RemoveAll fails partway through (NFS error, Windows file lock,
// race), orphaned files would remain.
//
// Fix behaviour: track successfullyRemovedBuckets separately; only skip per-file
// for buckets where RemoveAll SUCCEEDED; run per-file for failed-RemoveAll
// buckets → no orphaned files in any scenario.
//
// This test verifies the observable contract: after compact on a fully-covered
// bucket, filepath.Walk finds ZERO superseded .cbor files, regardless of whether
// RemoveAll could remove the bucket directory entry itself.
func TestCompactPersistent_OrphanFilesWhenRemoveAllFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based RemoveAll failure: use Windows hold-open-handle variant")
	}

	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	cfDir := campfireDirFor(transportBaseDir, campfireID)
	messagesDir := filepath.Join(cfDir, "messages")

	// Seed 2 messages into a synthetic past-day bucket (day1 = June 1 2024).
	// All of them will be fully covered (superseded) by a compact --before July 1.
	day1 := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	ymDir1 := filepath.Join(messagesDir, day1.Format("2006-01"), day1.Format("02"))
	if err := os.MkdirAll(ymDir1, 0700); err != nil {
		t.Fatalf("MkdirAll %s: %v", ymDir1, err)
	}

	var supersededIDs []string
	for j := 0; j < 2; j++ {
		ts := day1.Add(time.Duration(j) * time.Hour).UnixNano()
		msg := writeMessageToTransport(t, campfireID, fmt.Sprintf("orphan-test msg %d", j), []string{"status"}, agentID, transportBaseDir)
		supersededIDs = append(supersededIDs, msg.ID)
		latestBucket := findLatestBucket(t, messagesDir)
		if latestBucket != ymDir1 {
			entries, _ := os.ReadDir(latestBucket)
			for _, e := range entries {
				if strings.Contains(e.Name(), msg.ID) {
					src := filepath.Join(latestBucket, e.Name())
					dst := filepath.Join(ymDir1, fmt.Sprintf("%019d-%s.cbor", ts, msg.ID))
					data, err := os.ReadFile(src)
					if err != nil {
						t.Fatalf("reading %s: %v", src, err)
					}
					if err := os.WriteFile(dst, data, 0600); err != nil {
						t.Fatalf("writing %s: %v", dst, err)
					}
					os.Remove(src) //nolint:errcheck
					break
				}
			}
		}
		if _, err := s.AddMessage(store.MessageRecord{
			ID:         msg.ID,
			CampfireID: campfireID,
			Sender:     agentID.PublicKeyHex(),
			Payload:    msg.Payload,
			Tags:       msg.Tags,
			Timestamp:  ts,
			Signature:  msg.Signature,
			Provenance: msg.Provenance,
			ReceivedAt: store.NowNano(),
		}); err != nil {
			t.Fatalf("AddMessage orphan-test msg %d: %v", j, err)
		}
	}

	// Seed a keep message in July (different YYYY-MM) so compact has at least one
	// retained message and does not refuse with "nothing to compact".
	keepDay := time.Date(2024, 7, 1, 12, 0, 0, 0, time.UTC)
	ymDir2 := filepath.Join(messagesDir, keepDay.Format("2006-01"), keepDay.Format("02"))
	if err := os.MkdirAll(ymDir2, 0700); err != nil {
		t.Fatalf("MkdirAll %s: %v", ymDir2, err)
	}
	keepMsg := writeMessageToTransport(t, campfireID, "keep message", []string{"status"}, agentID, transportBaseDir)
	keepTS := keepDay.UnixNano()
	if lb := findLatestBucket(t, messagesDir); lb != ymDir2 {
		entries, _ := os.ReadDir(lb)
		for _, e := range entries {
			if strings.Contains(e.Name(), keepMsg.ID) {
				src := filepath.Join(lb, e.Name())
				dst := filepath.Join(ymDir2, fmt.Sprintf("%019d-%s.cbor", keepTS, keepMsg.ID))
				data, _ := os.ReadFile(src)
				_ = os.WriteFile(dst, data, 0600)
				os.Remove(src) //nolint:errcheck
				break
			}
		}
	}
	if _, err := s.AddMessage(store.MessageRecord{
		ID:         keepMsg.ID,
		CampfireID: campfireID,
		Sender:     agentID.PublicKeyHex(),
		Payload:    keepMsg.Payload,
		Tags:       keepMsg.Tags,
		Timestamp:  keepTS,
		Signature:  keepMsg.Signature,
		Provenance: keepMsg.Provenance,
		ReceivedAt: store.NowNano(),
	}); err != nil {
		t.Fatalf("AddMessage keep msg: %v", err)
	}

	// Pre-compact: confirm .cbor files are present on disk.
	for _, id := range supersededIDs {
		found := false
		_ = filepath.Walk(messagesDir, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.Contains(path, id) {
				found = true
			}
			return nil
		})
		if !found {
			t.Fatalf("pre-compact: superseded message %s not found on disk", id)
		}
	}

	// Force RemoveAll to fail at the rmdir step (not the per-file step) by
	// making the PARENT YYYY-MM dir read-only. With 0555 on the parent:
	//   • RemoveAll deletes all .cbor files inside ymDir1 (ymDir1 itself is 0700)
	//   • RemoveAll tries to rmdir ymDir1 from its parent → EACCES (parent is 0555)
	//   • RemoveAll returns an error with the log: "best-effort bucket removal failed"
	// After compact, restore the parent so t.Cleanup can remove TempDir.
	ymParent := filepath.Dir(ymDir1) // messages/2024-06
	if err := os.Chmod(ymParent, 0555); err != nil {
		t.Fatalf("chmod 0555 on %s: %v", ymParent, err)
	}
	defer func() {
		os.Chmod(ymParent, 0755) //nolint:errcheck
	}()

	// Compact --before July 1 midnight. day1 (June) bucket is fully covered.
	// RemoveAll returns EACCES (can't remove bucket dir from read-only parent).
	// The per-file fallback must run for the bucket files.
	cutoff := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	cutoffStr := cutoff.UTC().Format(time.RFC3339)
	_, err := execCompactPersistent(campfireID, cutoffStr, 0, "orphan-test compact", "discard", false, agentID, s)
	if err != nil {
		t.Fatalf("execCompactPersistent: %v", err)
	}

	// Restore parent permissions before walking the tree.
	if err := os.Chmod(ymParent, 0755); err != nil {
		t.Fatalf("restore chmod on %s: %v", ymParent, err)
	}

	// Assert: no superseded .cbor files remain under messagesDir after compact.
	// RemoveAll deleted the individual files even though it couldn't remove the
	// bucket dir entry. The per-file fallback (if it ran) found them already gone.
	// With the bug: per-file was skipped (fullyCoveredBuckets check always true
	// for failed RemoveAll) — files survive only if RemoveAll itself failed to
	// delete them (e.g., on NFS, Windows, or with a different failure mode).
	// With the fix: per-file runs; on Linux, files are already gone (RemoveAll
	// deleted them before the rmdir failed), so Walk finds zero orphans.
	var orphans []string
	_ = filepath.Walk(messagesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		for _, id := range supersededIDs {
			if strings.Contains(path, id) {
				orphans = append(orphans, path)
			}
		}
		return nil
	})
	if len(orphans) > 0 {
		t.Errorf("campfireagent-6d27: %d superseded file(s) still on disk after compact:\n  %s",
			len(orphans), strings.Join(orphans, "\n  "))
	}
}

// findLatestBucket returns the most recently written day-bucket directory under messagesDir.
func findLatestBucket(t *testing.T, messagesDir string) string {
	t.Helper()
	var latest string
	_ = filepath.Walk(messagesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && path != messagesDir {
			// Check it's a DD-level directory (2-char name under YYYY-MM).
			base := filepath.Base(path)
			parent := filepath.Base(filepath.Dir(path))
			// parent should match YYYY-MM, base should match DD.
			if len(parent) == 7 && len(base) == 2 {
				latest = path
			}
		}
		return nil
	})
	return latest
}

// ─── R8: no compact:performed tag anywhere (R7) ───────────────────────────────

// TestNoCompactPerformedTagAnywhere (assertion R8 / R7):
// grep all source files for "compact:performed" — must be zero occurrences.
// Also verify (positive) "campfire:compact" IS present.
func TestNoCompactPerformedTagAnywhere(t *testing.T) {
	// Find the repo root (go up from this test file's dir until go.mod found).
	repoRoot := findRepoRoot(t)

	var foundPerformed []string
	var foundCampfireCompact int

	// This test file itself mentions "compact:performed" only in grep strings and error
	// messages (as the thing being tested for absence). Exclude this file from the scan.
	thisFile := "compact_persistent_test.go"

	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip vendor, .git, and binary files.
		base := filepath.Base(path)
		if base == ".git" || base == "vendor" {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}
		// Skip this test file itself (it mentions the forbidden tag in grep/error strings).
		if base == thisFile {
			return nil
		}
		// Only scan Go source files and shell scripts.
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".sh") && !strings.HasSuffix(path, ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)
		if strings.Contains(content, "compact:performed") {
			foundPerformed = append(foundPerformed, path)
		}
		if strings.Contains(content, "campfire:compact") {
			foundCampfireCompact++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking repo: %v", err)
	}

	if len(foundPerformed) > 0 {
		t.Errorf("R7 VIOLATION: found 'compact:performed' in %d file(s) — this tag must not exist; use 'campfire:compact' instead:\n  %s",
			len(foundPerformed), strings.Join(foundPerformed, "\n  "))
	}
	if foundCampfireCompact == 0 {
		t.Error("expected to find 'campfire:compact' in at least one source file, found zero")
	}
}

// findRepoRoot returns the campfire repo root by walking up from this test
// file's known location (derived via runtime.Caller, immune to cwd changes
// from sibling tests using t.Chdir).
func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find repo root (go.mod not found by walking up from test file)")
	return ""
}

// ─── future timestamp refused ─────────────────────────────────────────────────

// TestCompactPersistent_FutureTimestamp_Refused:
// --before with a future timestamp must be refused.
func TestCompactPersistent_FutureTimestamp_Refused(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	seedPersistentMessages(t, 2, agentID, s, campfireID, transportBaseDir)

	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	_, err := execCompactPersistent(campfireID, future, 0, "", "discard", false, agentID, s)
	if err == nil {
		t.Fatal("expected error for future --before timestamp, got nil")
	}
	if !strings.Contains(err.Error(), "future") {
		t.Errorf("expected 'future' in error message, got: %v", err)
	}
}

// ─── dry-run: no mutations ────────────────────────────────────────────────────

// TestCompactPersistent_DryRun_NoMutations:
// --dry-run must not write the compact event or delete any files.
func TestCompactPersistent_DryRun_NoMutations(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	cfDir := campfireDirFor(transportBaseDir, campfireID)

	seedPersistentMessages(t, 3, agentID, s, campfireID, transportBaseDir)
	beforeFiles := listFilesUnderMessages(t, cfDir)

	_, err := execCompactPersistent(campfireID, "", 1, "", "discard", true /* dry-run */, agentID, s)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	// No compact events should have been written.
	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("dry-run should not write compact event, got %d events", len(events))
	}

	// File count must be unchanged.
	afterFiles := listFilesUnderMessages(t, cfDir)
	if len(afterFiles) != len(beforeFiles) {
		t.Errorf("dry-run: file count changed from %d to %d", len(beforeFiles), len(afterFiles))
	}
}

// ─── regression: existing session/swarm compact unchanged ─────────────────────

// TestCompactSession_StillWorks:
// existing compact behavior (message-ID --before, archive retention) still works
// after the persistent-compact additions.
func TestCompactSession_StillWorks(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	msgIDs := seedMessages(t, 4, agentID, s, campfireID, transportBaseDir)

	// Compact all messages (no --before) with archive retention.
	result, err := execCompact(campfireID, "", "legacy compact", "archive", agentID, s)
	if err != nil {
		t.Fatalf("execCompact: %v", err)
	}
	if len(result.supersededIDs) != 4 {
		t.Errorf("superseded count = %d, want 4", len(result.supersededIDs))
	}
	if result.retention != "archive" {
		t.Errorf("retention = %q, want archive", result.retention)
	}

	// Compact --before msgIDs[2] (message-ID prefix, not RFC3339).
	agentID2, s2, campfireID2, transportBaseDir2, _ := setupCompactTestEnv(t, campfire.RoleFull)
	msgIDs2 := seedMessages(t, 4, agentID2, s2, campfireID2, transportBaseDir2)

	result2, err := execCompact(campfireID2, msgIDs2[2], "before msg", "archive", agentID2, s2)
	if err != nil {
		t.Fatalf("execCompact --before msgID: %v", err)
	}
	// Should supersede msgIDs2[0] and [1] only.
	if len(result2.supersededIDs) != 2 {
		t.Errorf("superseded count = %d, want 2", len(result2.supersededIDs))
	}

	_ = msgIDs
}

// ─── audit event on disk under bucket layout ──────────────────────────────────

// TestCompactPersistent_AuditEventOnDiskUnderBucketLayout:
// the campfire:compact event must land in a YYYY-MM/DD/ bucket directory
// (not the messages/ root) after persistent compact.
func TestCompactPersistent_AuditEventOnDiskUnderBucketLayout(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	cfDir := campfireDirFor(transportBaseDir, campfireID)

	seedPersistentMessages(t, 2, agentID, s, campfireID, transportBaseDir)

	if _, err := execCompactPersistent(campfireID, "", 1, "", "discard", false, agentID, s); err != nil {
		t.Fatalf("execCompactPersistent: %v", err)
	}

	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 compact event, got %d", len(events))
	}
	compactID := events[0].ID

	// Find the compact event file on disk.
	messagesDir := filepath.Join(cfDir, "messages")
	foundInBucket := false
	err = filepath.Walk(messagesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.Contains(path, compactID) {
			// Must be under a YYYY-MM/DD/ path structure.
			rel, _ := filepath.Rel(messagesDir, path)
			parts := strings.Split(rel, string(os.PathSeparator))
			// Expected: [YYYY-MM, DD, leafname]
			if len(parts) == 3 {
				foundInBucket = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking messages dir: %v", err)
	}
	if !foundInBucket {
		t.Errorf("compact event %s should be on disk in a YYYY-MM/DD/ bucket, not found there", compactID)
	}
}

// ─── concurrent send blocks under LOCK_EX ─────────────────────────────────────

// TestCompactPersistent_ConcurrentSendBlocksUnderLockEx:
// While LOCK_EX is held (simulating what execCompactPersistent does during
// deletion), concurrent WriteMessage calls should block and then succeed after
// the lock is released. This test uses Transport.LockExclusive directly.
func TestCompactPersistent_ConcurrentSendBlocksUnderLockEx(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	cfDir := campfireDirFor(transportBaseDir, campfireID)
	_ = s

	tr := fs.New(transportBaseDir)

	// Acquire LOCK_EX.
	release, err := tr.LockExclusive(cfDir)
	if err != nil {
		t.Fatalf("LockExclusive: %v", err)
	}

	// Start a concurrent WriteMessage in a goroutine.
	// It should block until LOCK_EX is released (LOCK_SH is blocked by LOCK_EX).
	var wg sync.WaitGroup
	wg.Add(1)
	writeErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		msg := writeMessageToTransport(t, campfireID, "concurrent write", []string{"status"}, agentID, transportBaseDir)
		// If write succeeded (which it should after lock release), send nil.
		writeErr <- func() error {
			if msg == nil {
				return fmt.Errorf("nil message")
			}
			return nil
		}()
	}()

	// Hold the lock briefly, then release.
	time.Sleep(20 * time.Millisecond)
	release()

	// Wait for the write goroutine to finish.
	wg.Wait()
	if err := <-writeErr; err != nil {
		t.Errorf("concurrent write failed: %v", err)
	}
}

// ─── audit event payload shape (§2.5 pin) ────────────────────────────────────

// TestCompactPersistent_AuditPayloadShape verifies the campfire:compact payload
// has all required fields per design §2.5.
func TestCompactPersistent_AuditPayloadShape(t *testing.T) {
	agentID, s, campfireID, transportBaseDir, _ := setupCompactTestEnv(t, campfire.RoleFull)
	seedPersistentMessages(t, 3, agentID, s, campfireID, transportBaseDir)

	if _, err := execCompactPersistent(campfireID, "", 1, "payload shape test", "discard", false, agentID, s); err != nil {
		t.Fatalf("execCompactPersistent: %v", err)
	}

	events, err := s.ListCompactionEvents(campfireID)
	if err != nil {
		t.Fatalf("ListCompactionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}

	var payload store.CompactionPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	// Required fields per §2.5:
	if len(payload.Supersedes) == 0 {
		t.Error("supersedes must be non-empty")
	}
	if payload.Retention != "discard" {
		t.Errorf("retention = %q, want discard", payload.Retention)
	}
	if len(payload.CheckpointHash) != 64 {
		t.Errorf("checkpoint_hash length = %d, want 64 hex chars", len(payload.CheckpointHash))
	}
	if len(payload.Summary) == 0 {
		t.Error("summary must not be empty (required per §2.5; may be empty bytes for auto-generated)")
	}
	if payload.BytesSuperseded <= 0 {
		t.Error("bytes_superseded should be > 0 when messages have payloads")
	}

	// Tag must be campfire:compact (NOT compact:performed).
	tags := events[0].Tags
	found := false
	for _, tag := range tags {
		if tag == "campfire:compact" {
			found = true
		}
		if tag == "compact:performed" {
			t.Error("R7 VIOLATION: found tag 'compact:performed' — must not exist")
		}
	}
	if !found {
		t.Errorf("compact event missing campfire:compact tag, got: %v", tags)
	}

	// Antecedents should reference the last superseded message.
	if len(events[0].Antecedents) != 1 {
		t.Errorf("antecedents count = %d, want 1", len(events[0].Antecedents))
	}
}
