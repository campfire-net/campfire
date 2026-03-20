package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var disbandCmd = &cobra.Command{
	Use:   "disband <campfire-id>",
	Short: "Disband a campfire (creator only)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID)
		}
		if m.Role != "creator" {
			return fmt.Errorf("only the creator can disband a campfire")
		}

		// Read the campfire state to get the public key bytes for beacon removal
		transport := fs.New(fs.DefaultBaseDir())
		state, err := transport.ReadState(campfireID)
		if err != nil {
			return fmt.Errorf("reading campfire state: %w", err)
		}

		// Remove beacon
		if err := beacon.Remove(BeaconDir(), state.PublicKey); err != nil {
			return fmt.Errorf("removing beacon: %w", err)
		}

		// Remove transport directory
		if err := transport.Remove(campfireID); err != nil {
			return fmt.Errorf("removing transport directory: %w", err)
		}

		// Remove from local store
		if err := s.RemoveMembership(campfireID); err != nil {
			return fmt.Errorf("removing membership: %w", err)
		}

		if jsonOutput {
			out := map[string]string{
				"campfire_id": campfireID,
				"status":      "disbanded",
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("Disbanded campfire %s\n", campfireID[:12])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(disbandCmd)
}
