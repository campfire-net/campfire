package cmd

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

var admitCmd = &cobra.Command{
	Use:   "admit <campfire-id> <member-public-key-hex>",
	Short: "Admit a member to an invite-only campfire",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		admitRole, _ := cmd.Flags().GetString("role")
		memberKeyHex := args[1]

		memberKey, err := hex.DecodeString(memberKeyHex)
		if err != nil {
			return fmt.Errorf("invalid public key hex: %w", err)
		}
		if len(memberKey) != 32 {
			return fmt.Errorf("public key must be 32 bytes (64 hex chars), got %d bytes", len(memberKey))
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		client := protocol.New(s, agentID)
		if err := client.Admit(protocol.AdmitRequest{
			CampfireID:      campfireID,
			MemberPubKeyHex: memberKeyHex,
			Role:            admitRole,
		}); err != nil {
			return err
		}

		role := admitRole
		if role == "" {
			role = "full"
		}

		if jsonOutput {
			out := map[string]string{
				"campfire_id": campfireID,
				"member":      memberKeyHex,
				"role":        role,
				"status":      "admitted",
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("Admitted %s to campfire %s (role: %s)\n", memberKeyHex[:12], campfireID[:12], role)
		return nil
	},
}

func init() {
	admitCmd.Flags().String("role", "", "membership role: observer, writer, or full (default: full)")
	rootCmd.AddCommand(admitCmd)
}
