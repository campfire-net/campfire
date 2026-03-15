package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List campfires this agent belongs to",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		memberships, err := s.ListMemberships()
		if err != nil {
			return fmt.Errorf("listing memberships: %w", err)
		}

		transport := fs.New(fs.DefaultBaseDir())

		if jsonOutput {
			type entry struct {
				CampfireID   string `json:"campfire_id"`
				JoinProtocol string `json:"join_protocol"`
				Role         string `json:"role"`
				MemberCount  int    `json:"member_count"`
				JoinedAt     string `json:"joined_at"`
			}
			var entries []entry
			for _, m := range memberships {
				members, _ := transport.ListMembers(m.CampfireID)
				entries = append(entries, entry{
					CampfireID:   m.CampfireID,
					JoinProtocol: m.JoinProtocol,
					Role:         m.Role,
					MemberCount:  len(members),
					JoinedAt:     time.Unix(0, m.JoinedAt).Format(time.RFC3339),
				})
			}
			if entries == nil {
				entries = []entry{}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(entries)
		}

		if len(memberships) == 0 {
			fmt.Println("No campfires. Use 'cf create' to create one or 'cf join' to join one.")
			return nil
		}

		for _, m := range memberships {
			members, _ := transport.ListMembers(m.CampfireID)
			idShort := m.CampfireID
			if len(idShort) > 12 {
				idShort = idShort[:12]
			}
			fmt.Printf("%s  %s  %d members  %s\n",
				idShort,
				m.JoinProtocol,
				len(members),
				m.Role,
			)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(lsCmd)
}
