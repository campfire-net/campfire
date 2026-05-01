package primcmd

import (
	"encoding/json"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newCreateCmd() *cobra.Command {
	var joinProtocol string
	var transportDir string
	var peerEndpoint string
	var description string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new campfire",
		Long: `Create a new campfire and admit the caller as the first member.

Wraps protocol.Client.Create().

Transport selection:
  --dir <path>       filesystem transport (default when --dir is set)
  --peer <endpoint>  P2P HTTP transport

Join protocol:
  --protocol open          anyone can join (default)
  --protocol invite-only   pre-admission required`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			var transport protocol.Transport
			switch {
			case peerEndpoint != "":
				transport = protocol.P2PHTTPTransport{PeerEndpoint: peerEndpoint}
			default:
				transport = protocol.FilesystemTransport{Dir: transportDir}
			}

			result, err := client.Create(protocol.CreateRequest{
				Description:  description,
				JoinProtocol: joinProtocol,
				Transport:    transport,
			})
			if err != nil {
				return fmt.Errorf("create: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"campfire_id":  result.CampfireID,
					"invite_code": result.InviteCode,
					"beacon_path": result.BeaconPath,
				})
			}

			fmt.Fprintf(cmd.OutOrStdout(), "campfire_id: %s\n", result.CampfireID)
			fmt.Fprintf(cmd.OutOrStdout(), "invite_code: %s\n", result.InviteCode)
			if result.BeaconPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "beacon_path: %s\n", result.BeaconPath)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&joinProtocol, "protocol", "open", "join protocol: open or invite-only")
	cmd.Flags().StringVar(&transportDir, "dir", "", "filesystem transport directory")
	cmd.Flags().StringVar(&peerEndpoint, "peer", "", "P2P HTTP peer endpoint URL")
	cmd.Flags().StringVar(&description, "description", "", "campfire description")
	return cmd
}
