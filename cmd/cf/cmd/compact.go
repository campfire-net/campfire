package cmd

// compact.go — cf compact command: create a campfire:compact compaction event.
//
// A compaction event is a regular campfire message with tag "campfire:compact" and
// a JSON payload describing which messages are superseded. Only "full" role members
// can send campfire:* system messages.
//
// Usage (existing — swarm/persistent, message-ID --before):
//   cf compact <campfire-id>                           compact all messages
//   cf compact <campfire-id> --before <msg-id>         compact before given message ID
//
// Usage (v0.31 — persistent campfires only, timestamp/count-based):
//   cf compact <campfire-id> --before <RFC3339-ts>     remove messages strictly before ts
//   cf compact <campfire-id> --keep-last <N>           retain only the last N messages
//   cf compact <campfire-id> --before <ts> --dry-run   print plan without mutating
//
// Design: campfire-agent docs/design/0.31-storage-scaling.md §2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/spf13/cobra"
)

// rfc3339RE detects an RFC3339 timestamp by its distinctive format:
// starts with a 4-digit year followed by a dash.
// This distinguishes it from a message ID (hex string starting with a letter or digit
// but never in YYYY- format).
var rfc3339RE = regexp.MustCompile(`^\d{4}-`)

// isRFC3339 reports whether s looks like an RFC3339 timestamp (not a message ID).
func isRFC3339(s string) bool {
	return rfc3339RE.MatchString(s)
}

var compactCmd = &cobra.Command{
	Use:   "compact <campfire-id>",
	Short: "Create a compaction event (campfire:compact) for a campfire",
	Long: `Create a campfire:compact message that marks older messages as superseded.

The compaction event is written as a regular campfire message with:
  - tag: campfire:compact
  - payload: JSON with supersedes, summary, retention, checkpoint_hash
  - antecedents: [ID of last superseded message]

Requires "full" membership role. Messages are never deleted — compaction is append-only.
After compaction, cf read excludes superseded messages by default. Use cf read --all to
see all messages including compacted ones.

For persistent campfires (filesystem transport), v0.31 adds two new selection modes:

  --before <RFC3339-ts>   Remove messages with timestamp strictly before the given time.
                          Must not be a future timestamp. Triggers retention=discard.

  --keep-last <N>         Retain only the last N messages; remove all older ones.
                          N=0 is refused (foot-gun prevention). Triggers retention=discard.

  --dry-run               Print the compaction plan without modifying any state.

When neither --before RFC3339 nor --keep-last is provided, the legacy behavior applies:
--before is treated as a message-ID prefix, and retention defaults to 'archive'.`,
	Args: cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		compactBefore, _ := cmd.Flags().GetString("before")
		compactSummary, _ := cmd.Flags().GetString("summary")
		compactRetain, _ := cmd.Flags().GetString("retention")
		keepLast, _ := cmd.Flags().GetInt("keep-last")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		var campfireID string
		if len(args) > 0 {
			campfireID, err = resolveCampfireID(args[0], s)
			if err != nil {
				return err
			}
		} else {
			campfireID, err = requireImplicitCampfire()
			if err != nil {
				return err
			}
		}

		// Determine whether this is a persistent compact (new v0.31 path) or the
		// legacy compact path (existing swarm/message-ID behavior).
		// Persistent compact is triggered by:
		//   (a) --before with an RFC3339 timestamp, OR
		//   (b) --keep-last > 0
		isPersistentMode := (compactBefore != "" && isRFC3339(compactBefore)) || keepLast > 0

		if isPersistentMode {
			// --- v0.31 persistent-campfire compact path ---
			if compactRetain != "archive" && compactRetain != "discard" && compactRetain != "" {
				return fmt.Errorf("--retention must be 'archive' or 'discard', got %q", compactRetain)
			}
			if compactRetain == "" {
				compactRetain = "discard" // default for persistent compact
			}

			if keepLast > 0 && compactBefore != "" {
				return fmt.Errorf("--before and --keep-last are mutually exclusive")
			}

			result, err := execCompactPersistent(campfireID, compactBefore, keepLast, compactSummary, compactRetain, dryRun, agentID, s)
			if err != nil {
				return err
			}

			if dryRun {
				fmt.Printf("[dry-run] Would compact %d messages (retention: %s)\n", len(result.supersededIDs), result.retention)
				fmt.Printf("[dry-run] Checkpoint hash: %s\n", result.checkpointHash)
				return nil
			}

			if jsonOutput {
				out := map[string]interface{}{
					"campfire_id":     campfireID,
					"superseded":      result.supersededIDs,
					"retention":       result.retention,
					"checkpoint_hash": result.checkpointHash,
					"message_count":   len(result.supersededIDs),
					"bytes_deleted":   result.bytesDeleted,
					"buckets_removed": result.bucketsRemoved,
				}
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Println(string(b))
			} else {
				fmt.Printf("Compacted %d messages (retention: %s)\n", len(result.supersededIDs), result.retention)
				fmt.Printf("Checkpoint hash: %s\n", result.checkpointHash)
				if result.bucketsRemoved > 0 {
					fmt.Printf("Removed %d whole-day bucket(s).\n", result.bucketsRemoved)
				}
			}
			return nil
		}

		// --- Legacy compact path (existing behavior, unchanged) ---
		if compactRetain != "archive" && compactRetain != "discard" {
			return fmt.Errorf("--retention must be 'archive' or 'discard', got %q", compactRetain)
		}

		result, err := execCompact(campfireID, compactBefore, compactSummary, compactRetain, agentID, s)
		if err != nil {
			return err
		}

		if jsonOutput {
			out := map[string]interface{}{
				"campfire_id":     campfireID,
				"superseded":      result.supersededIDs,
				"retention":       result.retention,
				"checkpoint_hash": result.checkpointHash,
				"message_count":   len(result.supersededIDs),
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Printf("Compacted %d messages (retention: %s)\n", len(result.supersededIDs), result.retention)
			fmt.Printf("Checkpoint hash: %s\n", result.checkpointHash)
		}

		return nil
	},
}

