package primcmd

import (
	"encoding/json"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newMembersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "members <campfire-id>",
		Short: "List campfire members",
		Long: `List the current member set for campfire-id.

Wraps protocol.Client.Members().
Reads from the filesystem transport directory — does not touch the store.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]

			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			members, err := client.Members(campfireID)
			if err != nil {
				return fmt.Errorf("members: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(members)
			}

			for _, m := range members {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  role=%s\n", m.MemberPubkey, m.Role)
			}
			return nil
		},
	}
}
