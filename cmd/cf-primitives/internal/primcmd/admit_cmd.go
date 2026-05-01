package primcmd

import (
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newAdmitCmd() *cobra.Command {
	var role string

	cmd := &cobra.Command{
		Use:   "admit <campfire-id> <member-pubkey-hex>",
		Short: "Pre-admit a member to an invite-only campfire",
		Long: `Pre-admit a member to an invite-only campfire.

Wraps protocol.Client.Admit().
Writes a member record to the campfire transport so the target agent can join.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]
			memberPubKey := args[1]

			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			if err := client.Admit(protocol.AdmitRequest{
				CampfireID:      campfireID,
				MemberPubKeyHex: memberPubKey,
				Role:            role,
			}); err != nil {
				return fmt.Errorf("admit: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "admitted: %s to %s\n", memberPubKey[:12], campfireID)
			return nil
		},
	}

	cmd.Flags().StringVar(&role, "role", "", "role to assign (default: full)")
	return cmd
}
