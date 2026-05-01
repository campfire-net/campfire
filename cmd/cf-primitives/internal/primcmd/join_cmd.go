package primcmd

import (
	"encoding/json"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newJoinCmd() *cobra.Command {
	var transportDir string
	var peerEndpoint string

	cmd := &cobra.Command{
		Use:   "join <campfire-id>",
		Short: "Join an existing campfire",
		Long: `Join an existing campfire identified by campfire-id.

Wraps protocol.Client.Join().

Transport selection:
  --dir <path>       filesystem transport directory
  --peer <endpoint>  P2P HTTP peer endpoint URL`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]

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

			result, err := client.Join(protocol.JoinRequest{
				CampfireID: campfireID,
				Transport:  transport,
			})
			if err != nil {
				return fmt.Errorf("join: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"campfire_id":   result.CampfireID,
					"join_protocol": result.JoinProtocol,
					"transport_dir": result.TransportDir,
				})
			}

			fmt.Fprintf(cmd.OutOrStdout(), "joined: %s\n", result.CampfireID)
			fmt.Fprintf(cmd.OutOrStdout(), "protocol: %s\n", result.JoinProtocol)
			return nil
		},
	}

	cmd.Flags().StringVar(&transportDir, "dir", "", "filesystem transport directory")
	cmd.Flags().StringVar(&peerEndpoint, "peer", "", "P2P HTTP peer endpoint URL")
	return cmd
}
