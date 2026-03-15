package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/3dl-dev/campfire/pkg/campfire"
	"github.com/3dl-dev/campfire/pkg/identity"
	"github.com/3dl-dev/campfire/pkg/message"
	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var joinCmd = &cobra.Command{
	Use:   "join <campfire-id>",
	Short: "Join a campfire",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID := args[0]

		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity (run 'cf init' first): %w", err)
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		transport := fs.New(fs.DefaultBaseDir())

		// Read campfire state to check join protocol
		state, err := transport.ReadState(campfireID)
		if err != nil {
			return fmt.Errorf("reading campfire state: %w", err)
		}

		// Check if already a member
		members, err := transport.ListMembers(campfireID)
		if err != nil {
			return fmt.Errorf("listing members: %w", err)
		}
		alreadyOnDisk := false
		var existingJoinedAt int64
		for _, m := range members {
			if fmt.Sprintf("%x", m.PublicKey) == agentID.PublicKeyHex() {
				alreadyOnDisk = true
				existingJoinedAt = m.JoinedAt
				break
			}
		}

		// Check if already in local store
		existingMembership, _ := s.GetMembership(campfireID)
		if existingMembership != nil {
			return fmt.Errorf("already a member of campfire %s", campfireID[:12])
		}

		now := time.Now().UnixNano()

		if alreadyOnDisk {
			// Pre-admitted (e.g., via DM or cf admit). Just register locally.
			now = existingJoinedAt
		} else {
			// Need to be admitted first
			switch state.JoinProtocol {
			case "open":
				// Immediately admitted
			case "invite-only":
				return fmt.Errorf("campfire %s is invite-only; ask a member to run 'cf admit %s %s'",
					campfireID[:12], campfireID[:12], agentID.PublicKeyHex())
			default:
				return fmt.Errorf("unknown join protocol: %s", state.JoinProtocol)
			}

			// Write member record to transport directory
			if err := transport.WriteMember(campfireID, campfire.MemberRecord{
				PublicKey: agentID.PublicKey,
				JoinedAt:  now,
			}); err != nil {
				return fmt.Errorf("writing member record: %w", err)
			}
		}

		// Write campfire:member-joined system message (only if newly admitted)
		if !alreadyOnDisk {
			sysMsg, err := message.NewMessage(
				state.PrivateKey, state.PublicKey,
				[]byte(fmt.Sprintf(`{"member":"%s","joined_at":%d}`, agentID.PublicKeyHex(), now)),
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
		}

		// Record membership in local store
		if err := s.AddMembership(store.Membership{
			CampfireID:   campfireID,
			TransportDir: transport.CampfireDir(campfireID),
			JoinProtocol: state.JoinProtocol,
			Role:         "member",
			JoinedAt:     now,
		}); err != nil {
			return fmt.Errorf("recording membership: %w", err)
		}

		if jsonOutput {
			out := map[string]string{
				"campfire_id": campfireID,
				"status":      "joined",
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("Joined campfire %s\n", campfireID[:12])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(joinCmd)
}

// campfireFromState reconstructs a Campfire for membership hash computation.
func campfireFromState(state *campfire.CampfireState, members []campfire.MemberRecord) *campfire.Campfire {
	cf := &campfire.Campfire{
		JoinProtocol:          state.JoinProtocol,
		ReceptionRequirements: state.ReceptionRequirements,
		CreatedAt:             state.CreatedAt,
	}
	for _, m := range members {
		cf.Members = append(cf.Members, campfire.Member{
			PublicKey: m.PublicKey,
			JoinedAt:  m.JoinedAt,
		})
	}
	return cf
}
