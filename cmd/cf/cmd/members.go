package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var membersCmd = &cobra.Command{
	Use:   "members [campfire-id]",
	Short: "List members of a campfire",
	Args:  cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		var campfireID string
		if len(args) == 1 {
			campfireID, err = resolveCampfireID(args[0], s)
			if err != nil {
				return err
			}
		} else {
			id, _, ok := ProjectRoot()
			if !ok {
				return fmt.Errorf("campfire ID required: no .campfire/root found in this directory tree")
			}
			campfireID = id
		}

		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:12])
		}

		transport := fs.New(fs.DefaultBaseDir())
		members, err := transport.ListMembers(campfireID)
		if err != nil {
			return fmt.Errorf("listing members: %w", err)
		}

		if jsonOutput {
			type entry struct {
				PublicKey string `json:"public_key"`
				JoinedAt string `json:"joined_at"`
			}
			var entries []entry
			for _, mem := range members {
				entries = append(entries, entry{
					PublicKey: fmt.Sprintf("%x", mem.PublicKey),
					JoinedAt: time.Unix(0, mem.JoinedAt).Format(time.RFC3339),
				})
			}
			if entries == nil {
				entries = []entry{}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(entries)
		}

		if len(members) == 0 {
			fmt.Println("No members.")
			return nil
		}

		for _, mem := range members {
			idHex := fmt.Sprintf("%x", mem.PublicKey)
			short := idHex
			if len(short) > 12 {
				short = short[:12]
			}
			fmt.Printf("%s  joined %s\n", short, time.Unix(0, mem.JoinedAt).Format("2006-01-02 15:04:05"))
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(membersCmd)
}
