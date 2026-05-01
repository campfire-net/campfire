package primcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newSubscribeCmd() *cobra.Command {
	var tags []string
	var pollInterval time.Duration
	var afterTimestamp int64

	cmd := &cobra.Command{
		Use:   "subscribe <campfire-id>",
		Short: "Stream messages from a campfire (Ctrl-C to stop)",
		Long: `Subscribe to a campfire and stream messages as they arrive.

Wraps protocol.Client.Subscribe().
Polls the store at --poll-interval. Press Ctrl-C (or send SIGINT) to stop.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]

			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			sub := client.Subscribe(ctx, protocol.SubscribeRequest{
				CampfireID:     campfireID,
				Tags:           tags,
				PollInterval:   pollInterval,
				AfterTimestamp: afterTimestamp,
			})

			enc := json.NewEncoder(cmd.OutOrStdout())
			for msg := range sub.Messages() {
				if jsonOutput {
					if err := enc.Encode(msg); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s: %s\n",
						msg.ID[:12], msg.Sender[:12], string(msg.Payload))
				}
			}

			if serr := sub.Err(); serr != nil && serr != context.Canceled {
				return serr
			}
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter by tag (repeatable, OR logic)")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 500*time.Millisecond, "store poll interval")
	cmd.Flags().Int64Var(&afterTimestamp, "after", 0, "stream only messages with timestamp > this value")
	return cmd
}
