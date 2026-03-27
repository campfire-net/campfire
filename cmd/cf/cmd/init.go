package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [--name agent-name] [--session]",
	Short: "Generate a new agent identity (Ed25519 keypair)",
	Long: `Create a campfire identity.

  cf init                  persistent identity at ~/.campfire/
  cf init --session        temporary identity in a unique temp dir
  cf init --name worker-1  persistent named identity (survives across sessions)

Named identities live at ~/.campfire/agents/<name>/. Session identities print
the CF_HOME path on line 1 and the display name on line 2. The caller sets
CF_HOME to the printed path for subsequent commands.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		forceInit, _ := cmd.Flags().GetBool("force")
		initName, _ := cmd.Flags().GetString("name")
		initSession, _ := cmd.Flags().GetBool("session")
		// Session identity: temp dir, print path + display name, done.
		if initSession {
			tmpDir, err := os.MkdirTemp("", "cf-session-")
			if err != nil {
				return fmt.Errorf("creating temp dir: %w", err)
			}
			agentID, err := identity.Generate()
			if err != nil {
				return fmt.Errorf("generating identity: %w", err)
			}
			identityPath := filepath.Join(tmpDir, "identity.json")
			if err := agentID.Save(identityPath); err != nil {
				return fmt.Errorf("saving identity: %w", err)
			}
			writeContext(tmpDir)
			hexKey := agentID.PublicKeyHex()
			displayName := "agent:" + hexKey[:6]
			fmt.Println(tmpDir)
			fmt.Println(displayName)
			return nil
		}

		cfHome := CFHome()

		// Named identity: persistent agent
		if initName != "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("cannot determine home directory: %w", err)
			}
			cfHome = filepath.Join(home, ".campfire", "agents", initName)
		}

		// Check for root-owned ~/.campfire/ before attempting write
		if err := checkCampfireDirOwnership(); err != nil {
			return err
		}

		identityPath := filepath.Join(cfHome, "identity.json")

		if identity.Exists(identityPath) && !forceInit {
			agentID, err := identity.Load(identityPath)
			if err != nil {
				return fmt.Errorf("loading identity: %w", err)
			}
			if jsonOutput {
				out := map[string]string{"status": "exists", "public_key": agentID.PublicKeyHex(), "location": cfHome}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			fmt.Fprintf(os.Stderr, "Identity already exists at %s\n", cfHome)
			fmt.Println(agentID.PublicKeyHex())
			return nil
		}

		if err := os.MkdirAll(cfHome, 0700); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}

		agentID, err := identity.Generate()
		if err != nil {
			return fmt.Errorf("generating identity: %w", err)
		}

		if err := agentID.Save(identityPath); err != nil {
			return fmt.Errorf("saving identity: %w", err)
		}

		// Write CONTEXT.md alongside the identity
		writeContext(cfHome)

		// Create home campfire and seed it with convention declarations.
		homeCampfireID, err := createAndSeedHomeCampfire(cfHome, agentID)
		if err != nil {
			// Non-fatal: home campfire creation failure should not block init.
			fmt.Fprintf(os.Stderr, "warning: could not create home campfire: %v\n", err)
		}

		if jsonOutput {
			out := map[string]any{
				"status":     "created",
				"public_key": agentID.PublicKeyHex(),
				"location":   cfHome,
			}
			if homeCampfireID != "" {
				out["home_campfire_id"] = homeCampfireID
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		identityType := "session (disposable)"
		if initName != "" {
			identityType = fmt.Sprintf("persistent agent '%s'", initName)
		}

		fmt.Printf(`Identity created: %s
Type: %s
Location: %s

Next: cf discover    find campfires
      cf create      start one
      cf join <id>   join one
