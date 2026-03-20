package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/spf13/cobra"
)

var dagCmd = &cobra.Command{
	Use:   "dag <campfire-id>",
	Short: "Show message DAG index (no payloads)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dagAll, _ := cmd.Flags().GetBool("all")
		dagTagFilters, _ := cmd.Flags().GetStringArray("tag")
		dagSenderFilter, _ := cmd.Flags().GetString("sender")
		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
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
			syncCampfire(resolved, m, agentID, s)
		}

		// Query messages.
		var afterTS int64
		if !dagAll {
			afterTS, _ = s.GetReadCursor(resolved)
		}

		// Unfiltered fetch for cursor computation; SQL-filtered fetch for display.
		unfiltered, err := s.ListMessages(resolved, afterTS)
		if err != nil {
			return fmt.Errorf("listing messages: %w", err)
		}
		var preMaxTS int64
		for _, msg := range unfiltered {
			if msg.Timestamp > preMaxTS {
				preMaxTS = msg.Timestamp
			}
		}

		sqlFilter := store.MessageFilter{Tags: dagTagFilters, Sender: dagSenderFilter}
		msgs, err := s.ListMessages(resolved, afterTS, sqlFilter)
		if err != nil {
			return fmt.Errorf("listing messages (filtered): %w", err)
		}

		if jsonOutput {
			formatDAGJSON(msgs, os.Stdout)
		} else {
			formatDAGOutput(msgs, os.Stdout)
		}

		// Update read cursor from pre-filter timestamp.
		if !dagAll && preMaxTS > 0 {
			s.SetReadCursor(resolved, preMaxTS) //nolint:errcheck
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
	tagStr := ""
	if len(msg.Tags) > 0 {
		tagStr = " [" + strings.Join(msg.Tags, ",") + "]"
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
	antStr := "(root)"
	if len(msg.Antecedents) > 0 {
		shortAnts := make([]string, len(msg.Antecedents))
		for i, a := range msg.Antecedents {
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
		tags := msg.Tags
		if tags == nil {
			tags = []string{}
		}

		antecedents := msg.Antecedents
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
	dagCmd.Flags().Bool("all", false, "show all messages (default: unread only)")
	dagCmd.Flags().StringArray("tag", nil, "filter messages by tag (OR semantics, repeatable)")
	dagCmd.Flags().String("sender", "", "filter messages by sender hex prefix")
	rootCmd.AddCommand(dagCmd)
}
