package primcmd

import (
	"encoding/json"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newSendCmd() *cobra.Command {
	var tags []string
	var antecedents []string
	var instance string

	cmd := &cobra.Command{
		Use:   "send <campfire-id> <payload>",
		Short: "Send a signed message to a campfire",
		Long: `Send a signed message to the campfire identified by campfire-id.

Wraps protocol.Client.Send().
The caller must already be a member.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]
			payload := args[1]

			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			msg, err := client.Send(protocol.SendRequest{
				CampfireID:  campfireID,
				Payload:     []byte(payload),
				Tags:        tags,
				Antecedents: antecedents,
				Instance:    instance,
			})
			if err != nil {
				return fmt.Errorf("send: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"id":        msg.ID,
					"sender":    fmt.Sprintf("%x", msg.Sender),
					"timestamp": msg.Timestamp,
					"tags":      msg.Tags,
				})
			}

			fmt.Fprintf(cmd.OutOrStdout(), "sent: %s\n", msg.ID)
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&tags, "tag", nil, "message tags (repeatable)")
	cmd.Flags().StringSliceVar(&antecedents, "reply-to", nil, "antecedent message IDs (repeatable)")
	cmd.Flags().StringVar(&instance, "instance", "", "instance label (tainted, display-only)")
	return cmd
}