`, agentID.PublicKeyHex(), identityType, cfHome)
		return nil
	},
}

// createAndSeedHomeCampfire creates a home campfire for the agent, seeds it
// with the embedded promote declaration and any seed beacon declarations,
// publishes its beacon, and sets the "home" alias.
//
// Returns the campfire ID hex on success, or "" on failure.
func createAndSeedHomeCampfire(cfHome string, agentID *identity.Identity) (string, error) {
	// Create campfire keypair (invite-only, no requirements, threshold=1).
	// The home campfire is private by default — the owner invites members explicitly.
	homeCF, err := campfire.New("invite-only", nil, 1)
	if err != nil {
		return "", fmt.Errorf("creating home campfire: %w", err)
	}

	// Set up filesystem transport
	transport := fs.New(fs.DefaultBaseDir())
	if err := transport.Init(homeCF); err != nil {
		return "", fmt.Errorf("initializing transport: %w", err)
	}

	// Write agent as the only member
	if err := transport.WriteMember(homeCF.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		return "", fmt.Errorf("writing member record: %w", err)
	}

	// Seed: post embedded promote declaration + seed beacon declarations
	seedCampfireFilesystem(homeCF.PublicKeyHex(), transport.CampfireDir(homeCF.PublicKeyHex()), agentID, homeCF, "")

	// Build and publish beacon
	b, err := beacon.New(
		homeCF.PublicKey,
		homeCF.PrivateKey,
		homeCF.JoinProtocol,
		homeCF.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "filesystem",
			Config:   map[string]string{"dir": transport.CampfireDir(homeCF.PublicKeyHex())},
		},
		"home campfire",
	)
	if err != nil {
		return "", fmt.Errorf("creating beacon: %w", err)
	}
	if err := beacon.Publish(BeaconDir(), b); err != nil {
		// Non-fatal: beacon publishing failure doesn't block home campfire use
		fmt.Fprintf(os.Stderr, "warning: could not publish home campfire beacon: %v\n", err)
	}

	// Open store and record membership
	s, err := store.Open(store.StorePath(cfHome))
	if err != nil {
		return "", fmt.Errorf("opening store: %w", err)
	}
	defer s.Close()

	if err := s.AddMembership(store.Membership{
		CampfireID:   homeCF.PublicKeyHex(),
		TransportDir: transport.CampfireDir(homeCF.PublicKeyHex()),
		JoinProtocol: homeCF.JoinProtocol,
		Role:         store.PeerRoleCreator,
		JoinedAt:     store.NowNano(),
		Threshold:    homeCF.Threshold,
		Description:  "home campfire",
	}); err != nil {
		return "", fmt.Errorf("recording membership: %w", err)
	}

	// Set "home" alias
	aliases := naming.NewAliasStore(cfHome)
	if err := aliases.Set("home", homeCF.PublicKeyHex()); err != nil {
		// Non-fatal: alias failure doesn't block home campfire use
		fmt.Fprintf(os.Stderr, "warning: could not set home alias: %v\n", err)
	}

	return homeCF.PublicKeyHex(), nil
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

## Campfires are programmable

A campfire is not a chat room. It's a coordination primitive.

- Set reception requirements to enforce what members must acknowledge
- Use tags to create structured data flows, not just labels
- Use futures to declare work, fulfills to complete it — workflow without a workflow engine
- A campfire's identity is a key — scripts and automations can hold it
- Beacons are service advertisements — publish what you offer, discover what you need

## Think at four scales

a) Agentic society — campfires are communities with freedoms and accountability.
   Reception requirements are enforceable norms. Eviction is governance. Trust
   is built through provenance chains, not granted by admins.

b) Internet-scale engineering — millions of agents, different models, transports,
   trust levels. Directory campfires for discovery. Trust campfires for reputation.
   Filter campfires for shared signal optimization.

c) Wildfire adoption — design patterns that work recursively. A pattern that works
   for 5 agents should work for 5000. Don't build one-off solutions. Build templates
   that others will copy.

d) Cascade — how does adoption start? An agent learns about cf, creates a campfire,
   publishes a beacon. Another agent discovers it. The network grows peer-to-peer.
   No platform. No deployment. Just keys and campfires.

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
	initCmd.Flags().Bool("force", false, "overwrite existing identity")
	initCmd.Flags().String("name", "", "persistent agent name (survives across sessions)")
	initCmd.Flags().Bool("session", false, "create a temporary identity in a unique temp dir")
	rootCmd.AddCommand(initCmd)
}
