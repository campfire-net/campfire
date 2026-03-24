package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/spf13/cobra"
)

var awaitCmd = &cobra.Command{
	Use:   "await <campfire-id> <msg-id>",
	Short: "Block until a future message is fulfilled",
	Long: `Block until someone posts a message that fulfills the given message ID.

Returns the fulfilling message and exits 0. If --timeout expires, exits 1
with no output. Useful for in-session escalation: post a --future message,
then await its fulfillment without losing context.

Example:
  # Worker posts an escalation and waits for the decision
  msg_id=$(cf send "$campfire" --tag escalation --future "Need ruling on X" --json | jq -r .id)
  decision=$(cf await "$campfire" "$msg_id" --timeout 10m --json)`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		timeoutStr, _ := cmd.Flags().GetString("timeout")

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}
		targetMsgID := args[1]

		// Parse timeout. Zero means wait forever.
		var timeout time.Duration
		if timeoutStr != "" {
			timeout, err = time.ParseDuration(timeoutStr)
			if err != nil {
				return fmt.Errorf("invalid timeout: %w", err)
			}
		}

		// Look up the membership for polling.
		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:min(12, len(campfireID))])
		}

		entry := campfireEntry{id: campfireID, membership: m}
		interval := followIntervalForTransport(*m)

		// Set up signal handling and timeout.
		stopCh := make(chan os.Signal, 1)
		signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(stopCh)

		var deadline <-chan time.Time
		if timeout > 0 {
			timer := time.NewTimer(timeout)
			defer timer.Stop()
			deadline = timer.C
		}

		// Check existing messages first (the fulfillment may already exist).
		syncCampfire(entry.id, entry.membership, agentID, s)
		if msg, err := findFulfillment(s, campfireID, targetMsgID); err != nil {
			return err
		} else if msg != nil {
			return outputFulfillment(*msg)
		}

		// Poll loop.
		for {
			select {
			case <-stopCh:
				os.Exit(1)
				return nil
			case <-deadline:
				os.Exit(1)
				return nil
			case <-time.After(interval):
			}

			syncCampfire(entry.id, entry.membership, agentID, s)

			if msg, err := findFulfillment(s, campfireID, targetMsgID); err != nil {
				return err
			} else if msg != nil {
				return outputFulfillment(*msg)
			}
		}
	},
}

// findFulfillment searches for a message with the "fulfills" tag whose antecedents
// contain the target message ID. Returns nil if no fulfillment found.
func findFulfillment(s store.Store, campfireID, targetMsgID string) (*store.MessageRecord, error) {
	// Query all messages with the "fulfills" tag.
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"fulfills"},
	})
	if err != nil {
		return nil, fmt.Errorf("querying messages: %w", err)
	}

	for _, msg := range msgs {
		for _, ant := range msg.Antecedents {
			if ant == targetMsgID {
				return &msg, nil
			}
		}
	}
	return nil, nil
}

// outputFulfillment prints the fulfilling message and returns nil.
func outputFulfillment(msg store.MessageRecord) error {
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(msg)
	}
	fmt.Printf("[%s] %s\n", msg.ID[:12], string(msg.Payload))
	return nil
}

func init() {
	awaitCmd.Flags().String("timeout", "", "maximum time to wait (e.g. 30s, 5m, 1h)")
	rootCmd.AddCommand(awaitCmd)
}