// compactResult holds the outcome of a compaction operation.
type compactResult struct {
	supersededIDs  []string
	checkpointHash string
	retention      string
}

// persistentCompactResult holds the outcome of a persistent compaction operation.
type persistentCompactResult struct {
	supersededIDs  []string
	checkpointHash string
	retention      string
	bytesDeleted   int64
	bucketsRemoved int
}

// execCompact is the core compaction logic, shared between the cobra command and tests.
// It builds the compaction payload, sends the campfire:compact message, and stores it locally.
// beforeMsgID is an exact message ID (not a prefix) or empty to compact all messages.
func execCompact(campfireID, beforeMsgID, summary, retention string, agentID *identity.Identity, s store.Store) (*compactResult, error) {
	m, err := s.GetMembership(campfireID)
	if err != nil {
		return nil, fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("not a member of campfire %s", campfireID[:min(12, len(campfireID))])
	}

	// Only "full" role members can compact (campfire:compact is a system tag).
	if err := checkRoleCanSend(m.Role, []string{campfire.TagCompact}); err != nil {
		return nil, err
	}

	// Collect all existing messages (uncompacted view — we compact raw messages).
	allMsgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}

	// Determine the set of messages to supersede.
	var toSupersede []store.MessageRecord
	if beforeMsgID != "" {
		// Find the before message by exact ID or unique prefix.
		var beforeTS int64
		var matchedID string
		for _, msg := range allMsgs {
			if msg.ID == beforeMsgID || strings.HasPrefix(msg.ID, beforeMsgID) {
				if matchedID != "" {
					return nil, fmt.Errorf("ambiguous message prefix %q matches multiple messages", beforeMsgID)
				}
				beforeTS = msg.Timestamp
				matchedID = msg.ID
			}
		}
		if matchedID == "" {
			return nil, fmt.Errorf("message not found: %s", beforeMsgID)
		}
		for _, msg := range allMsgs {
			// Exclude the before-message itself (by ID) and all strictly-later messages.
			// Do NOT use timestamp >= beforeTS: that also excludes messages with the
			// same nanosecond timestamp as the before-message that should be superseded
			// (timestamp collisions are common on fast machines and in tests).
			if msg.ID == matchedID || msg.Timestamp > beforeTS || isCompactionMsg(msg) {
				continue
			}
			toSupersede = append(toSupersede, msg)
		}
	} else {
		for _, msg := range allMsgs {
			if isCompactionMsg(msg) {
				continue
			}
			toSupersede = append(toSupersede, msg)
		}
	}

	if len(toSupersede) == 0 {
		return nil, fmt.Errorf("no messages to compact")
	}

	// Sort by timestamp ascending.
	sort.Slice(toSupersede, func(i, j int) bool {
		return toSupersede[i].Timestamp < toSupersede[j].Timestamp
	})
	lastSupersededID := toSupersede[len(toSupersede)-1].ID

	supersededIDs := make([]string, len(toSupersede))
	var bytesSuperseded int64
	for i, msg := range toSupersede {
		supersededIDs[i] = msg.ID
		bytesSuperseded += int64(len(msg.Payload))
	}

	checkpointHash := computeCheckpointHash(toSupersede)

	summaryBytes := []byte(summary)
	if len(summaryBytes) == 0 {
		summaryBytes = []byte(fmt.Sprintf("compacted %d messages", len(toSupersede)))
	}

	if retention == "" {
		retention = "archive"
	}

	payload := store.CompactionPayload{
		Supersedes:      supersededIDs,
		Summary:         summaryBytes,
		Retention:       retention,
		CheckpointHash:  checkpointHash,
		BytesSuperseded: bytesSuperseded,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding compaction payload: %w", err)
	}

	// Enforce role before sending.
	_ = campfire.EffectiveRole(m.Role) // already checked above

	// Route the compaction message through the appropriate transport via protocol.Client.Send.
	compactTags := []string{campfire.TagCompact}
	compactAntes := []string{lastSupersededID}
	payloadStr := string(payloadJSON)

	client := protocol.New(s, agentID)
	if _, err = client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte(payloadStr),
		Tags:        compactTags,
		Antecedents: compactAntes,
		Instance:    "compact",
	}); err != nil {
		return nil, fmt.Errorf("sending compaction event: %w", err)
	}

	return &compactResult{
		supersededIDs:  supersededIDs,
		checkpointHash: checkpointHash,
		retention:      retention,
	}, nil
}

