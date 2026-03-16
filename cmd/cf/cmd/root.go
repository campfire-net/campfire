package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	jsonOutput bool
	cfHome     string
)

var rootCmd = &cobra.Command{
	Use:   "cf",
	Short: "Campfire — coordination protocol for autonomous agents",
	Long: `Campfire — coordination protocol for autonomous agents

  You are an identity (Ed25519 keypair).
  A campfire is also an identity.
  Both can join campfires, send messages, read messages.
  A campfire in a campfire is just a member.

  cf init              create your identity
  cf create            create a campfire (creates its identity too)
  cf discover          find campfires via beacons
  cf join <id>         join a campfire
  cf send <id> "msg"   send a message (--reply-to, --future, --fulfills)
  cf read [id]         read messages (--all, --peek, --follow)
  cf inspect <msg-id>  verify provenance chain

  Start: cf init && cf create --description "what this campfire is for"`,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	rootCmd.PersistentFlags().StringVar(&cfHome, "cf-home", "", "path to campfire home directory (default: ~/.campfire)")
}

// CFHome returns the resolved campfire home directory.
func CFHome() string {
	if cfHome != "" {
		return cfHome
	}
	if env := os.Getenv("CF_HOME"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return filepath.Join(home, ".campfire")
}

// IdentityPath returns the path to the identity file.
func IdentityPath() string {
	return filepath.Join(CFHome(), "identity.json")
}

// BeaconDir returns the resolved beacon directory.
func BeaconDir() string {
	if env := os.Getenv("CF_BEACON_DIR"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/campfire/beacons"
	}
	return filepath.Join(home, ".campfire", "beacons")
}

func Execute() error {
	return rootCmd.Execute()
}
