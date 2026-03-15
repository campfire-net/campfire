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

var (
	sendTags        []string
	sendAntecedents []string
	sendFuture      bool
	sendFulfills    string
)

var sendCmd = &cobra.Command{
	Use:   "send <campfire-id> <message>",
	Short: "Send a message to a campfire",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID := args[0]
		payload := args[1]

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

		// Verify sender is a member in the transport directory
		members, err := transport.ListMembers(campfireID)
		if err != nil {
			return fmt.Errorf("listing members: %w", err)
		}
		isMember := false
		for _, mem := range members {
			if fmt.Sprintf("%x", mem.PublicKey) == agentID.PublicKeyHex() {
				isMember = true
				break
			}
		}
		if !isMember {
			return fmt.Errorf("not recognized as a member in the transport directory")
		}

		// Build tags
		tags := sendTags
		if sendFuture {
			tags = append(tags, "future")
		}
		if sendFulfills != "" {
			tags = append(tags, "fulfills")
		}

		// Build antecedents
		antecedents := sendAntecedents
		if sendFulfills != "" {
			antecedents = append(antecedents, sendFulfills)
		}

		// Create and sign message
		msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, antecedents)
		if err != nil {
			return fmt.Errorf("creating message: %w", err)
		}

		// Read campfire state for provenance hop
		state, err := transport.ReadState(campfireID)
		if err != nil {
			return fmt.Errorf("reading campfire state: %w", err)
		}

		// Add provenance hop
		cf := campfireFromState(state, members)
		if err := msg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(members),
			state.JoinProtocol, state.ReceptionRequirements,
		); err != nil {
			return fmt.Errorf("adding provenance hop: %w", err)
		}

		// Write to transport
		if err := transport.WriteMessage(campfireID, msg); err != nil {
			return fmt.Errorf("writing message: %w", err)
		}

		if jsonOutput {
			out := map[string]interface{}{
				"id":          msg.ID,
				"campfire_id": campfireID,
				"sender":      agentID.PublicKeyHex(),
				"payload":     payload,
				"tags":        msg.Tags,
				"antecedents": msg.Antecedents,
				"timestamp":   msg.Timestamp,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Println(msg.ID)
		return nil
	},
}

func init() {
	sendCmd.Flags().StringSliceVar(&sendTags, "tag", nil, "message tags")
	sendCmd.Flags().StringSliceVar(&sendAntecedents, "antecedent", nil, "antecedent message IDs")
	sendCmd.Flags().BoolVar(&sendFuture, "future", false, "tag this message as a future")
	sendCmd.Flags().StringVar(&sendFulfills, "fulfills", "", "message ID this fulfills (adds 'fulfills' tag + antecedent)")
	rootCmd.AddCommand(sendCmd)
}
