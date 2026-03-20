package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var swarmStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current swarm campfire state",
	Long: `Display the status of the root campfire for this project.

Shows: campfire ID, member count, message count, and the last few messages.
Returns an error if no active swarm (.campfire/root) exists.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID, _, ok := ProjectRoot()
		if !ok {
			return fmt.Errorf("no active swarm — no .campfire/root found")
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		membership, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if membership == nil {
			fmt.Printf("Swarm campfire: %s\n", campfireID[:12])
			fmt.Println("Status: not a member (campfire exists but this agent hasn't joined)")
			return nil
		}

		// Get messages.
		msgs, err := s.ListMessages(campfireID, 0)
		if err != nil {
			return fmt.Errorf("listing messages: %w", err)
		}

		// Get members from peer endpoints.
		peers, err := s.ListPeerEndpoints(campfireID)
		if err != nil {
			peers = nil
		}

		// Count members. For p2p-http campfires, the local agent's endpoint is
		// stored in peer_endpoints (so len(peers) already includes self). For
		// filesystem campfires, self is not stored there, so we add 1.
		selfInPeers := false
		for _, p := range peers {
			if p.MemberPubkey == agentID.PublicKeyHex() {
				selfInPeers = true
				break
			}
		}
		memberCount := len(peers)
		if !selfInPeers {
			memberCount++
		}

		// Print status.
		fmt.Printf("Swarm campfire: %s\n", campfireID[:12])
		fmt.Printf("Full ID:        %s\n", campfireID)
		fmt.Printf("Role:           %s\n", membership.Role)
		fmt.Printf("Transport:      %s\n", membership.TransportDir)
		fmt.Printf("Members:        %d\n", memberCount)
		fmt.Printf("Messages:       %d\n", len(msgs))

		// Show last N messages.
		statusN, _ := cmd.Flags().GetInt("last")
		if statusN <= 0 {
			statusN = 5
		}

		if len(msgs) > 0 {
			fmt.Println()
			start := len(msgs) - statusN
			if start < 0 {
				start = 0
			}
			for _, m := range msgs[start:] {
				sender := m.Sender
				if len(sender) > 12 {
					sender = sender[:12]
				}
				tagStr := strings.Join(m.Tags, ", ")
				if tagStr == "" {
					tagStr = "untagged"
				}
				payload := string(m.Payload)
				if len(payload) > 120 {
					payload = payload[:120] + "..."
				}
				fmt.Printf("  [%s] %s: %s\n", tagStr, sender, payload)
			}
		}

		return nil
	},
}

func init() {
	swarmStatusCmd.Flags().Int("last", 5, "number of recent messages to show")
	swarmCmd.AddCommand(swarmStatusCmd)
}
