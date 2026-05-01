package primcmd

import (
	"encoding/json"
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

func newReadCmd() *cobra.Command {
	var tags []string
	var limit int
	var afterTimestamp int64
	var skipSync bool

	cmd := &cobra.Command{
		Use:   "read <campfire-id>",
		Short: "Read messages from a campfire",
		Long: `Read messages from the campfire identified by campfire-id.

Wraps protocol.Client.Read().
Syncs from the filesystem transport before querying (for filesystem campfires).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			campfireID := args[0]

			client, _, err := protocol.Init(cfHomeDir())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			defer client.Close()

			result, err := client.Read(protocol.ReadRequest{
				CampfireID:     campfireID,
				Tags:           tags,
				Limit:          limit,
				AfterTimestamp: afterTimestamp,
				SkipSync:       skipSync,
			})
			if err != nil {
				return fmt.Errorf("read: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(result.Messages)
			}

			for _, m := range result.Messages {
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s: %s\n", m.ID[:12], m.Sender[:12], string(m.Payload))
			}
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter by tag (repeatable, OR logic)")
	cmd.Flags().IntVar(&limit, "limit", 0, "max messages to return (0 = no limit)")
	cmd.Flags().Int64Var(&afterTimestamp, "after", 0, "return messages with timestamp > this value")
	cmd.Flags().BoolVar(&skipSync, "skip-sync", false, "skip filesystem sync before query")
	return cmd
}
