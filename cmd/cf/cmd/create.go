package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/3dl-dev/campfire/pkg/beacon"
	"github.com/3dl-dev/campfire/pkg/campfire"
	"github.com/3dl-dev/campfire/pkg/identity"
	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var (
	createProtocol    string
	createRequire     []string
	createDescription string
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new campfire",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load agent identity
		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity (run 'cf init' first): %w", err)
		}

		// Create campfire
		cf, err := campfire.New(createProtocol, createRequire)
		if err != nil {
			return fmt.Errorf("creating campfire: %w", err)
		}

		// Add creator as first member
		cf.AddMember(agentID.PublicKey)

		// Set up filesystem transport
		transport := fs.New(fs.DefaultBaseDir())
		if err := transport.Init(cf); err != nil {
			return fmt.Errorf("initializing transport: %w", err)
		}

		// Write creator's member record
		if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
			PublicKey: agentID.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
		}); err != nil {
			return fmt.Errorf("writing member record: %w", err)
		}

		// Publish beacon
		beaconDir := BeaconDir()
		b, err := beacon.New(
			cf.Identity.PublicKey,
			cf.Identity.PrivateKey,
			cf.JoinProtocol,
			cf.ReceptionRequirements,
			beacon.TransportConfig{
				Protocol: "filesystem",
				Config:   map[string]string{"dir": transport.CampfireDir(cf.PublicKeyHex())},
			},
			createDescription,
		)
		if err != nil {
			return fmt.Errorf("creating beacon: %w", err)
		}
		if err := beacon.Publish(beaconDir, b); err != nil {
			return fmt.Errorf("publishing beacon: %w", err)
		}

		// Record membership in local store
		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		if err := s.AddMembership(store.Membership{
			CampfireID:   cf.PublicKeyHex(),
			TransportDir: transport.CampfireDir(cf.PublicKeyHex()),
			JoinProtocol: cf.JoinProtocol,
			Role:         "creator",
			JoinedAt:     store.NowNano(),
		}); err != nil {
			return fmt.Errorf("recording membership: %w", err)
		}

		if jsonOutput {
			out := map[string]interface{}{
				"campfire_id":           cf.PublicKeyHex(),
				"join_protocol":         cf.JoinProtocol,
				"reception_requirements": cf.ReceptionRequirements,
				"transport_dir":         transport.CampfireDir(cf.PublicKeyHex()),
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Println(cf.PublicKeyHex())
		return nil
	},
}

func init() {
	createCmd.Flags().StringVar(&createProtocol, "protocol", "open", "join protocol: open, invite-only")
	createCmd.Flags().StringSliceVar(&createRequire, "require", nil, "reception requirements (tags)")
	createCmd.Flags().StringVar(&createDescription, "description", "", "campfire description")
	rootCmd.AddCommand(createCmd)
}
