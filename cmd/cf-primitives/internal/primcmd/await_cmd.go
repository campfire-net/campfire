package primcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newAwaitCmd() *cobra.Command {
	var timeout time.Duration
	var pollInterval time.Duration

	cmd := &cobra.Command{
		Use:   "await <campfire-id> <target-msg-id>",
		Short: "Block until a future message is fulfilled",
		Long: `Block until a message that fulfills target-msg-id is available.

Wraps protocol.Client.Await().
A fulfilling message carries the "fulfills" tag with target-msg-id in its antecedents.
Returns ErrAwaitTimeout when --timeout expires before fulfillment arrives.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]
			targetMsgID := args[1]

			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			msg, err := client.Await(context.Background(), protocol.AwaitRequest{
				CampfireID:   campfireID,
				TargetMsgID:  targetMsgID,
				Timeout:      timeout,
				PollInterval: pollInterval,
			})
			if err != nil {
				return fmt.Errorf("await: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(msg)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "fulfilled: %s\n", msg.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "sender: %s\n", msg.Sender)
			if len(msg.Payload) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "payload: %s\n", string(msg.Payload))
			}
			return nil
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", 0, "max wait duration (0 = no timeout)")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 0, "store poll interval (default: 2s)")
	return cmd
}
