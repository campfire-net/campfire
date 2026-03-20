package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var swarmStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Create a root campfire for this project",
	Long: `Create a root campfire anchored to the current project directory.

Writes the campfire ID to .campfire/root and stages the file with git add.
Other agents can join this campfire via 'cf join <id>' or by reading .campfire/root.

If .campfire/root already exists, returns an error. Use 'cf swarm end' first
or join the existing campfire instead.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		swarmStartDescription, _ := cmd.Flags().GetString("description")
		// Find the project directory (cwd).
		projectDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting current directory: %w", err)
		}

		// Check if .campfire/root already exists.
		rootFile := filepath.Join(projectDir, ".campfire", "root")
		if _, err := os.Stat(rootFile); err == nil {
			return fmt.Errorf("project already has a root campfire — use `cf swarm end` first or join the existing one")
		}

		// Load agent identity.
		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity (run 'cf init' first): %w", err)
		}

		// Create the campfire.
		cf, err := campfire.New("open", nil, 1)
		if err != nil {
			return fmt.Errorf("creating campfire: %w", err)
		}
		cf.AddMember(agentID.PublicKey)

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		// Set up filesystem transport.
		transport := fs.New(fs.DefaultBaseDir())
		if err := transport.Init(cf); err != nil {
			return fmt.Errorf("initializing transport: %w", err)
		}

		// Write creator's member record.
		if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
			PublicKey: agentID.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
		}); err != nil {
			return fmt.Errorf("writing member record: %w", err)
		}

		// Publish beacon.
		b, err := beacon.New(
			cf.PublicKey,
			cf.PrivateKey,
			cf.JoinProtocol,
			cf.ReceptionRequirements,
			beacon.TransportConfig{
				Protocol: "filesystem",
				Config:   map[string]string{"dir": transport.CampfireDir(cf.PublicKeyHex())},
			},
			swarmStartDescription,
		)
		if err != nil {
			return fmt.Errorf("creating beacon: %w", err)
		}
		if err := beacon.Publish(BeaconDir(), b); err != nil {
			return fmt.Errorf("publishing beacon: %w", err)
		}

		// Record membership in local store.
		if err := s.AddMembership(store.Membership{
			CampfireID:   cf.PublicKeyHex(),
			TransportDir: transport.CampfireDir(cf.PublicKeyHex()),
			JoinProtocol: cf.JoinProtocol,
			Role:         "creator",
			JoinedAt:     store.NowNano(),
			Threshold:    cf.Threshold,
		}); err != nil {
			return fmt.Errorf("recording membership: %w", err)
		}

		campfireID := cf.PublicKeyHex()

		// Write .campfire/root.
		campfireDir := filepath.Join(projectDir, ".campfire")
		if err := os.MkdirAll(campfireDir, 0755); err != nil {
			return fmt.Errorf("creating .campfire directory: %w", err)
		}
		if err := os.WriteFile(rootFile, []byte(campfireID+"\n"), 0644); err != nil {
			return fmt.Errorf("writing .campfire/root: %w", err)
		}

		// Stage the file with git add (best-effort; not an error if not in a git repo).
		gitCmd := exec.Command("git", "add", ".campfire/root")
		gitCmd.Dir = projectDir
		_ = gitCmd.Run()

		fmt.Println(campfireID)
		return nil
	},
}

func init() {
	swarmStartCmd.Flags().String("description", "", "description for the root campfire")
	swarmCmd.AddCommand(swarmStartCmd)
}
