package primcmd

import (
	"encoding/json"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create or load agent identity and store",
		Long: `Initialize campfire identity and store at CF_HOME (or --cf-home).

Wraps protocol.Init / protocol.InitWithConfig.
If identity.json already exists it is loaded unchanged (idempotent).
If it does not exist a new Ed25519 keypair is generated and persisted.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, result, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("protocol.Init: %w", err)
			}
			defer client.Close()

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"identity_created": result.IdentityCreated,
					"identity_path":    result.IdentityPath,
					"store_path":       result.StorePath,
				})
			}

			if result.IdentityCreated {
				fmt.Fprintf(cmd.OutOrStdout(), "created identity: %s\n", result.IdentityPath)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "loaded identity: %s\n", result.IdentityPath)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "store: %s\n", result.StorePath)
			fmt.Fprintf(cmd.OutOrStdout(), "pubkey: %s\n", client.PublicKeyHex())
			return nil
		},
	}
}
