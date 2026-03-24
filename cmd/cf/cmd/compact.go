package cmd

// compact.go — cf compact command: create a campfire:compact compaction event.
//
// A compaction event is a regular campfire message with tag "campfire:compact" and
// a JSON payload describing which messages are superseded. Only "full" role members
// can send campfire:* system messages.
//
// Usage:
//   cf compact <campfire-id>              compact all messages (up to the most recent)
//   cf compact <campfire-id> --before <msg-id>  compact messages before the given message

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport"
	"github.com/spf13/cobra"
)

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
see all messages including compacted ones.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		compactBefore, _ := cmd.Flags().GetString("before")
		compactSummary, _ := cmd.Flags().GetString("summary")
		compactRetain, _ := cmd.Flags().GetString("retention")
		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

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
	if err := checkRoleCanSend(m.Role, []string{"campfire:compact"}); err != nil {
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
	for i, msg := range toSupersede {
		supersededIDs[i] = msg.ID
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
		Supersedes:     supersededIDs,
		Summary:        summaryBytes,
		Retention:      retention,
		CheckpointHash: checkpointHash,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding compaction payload: %w", err)
	}

	// Enforce role before sending.
	_ = campfire.EffectiveRole(m.Role) // already checked above

	// Route the compaction message through the appropriate transport.
	compactTags := []string{"campfire:compact"}
	compactAntes := []string{lastSupersededID}
	payloadStr := string(payloadJSON)

	var sentMsg *message.Message
	transportType := transport.ResolveType(*m)
	switch transportType {
	case transport.TypeGitHub:
		sentMsg, err = sendGitHub(campfireID, payloadStr, compactTags, compactAntes, "compact", agentID, s, m)
	case transport.TypePeerHTTP:
		sentMsg, err = sendP2PHTTP(campfireID, payloadStr, compactTags, compactAntes, "compact", agentID, s, m)
	default: // TypeFilesystem
		sentMsg, err = sendFilesystem(campfireID, payloadStr, compactTags, compactAntes, "compact", agentID, m.TransportDir)
	}
	if err != nil {
		return nil, fmt.Errorf("sending compaction event: %w", err)
	}

	// Store the compaction event in the local SQLite store so ListCompactionEvents can find it.
	// sendP2PHTTP already calls s.AddMessage internally; skip it here to avoid a duplicate insert.
	if transportType != transport.TypePeerHTTP {
		s.AddMessage(store.MessageRecordFromMessage(campfireID, sentMsg, store.NowNano())) //nolint:errcheck
	}

	return &compactResult{
		supersededIDs:  supersededIDs,
		checkpointHash: checkpointHash,
		retention:      retention,
	}, nil
}

// isCompactionMsg returns true if the message has the campfire:compact tag.
// Used to exclude existing compaction events from the set of messages to supersede.
// Uses HasTag (exact element match) rather than strings.Contains to avoid false
// positives from tags like "xycampfire:compact". Unified with isCompactionEvent
// in store.go. (Fix for workspace-27q / workspace-2i1.)
func isCompactionMsg(m store.MessageRecord) bool {
	return store.HasTag(m.Tags, "campfire:compact")
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
	compactCmd.Flags().String("before", "", "compact messages before the given message ID prefix")
	compactCmd.Flags().String("summary", "", "human-readable summary of compacted content")
	compactCmd.Flags().String("retention", "archive", "retention policy: 'archive' or 'discard'")
	rootCmd.AddCommand(compactCmd)
}
