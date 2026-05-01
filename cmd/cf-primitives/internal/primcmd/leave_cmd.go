package primcmd

import (
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newLeaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "leave <campfire-id>",
		Short: "Leave a campfire",
		Long: `Leave the campfire identified by campfire-id.

Wraps protocol.Client.Leave().
Removes the membership record from the store and the member record from
the filesystem transport directory.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]

			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			if err := client.Leave(campfireID); err != nil {
				return fmt.Errorf("leave: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "left: %s\n", campfireID)
			return nil
		},
	}
}
