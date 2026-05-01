package primcmd

import (
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newDisbandCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disband <campfire-id>",
		Short: "Disband a campfire (creator only)",
		Long: `Disband the campfire identified by campfire-id.

Wraps protocol.Client.Disband().
Only the creator may disband. Removes the campfire directory from the
filesystem transport and all membership records from the store.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]

			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			if err := client.Disband(campfireID); err != nil {
				return fmt.Errorf("disband: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "disbanded: %s\n", campfireID)
			return nil
		},
	}
}
