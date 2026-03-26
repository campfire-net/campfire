package cmd

import (
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var rootSubCmd = &cobra.Command{
	Use:   "root",
	Short: "Manage operator root campfire",
	Long: `Manage the operator root campfire — a local directory and root-of-trust
for all campfires you operate.

  cf root init --name <org>   create your operator root campfire`,
}

var rootInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create your operator root campfire",
	Long: `Create an operator root campfire and register it in your local config.

The operator root is a campfire tagged directory, root-registry, and operator-root.
It is used as the parent namespace for campfires you operate.
A local alias is automatically created: cf://~<name>

Example:
  cf root init --name baron
  cf baron.ready list --status active`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required (e.g. --name baron)")
		}
		if err := naming.ValidateSegment(name); err != nil {
			return fmt.Errorf("invalid name %q: %w", name, err)
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		campfireID, err := ensureOperatorRoot(name, agentID.PublicKey, s)
		if err != nil {
			return err
		}

		// Save operator root config
		root := &naming.OperatorRoot{Name: name, CampfireID: campfireID}
		if err := naming.SaveOperatorRoot(CFHome(), root); err != nil {
			return fmt.Errorf("saving operator root config: %w", err)
		}

		// Create alias ~<name>
		aliases := naming.NewAliasStore(CFHome())
		if err := aliases.Set(name, campfireID); err != nil {
			return fmt.Errorf("creating alias: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "operator root: %s\nalias:         cf://~%s → %s\n",
			campfireID, name, campfireID[:12])
		return nil
	},
}

// EnsureOperatorRoot returns the existing operator root campfire ID, or creates one.
// Idempotent: if operator-root.json already exists, returns the stored ID without creating.
func EnsureOperatorRoot(name string, s store.Store) (string, error) {
	existing, err := naming.LoadOperatorRoot(CFHome())
	if err != nil {
		return "", err
	}
	if existing != nil {
		return existing.CampfireID, nil
	}

	agentID, err := loadIdentity()
	if err != nil {
		return "", err
	}
	return ensureOperatorRoot(name, agentID.PublicKey, s)
}

// ensureOperatorRoot creates the operator root campfire if it doesn't exist.
// Returns the campfire ID (new or existing from store membership).
func ensureOperatorRoot(name string, creatorKey []byte, s store.Store) (string, error) {
	// Check if already in operator-root.json
	existing, err := naming.LoadOperatorRoot(CFHome())
	if err != nil {
		return "", err
	}
	if existing != nil {
		return existing.CampfireID, nil
	}

	// Create campfire with operator-root tags
	cf, err := campfire.New("open", []string{"directory", "root-registry", "operator-root"}, 1)
	if err != nil {
		return "", fmt.Errorf("creating campfire: %w", err)
	}
	cf.AddMember(creatorKey)

	campfireID := cf.PublicKeyHex()
	baseDir := fs.DefaultBaseDir()
	transport := fs.New(baseDir)

	if err := transport.Init(cf); err != nil {
		return "", fmt.Errorf("initializing transport: %w", err)
	}
	if err := transport.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: creatorKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		return "", fmt.Errorf("writing member record: %w", err)
	}

	// Build and publish beacon
	b, err := beacon.New(
		cf.PublicKey, cf.PrivateKey,
		cf.JoinProtocol, cf.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "filesystem",
			Config:   map[string]string{"dir": transport.CampfireDir(campfireID)},
		},
		name+" operator root",
	)
	if err != nil {
		return "", fmt.Errorf("creating beacon: %w", err)
	}
	if err := beacon.Publish(BeaconDir(), b); err != nil {
		return "", fmt.Errorf("publishing beacon: %w", err)
	}

	// Record membership
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transport.CampfireDir(campfireID),
		JoinProtocol: cf.JoinProtocol,
		Role:         store.PeerRoleCreator,
		JoinedAt:     store.NowNano(),
		Threshold:    cf.Threshold,
		Description:  name + " operator root",
	}); err != nil {
		return "", fmt.Errorf("recording membership: %w", err)
	}

	return campfireID, nil
}

func init() {
	rootInitCmd.Flags().String("name", "", "operator/org name (e.g. baron, acme)")
	rootSubCmd.AddCommand(rootInitCmd)
	rootCmd.AddCommand(rootSubCmd)
}
