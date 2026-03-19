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
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var admitRole string

var admitCmd = &cobra.Command{
	Use:   "admit <campfire-id> <member-public-key-hex>",
	Short: "Admit a member to an invite-only campfire",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
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

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:12])
		}

		transportType := transport.ResolveType(*m)
		if transportType != transport.TypeFilesystem {
			return fmt.Errorf("admission not yet supported for %s transport — use the transport's native member management", transportType)
		}

		// Derive the fs transport base dir from the membership's TransportDir.
		// TransportDir is the campfire-specific subdirectory (e.g. /tmp/campfire/<id>),
		// so the base dir is its parent. Fall back to the default when empty.
		baseDir := fs.DefaultBaseDir()
		if m.TransportDir != "" {
			baseDir = filepath.Dir(m.TransportDir)
		}
		fsTransport := fs.New(baseDir)

		// Check if already a member
		members, err := fsTransport.ListMembers(campfireID)
		if err != nil {
			return fmt.Errorf("listing members: %w", err)
		}
		for _, existing := range members {
			if fmt.Sprintf("%x", existing.PublicKey) == memberKeyHex {
				return fmt.Errorf("agent %s is already a member", memberKeyHex[:12])
			}
		}

		state, err := fsTransport.ReadState(campfireID)
		if err != nil {
			return fmt.Errorf("reading campfire state: %w", err)
		}

		now := time.Now().UnixNano()

		// Validate role and default to "full".
		switch admitRole {
		case campfire.RoleObserver, campfire.RoleWriter, campfire.RoleFull, "":
			// valid
		default:
			return fmt.Errorf("invalid role %q: must be one of observer, writer, full", admitRole)
		}
		role := admitRole
		if role == "" {
			role = campfire.RoleFull
		}

		// Write member record
		if err := fsTransport.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: memberKey,
			JoinedAt:  now,
			Role:      role,
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

		updatedMembers, _ := fsTransport.ListMembers(campfireID)
		cf := campfireFromState(state, updatedMembers)
		if err := sysMsg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(updatedMembers),
			state.JoinProtocol, state.ReceptionRequirements,
		); err != nil {
			return fmt.Errorf("adding provenance hop: %w", err)
		}

		if err := fsTransport.WriteMessage(campfireID, sysMsg); err != nil {
			return fmt.Errorf("writing system message: %w", err)
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
	admitCmd.Flags().StringVar(&admitRole, "role", "", "membership role: observer, writer, or full (default: full)")
	rootCmd.AddCommand(admitCmd)
}
