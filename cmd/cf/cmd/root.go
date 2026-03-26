package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags "-X ...cmd.Version=v1.2.3".
// Falls back to "dev" when built without ldflags (e.g. `go run`).
var Version = "dev"

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

  Campfires filter members. Members filter campfires.
  Campfires form arbitrarily connected and disconnected graphs.

  cf init              create your identity
  cf create            create a campfire (creates its identity too)
  cf discover          find campfires via beacons
  cf join <id>         join a campfire
  cf send <id> "msg"   send a message (--reply-to, --future, --fulfills)
  cf read [id]         read messages (--all, --peek, --follow)
  cf await <id> <msg>  block until a future message is fulfilled
  cf inspect <msg-id>  verify provenance chain

  Start: cf init && cf create --description "what this campfire is for"`,
	Version: Version,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	rootCmd.PersistentFlags().StringVar(&cfHome, "cf-home", "", "path to campfire home directory (default: ~/.campfire)")

	// Allow unknown flags at root level so convention dispatch can capture them.
	rootCmd.FParseErrWhitelist = cobra.FParseErrWhitelist{UnknownFlags: true}
	rootCmd.Args = cobra.ArbitraryArgs

	// Root RunE fires when args[0] is not a registered subcommand.
	// Interprets: cf <campfire> [operation] [--flags...]
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		campfireName := args[0]
		operationName := ""
		var flagArgs []string
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			operationName = args[1]
			flagArgs = args[2:]
		} else {
			flagArgs = args[1:]
		}
		return dispatchConventionOp(campfireName, operationName, flagArgs)
	}
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
	return beacon.DefaultBeaconDir()
}

// ProjectRoot walks up from cwd looking for a .campfire/root file.
// That file contains a single line: the full 64-char hex campfire ID.
// Returns (campfireID, projectDir, true) if found, ("", "", false) otherwise.
func ProjectRoot() (campfireID string, projectDir string, ok bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	for {
		rootFile := filepath.Join(dir, ".campfire", "root")
		data, err := os.ReadFile(rootFile)
		if err == nil {
			id := strings.TrimSpace(string(data))
			if len(id) == 64 {
				return id, dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", false
}

// ProjectDir returns the project directory containing .campfire/root,
// or ("", false) if not found.
func ProjectDir() (string, bool) {
	_, dir, ok := ProjectRoot()
	return dir, ok
}

func Execute() error {
	return rootCmd.Execute()
}