// execCompactPersistent implements the v0.31 persistent-campfire compact path.
//
// It selects messages to supersede using either a RFC3339 --before timestamp or a
// --keep-last count, writes the campfire:compact audit event (NOT a new event type),
// and, when retention=discard, deletes on-disk files for superseded messages.
//
// Whole-day bucket deletion: when all files in a YYYY-MM/DD/ bucket are superseded,
// os.RemoveAll is used on the whole directory (R6). Partially-covered buckets get
// per-file deletes. The compact event itself lands in its own day bucket like any
// other message and is never listed in supersedes (R2, invariant by construction).
//
// Concurrency: LOCK_EX is acquired on <campfire-dir>/.migrate.lock before any
// filesystem deletion begins. WriteMessage holds LOCK_SH per call; it blocks
// until LOCK_EX is released (design §3.2).
//
// Parameters:
//   - beforeTS:   RFC3339 string (exclusive upper bound on message timestamp); empty if keepLast>0
//   - keepLast:   retain only the last N messages; 0 means "use beforeTS"
//   - summary:    human/agent readable description (may be empty)
//   - retention:  "discard" (delete on-disk files) or "archive" (keep files, log-only)
//   - dryRun:     if true, compute plan but do not mutate any state
func execCompactPersistent(
	campfireID, beforeTS string,
	keepLast int,
	summary, retention string,
	dryRun bool,
	agentID *identity.Identity,
	s store.Store,
) (*persistentCompactResult, error) {
	m, err := s.GetMembership(campfireID)
	if err != nil {
		return nil, fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("not a member of campfire %s", campfireID[:min(12, len(campfireID))])
	}

	// Persistent compact requires a filesystem transport.
	if m.TransportDir == "" {
		return nil, fmt.Errorf("campfire %s has no local filesystem transport; --before RFC3339 / --keep-last require a filesystem campfire", campfireID[:min(12, len(campfireID))])
	}

	// Only "full" role members can compact.
	if err := checkRoleCanSend(m.Role, []string{campfire.TagCompact}); err != nil {
		return nil, err
	}

	// Parse --before RFC3339 timestamp if provided.
	var cutoffNanos int64
	if beforeTS != "" {
		ts, err := time.Parse(time.RFC3339, beforeTS)
		if err != nil {
			return nil, fmt.Errorf("parsing --before timestamp %q: %w", beforeTS, err)
		}
		if ts.After(time.Now()) {
			return nil, fmt.Errorf("--before timestamp %q is in the future; refusing to compact (foot-gun prevention)", beforeTS)
		}
		cutoffNanos = ts.UnixNano()
	}

	// Collect all messages from the store (uncompacted view).
	allMsgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}

	// Filter out existing compaction events — they are never superseded (R2).
	var candidateMsgs []store.MessageRecord
	for _, msg := range allMsgs {
		if !isCompactionMsg(msg) {
			candidateMsgs = append(candidateMsgs, msg)
		}
	}

	// Sort candidates by timestamp ascending (total order).
	sort.Slice(candidateMsgs, func(i, j int) bool {
		return candidateMsgs[i].Timestamp < candidateMsgs[j].Timestamp
	})

	toSupersede, err := selectMessagesToSupersede(candidateMsgs, keepLast, cutoffNanos)
	if err != nil {
		return nil, err
	}

	// Build the campfire:compact payload per design §2.5.
	supersededIDs := make([]string, len(toSupersede))
	var bytesSuperseded int64
	for i, msg := range toSupersede {
		supersededIDs[i] = msg.ID
		bytesSuperseded += int64(len(msg.Payload))
	}

	checkpointHash := computeCheckpointHash(toSupersede)
	lastSupersededID := toSupersede[len(toSupersede)-1].ID

	summaryBytes := []byte(summary)
	if len(summaryBytes) == 0 {
		summaryBytes = []byte(fmt.Sprintf("compacted %d messages (retention=%s)", len(toSupersede), retention))
	}

	if dryRun {
		return &persistentCompactResult{
			supersededIDs:  supersededIDs,
			checkpointHash: checkpointHash,
			retention:      retention,
		}, nil
	}

	// --- Durably write the campfire:compact event FIRST (R2: audit event before deletes) ---
	if err := writeCompactAuditEvent(campfireID, supersededIDs, summaryBytes, retention, checkpointHash, bytesSuperseded, lastSupersededID, agentID, s); err != nil {
		return nil, err
	}

	// --- On-disk deletion (retention=discard only) ---
	if retention != "discard" {
		return &persistentCompactResult{
			supersededIDs:  supersededIDs,
			checkpointHash: checkpointHash,
			retention:      retention,
		}, nil
	}

	campfireDir := m.TransportDir
	tr := fs.ForDir(campfireDir)

	// Acquire LOCK_EX before any deletion (design §3.2, R6 concurrency).
	release, err := tr.LockExclusive(campfireDir)
	if err != nil {
		return nil, fmt.Errorf("acquiring exclusive lock for deletion: %w", err)
	}
	defer release()

	messagesDir := filepath.Join(campfireDir, "messages")

	// Build ID→file map by walking the on-disk bucketed layout once.
	idToFile, err := buildBucketIndex(messagesDir)
	if err != nil {
		return nil, err
	}

	// Classify buckets and delete files.
	fullyCoveredBuckets := classifyFullyCoveredBuckets(idToFile, supersededIDs)
	bucketsRemoved, bytesDeleted := deleteSupersededFiles(supersededIDs, idToFile, fullyCoveredBuckets)

	// Best-effort: prune empty parent month directories (§2.6).
	// Pass the full fullyCoveredBuckets set (not just successfully removed ones)
	// so we also check month dirs whose buckets were cleaned up via per-file deletes.
	pruneEmptyMonthDirs(messagesDir, fullyCoveredBuckets)

	return &persistentCompactResult{
		supersededIDs:  supersededIDs,
		checkpointHash: checkpointHash,
		retention:      retention,
		bytesDeleted:   bytesDeleted,
		bucketsRemoved: bucketsRemoved,
	}, nil
}

