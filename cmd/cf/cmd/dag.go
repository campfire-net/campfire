package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/spf13/cobra"
)

var dagAll bool

var dagCmd = &cobra.Command{
	Use:   "dag <campfire-id>",
	Short: "Show message DAG index (no payloads)",
	Args:  cobra.ExactArgs(1),
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

		resolved, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		// Sync messages (same as cf read).
		m, err := s.GetMembership(resolved)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m != nil {
			if isGitHubCampfire(m.TransportDir) {
				syncFromGitHub(resolved, m.TransportDir, s)
			} else if isPeerHTTPCampfire(m.TransportDir, resolved) {
				syncFromHTTPPeers(resolved, agentID, s)
			} else {
				syncFromFilesystem(resolved, m.TransportDir, s)
			}
		}

		// Query messages.
		var afterTS int64
		if !dagAll {
			afterTS, _ = s.GetReadCursor(resolved)
		}
		msgs, err := s.ListMessages(resolved, afterTS)
		if err != nil {
			return fmt.Errorf("listing messages: %w", err)
		}

		if jsonOutput {
			formatDAGJSON(msgs, os.Stdout)
		} else {
			formatDAGOutput(msgs, os.Stdout)
		}

		// Update read cursor (same behavior as cf read).
		if !dagAll && len(msgs) > 0 {
			var maxTS int64
			for _, msg := range msgs {
				if msg.Timestamp > maxTS {
					maxTS = msg.Timestamp
				}
			}
			s.SetReadCursor(resolved, maxTS) //nolint:errcheck
		}

		return nil
	},
}

// formatDAGLine formats a single message as a DAG index line.
func formatDAGLine(msg store.MessageRecord) string {
	// Short message ID (first 6 chars).
	idShort := msg.ID
	if len(idShort) > 6 {
		idShort = idShort[:6]
	}

	// Tags.
	var tags []string
	json.Unmarshal([]byte(msg.Tags), &tags) //nolint:errcheck
	tagStr := ""
	if len(tags) > 0 {
		tagStr = " [" + strings.Join(tags, ",") + "]"
	}

	// Sender.
	senderShort := msg.Sender
	if len(senderShort) > 6 {
		senderShort = senderShort[:6]
	}
	senderDisplay := "agent:" + senderShort
	if msg.Instance != "" {
		senderDisplay += " (" + msg.Instance + ")"
	}

	// Antecedents.
	var antecedents []string
	json.Unmarshal([]byte(msg.Antecedents), &antecedents) //nolint:errcheck
	antStr := "(root)"
	if len(antecedents) > 0 {
		shortAnts := make([]string, len(antecedents))
		for i, a := range antecedents {
			if len(a) > 6 {
				shortAnts[i] = a[:6]
			} else {
				shortAnts[i] = a
			}
		}
		antStr = strings.Join(shortAnts, ", ")
	}

	return fmt.Sprintf("%s%s %s → %s", idShort, tagStr, senderDisplay, antStr)
}

// formatDAGOutput writes the human-readable DAG index to w.
func formatDAGOutput(msgs []store.MessageRecord, w io.Writer) {
	if len(msgs) == 0 {
		fmt.Fprintln(w, "No messages.")
		return
	}
	for _, msg := range msgs {
		fmt.Fprintln(w, formatDAGLine(msg))
	}
}

// formatDAGJSON writes the JSON DAG index to w.
func formatDAGJSON(msgs []store.MessageRecord, w io.Writer) {
	type dagEntry struct {
		ID          string   `json:"id"`
		Tags        []string `json:"tags"`
		Sender      string   `json:"sender"`
		Instance    string   `json:"instance,omitempty"`
		Antecedents []string `json:"antecedents"`
	}

	entries := make([]dagEntry, 0, len(msgs))
	for _, msg := range msgs {
		var tags []string
		json.Unmarshal([]byte(msg.Tags), &tags) //nolint:errcheck
		if tags == nil {
			tags = []string{}
		}

		var antecedents []string
		json.Unmarshal([]byte(msg.Antecedents), &antecedents) //nolint:errcheck
		if antecedents == nil {
			antecedents = []string{}
		}

		entries = append(entries, dagEntry{
			ID:          msg.ID,
			Tags:        tags,
			Sender:      msg.Sender,
			Instance:    msg.Instance,
			Antecedents: antecedents,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(entries) //nolint:errcheck
}

func init() {
	dagCmd.Flags().BoolVar(&dagAll, "all", false, "show all messages (default: unread only)")
	rootCmd.AddCommand(dagCmd)
}
