package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

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
		} else if ctxID, ctxErr := resolveImplicitCampfire(); ctxErr != nil {
			return ctxErr
		} else if ctxID != "" {
			campfireID = ctxID
		} else {
			id, _, ok := ProjectRoot()
			if !ok {
				return fmt.Errorf("campfire ID required: no context set and no .campfire/root found in this directory tree")
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

		members, err := listMembersByTransport(*m, s)
		if err != nil {
			return fmt.Errorf("listing members: %w", err)
		}

		// Populate profile cache from stored identity:profile messages (best-effort).
		populateProfileCacheFromStore(s, campfireID)

		if jsonOutput {
			type entry struct {
				PublicKey   string `json:"public_key"`
				JoinedAt    string `json:"joined_at"`
				DisplayName string `json:"display_name,omitempty"`
			}
			var entries []entry
			for _, mem := range members {
				pubkeyHex := fmt.Sprintf("%x", mem.PublicKey)
				e := entry{
					PublicKey: pubkeyHex,
					JoinedAt:  time.Unix(0, mem.JoinedAt).Format(time.RFC3339),
				}
				if name := sessionProfileCache.Lookup(pubkeyHex); name != "" {
					e.DisplayName = name
				}
				entries = append(entries, e)
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
			if len(short) > 8 {
				short = short[:8]
			}
			role := mem.Role
			if role == "" {
				role = "full"
			}
			var line string
			if name := sessionProfileCache.Lookup(idHex); name != "" {
				// Display name is UNVERIFIED — show pubkey prefix alongside.
				line = fmt.Sprintf("%s (%s)", name, short)
			} else {
				line = short
			}
			fmt.Printf("%s  %s\n", line, role)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(membersCmd)
}