// selectMessagesToSupersede applies the keepLast or cutoffNanos selection policy
// to the sorted candidate message list and returns the slice to supersede.
// candidateMsgs must be sorted by timestamp ascending (total order).
// Returns an error if the selection would produce an empty set (R3).
func selectMessagesToSupersede(candidateMsgs []store.MessageRecord, keepLast int, cutoffNanos int64) ([]store.MessageRecord, error) {
	var toSupersede []store.MessageRecord
	if keepLast > 0 {
		// Keep the last keepLast messages; supersede all older ones.
		if len(candidateMsgs) <= keepLast {
			return nil, fmt.Errorf("only %d non-compact messages exist; --keep-last %d would leave nothing to compact (R3)", len(candidateMsgs), keepLast)
		}
		toSupersede = candidateMsgs[:len(candidateMsgs)-keepLast]
	} else {
		// --before RFC3339: supersede all messages with Timestamp < cutoffNanos.
		for _, msg := range candidateMsgs {
			if msg.Timestamp < cutoffNanos {
				toSupersede = append(toSupersede, msg)
			}
		}
	}
	if len(toSupersede) == 0 {
		return nil, fmt.Errorf("no messages selected for compaction (supersedes would be empty); refusing (R3)")
	}
	return toSupersede, nil
}

