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

var (
	compactBefore  string
	compactSummary string
	compactRetain  string
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
		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
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
func execCompact(campfireID, beforeMsgID, summary, retention string, agentID *identity.Identity, s *store.Store) (*compactResult, error) {
	m, err := s.GetMembership(campfireID)
	if err != nil {
		return nil, fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("not a member of campfire %s", campfireID[:minInt(12, len(campfireID))])
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
		// Find the before message.
		var beforeTS int64
		for _, msg := range allMsgs {
			if msg.ID == beforeMsgID {
				beforeTS = msg.Timestamp
				break
			}
		}
		if beforeTS == 0 {
			return nil, fmt.Errorf("message not found: %s", beforeMsgID)
		}
		for _, msg := range allMsgs {
			if msg.Timestamp >= beforeTS || isCompactionMsg(msg) {
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
	switch transport.ResolveType(*m) {
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
	// (sendP2PHTTP already stores locally; sendFilesystem and sendGitHub do not — store here for all paths.)
	tagsJSON, _ := json.Marshal(sentMsg.Tags)
	anteJSON, _ := json.Marshal(sentMsg.Antecedents)
	provJSON, _ := json.Marshal(sentMsg.Provenance)
	s.AddMessage(store.MessageRecord{ //nolint:errcheck
		ID:          sentMsg.ID,
		CampfireID:  campfireID,
		Sender:      agentID.PublicKeyHex(),
		Payload:     sentMsg.Payload,
		Tags:        string(tagsJSON),
		Antecedents: string(anteJSON),
		Timestamp:   sentMsg.Timestamp,
		Signature:   sentMsg.Signature,
		Provenance:  string(provJSON),
		ReceivedAt:  store.NowNano(),
		Instance:    sentMsg.Instance,
	})

	return &compactResult{
		supersededIDs:  supersededIDs,
		checkpointHash: checkpointHash,
		retention:      retention,
	}, nil
}

// isCompactionMsg returns true if the message has the campfire:compact tag.
// Used to exclude existing compaction events from the set of messages to supersede.
func isCompactionMsg(m store.MessageRecord) bool {
	return strings.Contains(m.Tags, `"campfire:compact"`)
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
	compactCmd.Flags().StringVar(&compactBefore, "before", "", "compact messages before the given message ID prefix")
	compactCmd.Flags().StringVar(&compactSummary, "summary", "", "human-readable summary of compacted content")
	compactCmd.Flags().StringVar(&compactRetain, "retention", "archive", "retention policy: 'archive' or 'discard'")
	rootCmd.AddCommand(compactCmd)
}
