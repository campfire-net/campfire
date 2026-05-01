package primcmd

import (
	"encoding/json"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newEvictCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "evict <campfire-id> <member-pubkey-hex>",
		Short: "Evict a member from a campfire",
		Long: `Evict the member identified by member-pubkey-hex from campfire-id.

Wraps protocol.Client.Evict().
For threshold>1 P2P HTTP campfires, re-runs DKG producing a new campfire ID.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]
			memberPubKey := args[1]

			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			result, err := client.Evict(protocol.EvictRequest{
				CampfireID:      campfireID,
				MemberPubKeyHex: memberPubKey,
			})
			if err != nil {
				return fmt.Errorf("evict: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"rekeyed":        result.Rekeyed,
					"new_campfire_id": result.NewCampfireID,
				})
			}

			fmt.Fprintf(cmd.OutOrStdout(), "evicted: %s from %s\n", memberPubKey[:12], campfireID)
			if result.Rekeyed {
				fmt.Fprintf(cmd.OutOrStdout(), "rekeyed: new campfire_id=%s\n", result.NewCampfireID)
			}
			return nil
		},
	}
}
