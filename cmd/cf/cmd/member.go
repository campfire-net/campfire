package cmd

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/transport"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var memberCmd = &cobra.Command{
	Use:   "member",
	Short: "Manage campfire members",
}

var memberSetRoleCmd = &cobra.Command{
	Use:   "set-role <campfire-id> <pubkey-hex>",
	Short: "Change the role of a campfire member",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireIDArg := args[0]
		targetPubkeyHex := args[1]

		newRole, _ := cmd.Flags().GetString("role")
		switch newRole {
		case campfire.RoleObserver, campfire.RoleWriter, campfire.RoleFull:
			// valid
		case "":
			return fmt.Errorf("--role is required: must be one of observer, writer, full")
		default:
			return fmt.Errorf("invalid role %q: must be one of observer, writer, full", newRole)
		}

		// Validate target pubkey.
		targetPubkeyBytes, err := hex.DecodeString(targetPubkeyHex)
		if err != nil {
			return fmt.Errorf("invalid public key hex: %w", err)
		}
		if len(targetPubkeyBytes) != 32 {
			return fmt.Errorf("public key must be 32 bytes (64 hex chars), got %d bytes", len(targetPubkeyBytes))
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		campfireID, err := resolveCampfireID(campfireIDArg, s)
		if err != nil {
			return err
		}

		// Verify caller is a member.
		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:12])
		}

		// Caller must have "full" role.
		if campfire.EffectiveRole(m.Role) != campfire.RoleFull {
			return fmt.Errorf("role change requires full membership (your role: %s)", m.Role)
		}

		// No self-role-change.
		if targetPubkeyHex == agentID.PublicKeyHex() {
			return fmt.Errorf("cannot change your own role")
		}

		// Route by transport type.
		transportType := transport.ResolveType(*m)
		if transportType != transport.TypeFilesystem {
			return fmt.Errorf("role change not yet supported for %s transport", transportType)
		}

		// Derive fs transport base dir from membership TransportDir.
		baseDir := fs.DefaultBaseDir()
		if m.TransportDir != "" {
			baseDir = filepath.Dir(m.TransportDir)
		}
		fsTransport := fs.New(baseDir)

		// Find target member in transport.
		members, err := fsTransport.ListMembers(campfireID)
		if err != nil {
			return fmt.Errorf("listing members: %w", err)
		}
		var targetMember *campfire.MemberRecord
		for i, mem := range members {
			if fmt.Sprintf("%x", mem.PublicKey) == targetPubkeyHex {
				targetMember = &members[i]
				break
			}
		}
		if targetMember == nil {
			return fmt.Errorf("member %s not found in campfire %s", targetPubkeyHex[:12], campfireID[:12])
		}

		previousRole := campfire.EffectiveRole(targetMember.Role)
		if previousRole == newRole {
			return fmt.Errorf("member %s already has role %s", targetPubkeyHex[:12], newRole)
		}

		// Update member record in transport (overwrite .cbor file).
		updatedRecord := campfire.MemberRecord{
			PublicKey: targetMember.PublicKey,
			JoinedAt:  targetMember.JoinedAt,
			Role:      newRole,
		}
		if err := fsTransport.WriteMember(campfireID, updatedRecord); err != nil {
			return fmt.Errorf("writing updated member record: %w", err)
		}

		// Emit campfire:member-role-changed system message.
		now := time.Now().UnixNano()
		state, err := fsTransport.ReadState(campfireID)
		if err != nil {
			return fmt.Errorf("reading campfire state: %w", err)
		}

		payload := fmt.Sprintf(
			`{"member":%q,"previous_role":%q,"new_role":%q,"changed_at":%d}`,
			targetPubkeyHex, previousRole, newRole, now,
		)
		sysMsg, err := message.NewMessage(
			state.PrivateKey, state.PublicKey,
			[]byte(payload),
			[]string{"campfire:member-role-changed"},
			nil,
		)
		if err != nil {
			return fmt.Errorf("creating system message: %w", err)
		}

		updatedMembers, _ := fsTransport.ListMembers(campfireID)
		cf := campfireFromState(state, updatedMembers)
		if err := sysMsg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(updatedMembers),
			state.JoinProtocol, state.ReceptionRequirements,
			campfire.RoleFull,
		); err != nil {
			return fmt.Errorf("adding provenance hop: %w", err)
		}

		if err := fsTransport.WriteMessage(campfireID, sysMsg); err != nil {
			return fmt.Errorf("writing system message: %w", err)
		}

		if jsonOutput {
			out := map[string]interface{}{
				"campfire_id":   campfireID,
				"member":        targetPubkeyHex,
				"previous_role": previousRole,
				"new_role":      newRole,
				"changed_at":    now,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("Changed role of %s in campfire %s: %s → %s\n",
			targetPubkeyHex[:12], campfireID[:12], previousRole, newRole)
		return nil
	},
}

func init() {
	memberSetRoleCmd.Flags().String("role", "", "new role: observer, writer, or full (required)")
	memberSetRoleCmd.MarkFlagRequired("role") //nolint:errcheck
	memberCmd.AddCommand(memberSetRoleCmd)
	rootCmd.AddCommand(memberCmd)
}
