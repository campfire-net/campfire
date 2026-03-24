package cmd

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var dmCmd = &cobra.Command{
	Use:   "dm <target-public-key-hex> <message>",
	Short: "Send a private message (creates/reuses a 2-member campfire)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		dmTags, _ := cmd.Flags().GetStringSlice("tag")
		targetHex := args[0]
		payload := args[1]

		targetKey, err := hex.DecodeString(targetHex)
		if err != nil {
			return fmt.Errorf("invalid public key hex: %w", err)
		}
		if len(targetKey) != 32 {
			return fmt.Errorf("public key must be 32 bytes (64 hex chars), got %d bytes", len(targetKey))
		}

		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
		}

		if targetHex == agentID.PublicKeyHex() {
			return fmt.Errorf("cannot DM yourself")
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		transport := fs.New(fs.DefaultBaseDir())

		// Look for existing DM campfire
		memberships, err := s.ListMemberships()
		if err != nil {
			return fmt.Errorf("listing memberships: %w", err)
		}
		// Find existing DM campfire: must be invite-only with exactly 2 members
		var existingCF string
		for _, mem := range memberships {
			if mem.JoinProtocol != "invite-only" {
				continue
			}
			members, err := transport.ListMembers(mem.CampfireID)
			if err != nil || len(members) != 2 {
				continue
			}
			for _, m := range members {
				if fmt.Sprintf("%x", m.PublicKey) == targetHex {
					existingCF = mem.CampfireID
					break
				}
			}
			if existingCF != "" {
				break
			}
		}

		var campfireID string

		if existingCF != "" {
			campfireID = existingCF
		} else {
			// Create a new DM campfire
			cf, err := campfire.New("invite-only", nil, 1)
			if err != nil {
				return fmt.Errorf("creating DM campfire: %w", err)
			}

			// Add both members
			cf.AddMember(agentID.PublicKey)
			cf.AddMember(targetKey)

			if err := transport.Init(cf); err != nil {
				return fmt.Errorf("initializing transport: %w", err)
			}

			now := time.Now().UnixNano()

			// Write both member records
			if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
				PublicKey: agentID.PublicKey,
				JoinedAt:  now,
			}); err != nil {
				return fmt.Errorf("writing sender member record: %w", err)
			}
			if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
				PublicKey: targetKey,
				JoinedAt:  now,
			}); err != nil {
				return fmt.Errorf("writing target member record: %w", err)
			}

			// Publish beacon
			b, err := beacon.New(
				cf.PublicKey, cf.PrivateKey,
				cf.JoinProtocol, cf.ReceptionRequirements,
				beacon.TransportConfig{
					Protocol: "filesystem",
					Config:   map[string]string{"dir": transport.CampfireDir(cf.PublicKeyHex())},
				},
				fmt.Sprintf("dm:%s:%s", agentID.PublicKeyHex()[:12], targetHex[:12]),
			)
			if err != nil {
				return fmt.Errorf("creating beacon: %w", err)
			}
			if err := beacon.Publish(BeaconDir(), b); err != nil {
				return fmt.Errorf("publishing beacon: %w", err)
			}

			// Record membership locally
			if err := s.AddMembership(store.Membership{
				CampfireID:   cf.PublicKeyHex(),
				TransportDir: transport.CampfireDir(cf.PublicKeyHex()),
				JoinProtocol: cf.JoinProtocol,
				Role:         store.PeerRoleCreator,
				JoinedAt:     now,
			}); err != nil {
				return fmt.Errorf("recording membership: %w", err)
			}

			campfireID = cf.PublicKeyHex()
		}

		// Send the message
		msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), dmTags, nil)
		if err != nil {
			return fmt.Errorf("creating message: %w", err)
		}

		state, err := transport.ReadState(campfireID)
		if err != nil {
			return fmt.Errorf("reading campfire state: %w", err)
		}

		members, err := transport.ListMembers(campfireID)
		if err != nil {
			return fmt.Errorf("listing members: %w", err)
		}

		cf := campfireFromState(state, members)
		if err := msg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(members),
			state.JoinProtocol, state.ReceptionRequirements,
			campfire.RoleFull,
		); err != nil {
			return fmt.Errorf("adding provenance hop: %w", err)
		}

		if err := transport.WriteMessage(campfireID, msg); err != nil {
			return fmt.Errorf("writing message: %w", err)
		}

		if jsonOutput {
			out := map[string]interface{}{
				"id":          msg.ID,
				"campfire_id": campfireID,
				"target":      targetHex,
				"payload":     payload,
				"reused":      existingCF != "",
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		if existingCF != "" {
			fmt.Println(msg.ID)
		} else {
			fmt.Printf("%s (new DM campfire: %s)\n", msg.ID, campfireID[:12])
		}
		return nil
	},
}

func init() {
	dmCmd.Flags().StringSlice("tag", nil, "message tags")
	rootCmd.AddCommand(dmCmd)
}
