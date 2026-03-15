package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/3dl-dev/campfire/pkg/identity"
	"github.com/3dl-dev/campfire/pkg/message"
	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var leaveCmd = &cobra.Command{
	Use:   "leave <campfire-id>",
	Short: "Leave a campfire",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID := args[0]

		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
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

		// Read campfire state for system message signing
		state, err := transport.ReadState(campfireID)
		if err != nil {
			return fmt.Errorf("reading campfire state: %w", err)
		}

		// Remove member record from transport directory
		if err := transport.RemoveMember(campfireID, agentID.PublicKey); err != nil {
			return fmt.Errorf("removing member record: %w", err)
		}

		// Write campfire:member-left system message
		sysMsg, err := message.NewMessage(
			state.PrivateKey, state.PublicKey,
			[]byte(fmt.Sprintf(`{"member":"%s"}`, agentID.PublicKeyHex())),
			[]string{"campfire:member-left"},
			nil,
		)
		if err != nil {
			return fmt.Errorf("creating system message: %w", err)
		}

		remainingMembers, _ := transport.ListMembers(campfireID)
		cf := campfireFromState(state, remainingMembers)
		if err := sysMsg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(remainingMembers),
			state.JoinProtocol, state.ReceptionRequirements,
		); err != nil {
			return fmt.Errorf("adding provenance hop: %w", err)
		}

		if err := transport.WriteMessage(campfireID, sysMsg); err != nil {
			return fmt.Errorf("writing system message: %w", err)
		}

		// Remove from local store
		if err := s.RemoveMembership(campfireID); err != nil {
			return fmt.Errorf("removing membership: %w", err)
		}

		if jsonOutput {
			out := map[string]string{
				"campfire_id": campfireID,
				"status":      "left",
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("Left campfire %s\n", campfireID[:12])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(leaveCmd)
}
