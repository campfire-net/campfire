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

  Campfires declare typed operations (conventions) that agents call by name.
  After joining, the campfire's conventions are your API.

  cf init                              create your identity
  cf join <id>                         join a campfire
  cf <campfire> <operation> [--args]   call a convention operation
  cf <campfire>                        read messages (shorthand)

  Example:
    cf init
    cf join abc123...
    cf abc123 post --text "hello world"       # convention operation
    cf abc123 register --campfire_id def456   # another convention

  Convention operations handle validation, tag composition, rate limiting,
  and signing automatically. In MCP mode (cf-mcp), they appear as typed
  tools after campfire_join — call tools/list to see them.

  For primitives (send, read, create, discover, await, inspect), run:
    cf --help-primitives`,
	Version: Version,
}

var helpPrimitives bool

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	rootCmd.PersistentFlags().StringVar(&cfHome, "cf-home", "", "path to campfire home directory (default: ~/.campfire)")
	rootCmd.Flags().BoolVar(&helpPrimitives, "help-primitives", false, "show primitive commands (send, read, create, discover, await, inspect)")

	// Allow unknown flags at root level so convention dispatch can capture them.
	rootCmd.FParseErrWhitelist = cobra.FParseErrWhitelist{UnknownFlags: true}
	rootCmd.Args = cobra.ArbitraryArgs

	// Root RunE fires when args[0] is not a registered subcommand.
	// Interprets: cf <campfire> [operation] [--flags...]
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Check both the parsed flag and raw args (UnknownFlags whitelist
		// can cause --help-primitives to land in args instead of flags).
		for _, a := range args {
			if a == "--help-primitives" {
				helpPrimitives = true
			}
		}
		if helpPrimitives {
			fmt.Println(`Campfire primitives — low-level commands for when conventions don't cover your use case.

Most agents should use convention operations instead (cf <campfire> <operation>).

  cf init              create your identity (Ed25519 keypair)
  cf create            create a campfire
  cf join <id>         join a campfire (also discovers conventions)
  cf send <id> "msg"   send a raw message (--tags, --reply-to, --future, --fulfills)
  cf read [id]         read messages (--all, --peek, --follow)
  cf discover          find campfires via beacons
  cf await <id> <msg>  block until a future message is fulfilled
  cf inspect <msg-id>  verify provenance chain
  cf members <id>      list campfire members
  cf ls                list campfires you belong to

  cf convention lint <file>     validate a declaration
  cf convention test <dir>      test declarations locally
  cf convention promote <file>  publish to a registry campfire`)
			return nil
		}
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
		return dispatchConventionOp(cmd.Context(), campfireName, operationName, flagArgs)
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