// writeCompactAuditEvent marshals and sends the campfire:compact audit message.
// This must be called BEFORE any on-disk deletion (R2: audit event before deletes).
func writeCompactAuditEvent(
	campfireID string,
	supersededIDs []string,
	summaryBytes []byte,
	retention, checkpointHash string,
	bytesSuperseded int64,
	lastSupersededID string,
	agentID *identity.Identity,
	s store.Store,
) error {
	compactPayload := store.CompactionPayload{
		Supersedes:      supersededIDs,
		Summary:         summaryBytes,
		Retention:       retention,
		CheckpointHash:  checkpointHash,
		BytesSuperseded: bytesSuperseded,
	}
	payloadJSON, err := json.Marshal(compactPayload)
	if err != nil {
		return fmt.Errorf("encoding compaction payload: %w", err)
	}
	client := protocol.New(s, agentID)
	if _, err = client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     payloadJSON,
		Tags:        []string{campfire.TagCompact},
		Antecedents: []string{lastSupersededID},
		Instance:    "compact",
	}); err != nil {
		return fmt.Errorf("sending compaction event: %w", err)
	}
	return nil
}

// onDiskFile records the bucket directory and full path for a single on-disk message file.
type onDiskFile struct {
	bucket string // full path to YYYY-MM/DD/ dir
	path   string // full path to leaf .cbor file
}

// buildBucketIndex walks the bucketed on-disk layout under messagesDir and returns
// a map from message ID to its on-disk file location.
//
// Layout: messages/YYYY-MM/DD/<19nanos>-<id>.cbor
// This is the single authoritative walk over the on-disk tree; classifyFullyCoveredBuckets
// and deleteSupersededFiles both consume its output.
func buildBucketIndex(messagesDir string) (map[string]onDiskFile, error) {
	idToFile := make(map[string]onDiskFile)

	yearMonthEntries, err := os.ReadDir(messagesDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading messages directory: %w", err)
	}
	for _, ymE := range yearMonthEntries {
		if !ymE.IsDir() {
			continue
		}
		ymDir := filepath.Join(messagesDir, ymE.Name())
		ddEntries, err := os.ReadDir(ymDir)
		if err != nil {
			continue
		}
		for _, ddE := range ddEntries {
			if !ddE.IsDir() {
				continue
			}
			ddDir := filepath.Join(ymDir, ddE.Name())
			leafEntries, err := os.ReadDir(ddDir)
			if err != nil {
				continue
			}
			for _, leafE := range leafEntries {
				leaf := leafE.Name()
				if !strings.HasSuffix(leaf, ".cbor") {
					continue
				}
				// Leaf format: <19nanos>-<id>.cbor
				// Extract message ID: everything after the first '-'.
				dashIdx := strings.Index(leaf, "-")
				if dashIdx < 0 || dashIdx == len(leaf)-1 {
					continue
				}
				msgID := strings.TrimSuffix(leaf[dashIdx+1:], ".cbor")
				idToFile[msgID] = onDiskFile{
					bucket: ddDir,
					path:   filepath.Join(ddDir, leaf),
				}
			}
		}
	}
	return idToFile, nil
}

// classifyFullyCoveredBuckets returns the set of YYYY-MM/DD bucket directories
// where every resident message ID is in supersededIDs.
// A bucket with zero superseded residents is NOT included (empty-bucket guard).
func classifyFullyCoveredBuckets(idToFile map[string]onDiskFile, supersededIDs []string) map[string]bool {
	supersededSet := make(map[string]bool, len(supersededIDs))
	for _, id := range supersededIDs {
		supersededSet[id] = true
	}

	// Build bucket → resident IDs map.
	bucketAllIDs := make(map[string]map[string]bool)
	for msgID, fi := range idToFile {
		if bucketAllIDs[fi.bucket] == nil {
			bucketAllIDs[fi.bucket] = make(map[string]bool)
		}
		bucketAllIDs[fi.bucket][msgID] = true
	}

	fullyCoveredBuckets := make(map[string]bool)
	for bucket, idsInBucket := range bucketAllIDs {
		allSuperseded := true
		for id := range idsInBucket {
			if !supersededSet[id] {
				allSuperseded = false
				break
			}
		}
		if !allSuperseded {
			continue
		}
		// Only mark as fully covered when at least one ID is superseded
		// (guards against empty buckets that happen to have no residents).
		for id := range idsInBucket {
			if supersededSet[id] {
				fullyCoveredBuckets[bucket] = true
				break
			}
		}
	}
	return fullyCoveredBuckets
}

