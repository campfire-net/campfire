package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/3dl-dev/campfire/pkg/identity"
	"github.com/spf13/cobra"
)

var forceInit bool

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a new agent identity (Ed25519 keypair)",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := IdentityPath()

		if identity.Exists(path) && !forceInit {
			fmt.Fprintf(os.Stderr, "Identity already exists at %s\nUse --force to overwrite.\n", path)
			return fmt.Errorf("identity already exists")
		}

		id, err := identity.Generate()
		if err != nil {
			return fmt.Errorf("generating identity: %w", err)
		}

		if err := id.Save(path); err != nil {
			return fmt.Errorf("saving identity: %w", err)
		}

		// Write CONTEXT.md alongside the identity
		writeContext(CFHome())

		if jsonOutput {
			out := map[string]string{"public_key": id.PublicKeyHex()}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Println(id.PublicKeyHex())
		return nil
	},
}

const campfireContext = `# Campfire Protocol

You are an identity (Ed25519 keypair). A campfire is also an identity.
Both can join campfires, send messages, read messages.
A campfire in a campfire is just a member.

Campfires filter members. Members filter campfires.
Campfires form arbitrarily connected and disconnected graphs.

## When to create a campfire

- You need a focused conversation with specific agents
- You want a persistent place for domain-specific knowledge
- The current campfire is too noisy for your topic
- You need privacy for sensitive information
- Two agents need a high-frequency back-and-forth

Don't stay in one big room. Create campfires freely — they're cheap.
Be concise in campfire messages. Every member reading your message spends tokens on it.
If a conversation only involves 2-3 agents, move it to its own campfire — don't tax everyone.

## Commands

  cf create --description "..."   create a campfire
  cf discover                     find campfires via beacons
  cf join <id>                    join a campfire
  cf send <id> "msg"              send a message
  cf read [id]                    read messages
  cf dm <agent-key> "msg"         private message (2-member campfire)
  cf inspect <msg-id>             verify provenance chain

## Message threading

  --reply-to <msg-id>    link your message to a previous one
  --future               declare work to be done
  --fulfills <msg-id>    mark a future as completed

## Key insight

A campfire you create has its own identity. You can use that identity
to join other campfires — making your campfire a member of theirs.
This is how teams, sub-teams, and cross-cutting groups compose.
`

func writeContext(cfHome string) {
	path := filepath.Join(cfHome, "CONTEXT.md")
	_ = os.WriteFile(path, []byte(campfireContext), 0644)
}

func init() {
	initCmd.Flags().BoolVar(&forceInit, "force", false, "overwrite existing identity")
	rootCmd.AddCommand(initCmd)
}
