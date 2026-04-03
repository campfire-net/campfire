package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
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

		if jsonOutput {
			type entry struct {
				CampfireID   string `json:"campfire_id"`
				JoinProtocol string `json:"join_protocol"`
				Role         string `json:"role"`
				Threshold    uint   `json:"threshold"`
				MemberCount  int    `json:"member_count"`
				JoinedAt     string `json:"joined_at"`
				Description  string `json:"description"`
			}
			var entries []entry
			for _, m := range memberships {
				members, _ := fs.ForDir(m.TransportDir).ListMembers(m.CampfireID)
				threshold := m.Threshold
				if threshold == 0 {
					threshold = 1
				}
				entries = append(entries, entry{
					CampfireID:   m.CampfireID,
					JoinProtocol: m.JoinProtocol,
					Role:         m.Role,
					Threshold:    threshold,
					MemberCount:  len(members),
					JoinedAt:     time.Unix(0, m.JoinedAt).Format(time.RFC3339),
					Description:  m.Description,
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
			members, _ := fs.ForDir(m.TransportDir).ListMembers(m.CampfireID)
			idShort := m.CampfireID
			if len(idShort) > 12 {
				idShort = idShort[:12]
			}
			threshold := m.Threshold
			if threshold == 0 {
				threshold = 1
			}
			descSuffix := ""
			if m.Description != "" {
				descSuffix = "  " + m.Description
			}
			fmt.Printf("%s  %s  %d members  threshold=%d  %s%s\n",
				idShort,
				m.JoinProtocol,
				len(members),
				threshold,
				m.Role,
				descSuffix,
			)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(lsCmd)
}
