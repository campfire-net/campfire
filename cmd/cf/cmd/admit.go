package cmd

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/3dl-dev/campfire/pkg/campfire"
	"github.com/3dl-dev/campfire/pkg/message"
	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var admitCmd = &cobra.Command{
	Use:   "admit <campfire-id> <member-public-key-hex>",
	Short: "Admit a member to an invite-only campfire",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID := args[0]
		memberKeyHex := args[1]

		memberKey, err := hex.DecodeString(memberKeyHex)
		if err != nil {
			return fmt.Errorf("invalid public key hex: %w", err)
		}
		if len(memberKey) != 32 {
			return fmt.Errorf("public key must be 32 bytes (64 hex chars), got %d bytes", len(memberKey))
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:12])
		}

		transport := fs.New(fs.DefaultBaseDir())

		// Check if already a member
		members, err := transport.ListMembers(campfireID)
		if err != nil {
			return fmt.Errorf("listing members: %w", err)
		}
		for _, existing := range members {
			if fmt.Sprintf("%x", existing.PublicKey) == memberKeyHex {
				return fmt.Errorf("agent %s is already a member", memberKeyHex[:12])
			}
		}

		state, err := transport.ReadState(campfireID)
		if err != nil {
			return fmt.Errorf("reading campfire state: %w", err)
		}

		now := time.Now().UnixNano()

		// Write member record
		if err := transport.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: memberKey,
			JoinedAt:  now,
		}); err != nil {
			return fmt.Errorf("writing member record: %w", err)
		}

		// Write campfire:member-joined system message
		sysMsg, err := message.NewMessage(
			state.PrivateKey, state.PublicKey,
			[]byte(fmt.Sprintf(`{"member":"%s","joined_at":%d}`, memberKeyHex, now)),
			[]string{"campfire:member-joined"},
			nil,
		)
		if err != nil {
			return fmt.Errorf("creating system message: %w", err)
		}

		updatedMembers, _ := transport.ListMembers(campfireID)
		cf := campfireFromState(state, updatedMembers)
		if err := sysMsg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(updatedMembers),
			state.JoinProtocol, state.ReceptionRequirements,
		); err != nil {
			return fmt.Errorf("adding provenance hop: %w", err)
		}

		if err := transport.WriteMessage(campfireID, sysMsg); err != nil {
			return fmt.Errorf("writing system message: %w", err)
		}

		if jsonOutput {
			out := map[string]string{
				"campfire_id": campfireID,
				"member":      memberKeyHex,
				"status":      "admitted",
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("Admitted %s to campfire %s\n", memberKeyHex[:12], campfireID[:12])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(admitCmd)
}