// deleteSupersededFiles removes on-disk files for the superseded message IDs.
// Fully-covered buckets are removed with os.RemoveAll (R6).
// When RemoveAll fails, or for partially-covered buckets, individual files are
// deleted best-effort.
//
// The successfullyRemovedBuckets tracking ensures the per-file loop is entered
// for failed-RemoveAll buckets (campfireagent-6d27 regression fix).
//
// Returns (bucketsRemoved, bytesDeleted).
func deleteSupersededFiles(supersededIDs []string, idToFile map[string]onDiskFile, fullyCoveredBuckets map[string]bool) (bucketsRemoved int, bytesDeleted int64) {
	// Delete fully-covered buckets wholesale (R6).
	// Track which buckets were SUCCESSFULLY removed so the per-file fallback
	// below knows which ones still need individual file cleanup.
	successfullyRemovedBuckets := make(map[string]bool, len(fullyCoveredBuckets))
	for bucket := range fullyCoveredBuckets {
		if err := os.RemoveAll(bucket); err != nil {
			log.Printf("compact: best-effort bucket removal failed for %s: %v", bucket, err)
			// Do NOT mark as successfully removed; per-file deletes below will
			// handle remaining files in this bucket.
		} else {
			successfullyRemovedBuckets[bucket] = true
			bucketsRemoved++
		}
	}

	// Delete individual files in partially-covered buckets (best-effort).
	// Also handles files in fully-covered buckets where RemoveAll failed (campfireagent-6d27).
	for _, id := range supersededIDs {
		fi, found := idToFile[id]
		if !found {
			// File not found on disk — may have been deleted already or never written here.
			continue
		}
		if successfullyRemovedBuckets[fi.bucket] {
			// Bucket was fully removed via RemoveAll — individual files are gone.
			continue
		}
		info, err := os.Stat(fi.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // Already gone.
			}
			log.Printf("compact: stat %s: %v", fi.path, err)
			continue
		}
		bytesDeleted += info.Size()
		if err := os.Remove(fi.path); err != nil && !os.IsNotExist(err) {
			log.Printf("compact: best-effort file removal failed for %s: %v", fi.path, err)
		}
	}
	return bucketsRemoved, bytesDeleted
}

// pruneEmptyMonthDirs removes empty YYYY-MM parent directories after bucket deletions.
// This is a best-effort operation; errors are silently ignored.
func pruneEmptyMonthDirs(messagesDir string, removedBuckets map[string]bool) {
	// Collect unique YYYY-MM parent dirs of removed buckets.
	checked := make(map[string]bool)
	for bucket := range removedBuckets {
		ymDir := filepath.Dir(bucket) // YYYY-MM dir
		if checked[ymDir] {
			continue
		}
		checked[ymDir] = true

		entries, err := os.ReadDir(ymDir)
		if err != nil {
			continue // Best-effort.
		}
		if len(entries) == 0 {
			os.Remove(ymDir) //nolint:errcheck // best-effort
		}
	}
}

// isCompactionMsg returns true if the message has the campfire:compact tag.
// Used to exclude existing compaction events from the set of messages to supersede.
// Uses HasTag (exact element match) rather than strings.Contains to avoid false
// positives from tags like "xycampfire:compact". Unified with isCompactionEvent
// in store.go. (Fix for workspace-27q / workspace-2i1.)
func isCompactionMsg(m store.MessageRecord) bool {
	return store.HasTag(m.Tags, campfire.TagCompact)
}

// computeCheckpointHash computes SHA-256 of sorted(id + "|" + hex(signature)) for each message.
func computeCheckpointHash(msgs []store.MessageRecord) string {
	entries := make([]string, len(msgs))
	for i, m := range msgs {
		entries[i] = m.ID + "|" + hex.EncodeToString(m.Signature)
	}
	sort.Strings(entries)
	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func init() {
	compactCmd.Flags().String("before", "", "compact messages before the given message ID prefix (legacy) or RFC3339 timestamp (persistent campfires)")
	compactCmd.Flags().String("summary", "", "human-readable summary of compacted content")
	compactCmd.Flags().String("retention", "", "retention policy: 'archive' (default for legacy) or 'discard' (default for persistent)")
	compactCmd.Flags().Int("keep-last", 0, "retain only the last N messages; remove all older ones (persistent campfires only)")
	compactCmd.Flags().Bool("dry-run", false, "print compaction plan without modifying state (persistent campfires only)")
	rootCmd.AddCommand(compactCmd)
}
