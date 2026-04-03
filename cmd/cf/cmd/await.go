package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
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

		// Verify membership before delegating to protocol.Client.
		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:min(12, len(campfireID))])
		}

		// Use transport-appropriate poll interval.
		interval := followIntervalForTransport(*m)

		// Set up a cancellable context driven by SIGINT/SIGTERM.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		stopCh := make(chan os.Signal, 1)
		signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(stopCh)
		go func() {
			select {
			case <-stopCh:
				cancel()
			case <-ctx.Done():
			}
		}()

		client := protocol.New(s, agentID)
		msg, err := client.Await(ctx, protocol.AwaitRequest{
			CampfireID:   campfireID,
			TargetMsgID:  targetMsgID,
			Timeout:      timeout,
			PollInterval: interval,
		})
		if errors.Is(err, protocol.ErrAwaitTimeout) || errors.Is(err, context.Canceled) {
			os.Exit(1)
			return nil
		}
		if err != nil {
			return err
		}
		return outputFulfillment(*msg)
	},
}

// outputFulfillment prints the fulfilling message and returns nil.
func outputFulfillment(msg protocol.Message) error {
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
