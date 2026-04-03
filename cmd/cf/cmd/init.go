package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	bip39 "github.com/tyler-smith/go-bip39"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [--name agent-name] [--session]",
	Short: "Generate a new agent identity (Ed25519 keypair)",
	Long: `Create a campfire identity.

  cf init                  persistent identity at ~/.campfire/
  cf init --session        temporary identity in a unique temp dir
  cf init --name worker-1  persistent named identity (survives across sessions)
  cf init --durable        threshold=2 identity with cold key recovery phrase

Named identities live at ~/.campfire/agents/<name>/. Session identities print
the CF_HOME path on line 1 and the display name on line 2. The caller sets
CF_HOME to the printed path for subsequent commands.

When --name is set, the new agent inherits join-policy.json, operator-root.json,
and aliases.json from the parent CF_HOME (or --from path if specified).
The --from flag requires --name — config inheritance only applies to named agents.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		forceInit, _ := cmd.Flags().GetBool("force")
		initName, _ := cmd.Flags().GetString("name")
		initSession, _ := cmd.Flags().GetBool("session")
		initFrom, _ := cmd.Flags().GetString("from")
		initDurable, _ := cmd.Flags().GetBool("durable")
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
			// Inherit join-policy and operator-root from parent CF_HOME (silently skip if missing).
			// aliases.json is NOT copied — sessions are short-lived and don't need command sugar.
			parentHome := CFHome()
			for _, fname := range []string{"join-policy.json", "operator-root.json"} {
				src := filepath.Join(parentHome, fname)
				data, readErr := os.ReadFile(src)
				if readErr != nil {
					continue // missing is fine
				}
				if writeErr := os.WriteFile(filepath.Join(tmpDir, fname), data, 0600); writeErr != nil {
					return fmt.Errorf("inheriting %s to session: %w", fname, writeErr)
				}
			}
			writeContext(tmpDir)
			hexKey := agentID.PublicKeyHex()
			displayName := "agent:" + hexKey[:6]
			fmt.Println(tmpDir)
			fmt.Println(displayName)
			return nil
		}

		cfHome := CFHome()

		// Validate --from path early, before any work is done.
		// This provides a clear error when the user supplies an invalid path.
		if initFrom != "" {
			info, err := os.Stat(initFrom)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("cf init --from: path does not exist: %s", initFrom)
				}
				return fmt.Errorf("cf init --from: cannot access path %s: %w", initFrom, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("cf init --from: path is not a directory: %s", initFrom)
			}
		}

		// --from without --name is a user error: --from only makes sense when
		// creating a named agent that will inherit the config. Without --name,
		// there is no agent home to inherit into, so the flag would be silently
		// ignored. Return a clear error so the user knows what to do.
		if initFrom != "" && initName == "" {
			return fmt.Errorf("cf init --from requires --name: use --name <agent-name> to specify the agent that will inherit config from %s", initFrom)
		}

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

		// Resolve passphrase: env var first, then interactive prompt.
		passphrase, passphraseErr := resolvePassphrase()
		if passphraseErr != nil {
			return fmt.Errorf("reading passphrase: %w", passphraseErr)
		}

		if len(passphrase) > 0 {
			if err := agentID.SaveWrapped(identityPath, passphrase); err != nil {
				return fmt.Errorf("saving identity: %w", err)
			}
		} else {
			if err := agentID.Save(identityPath); err != nil {
				return fmt.Errorf("saving identity: %w", err)
			}
		}

		// Write CONTEXT.md alongside the identity
		writeContext(cfHome)

		// Step 2-7: Create self-campfire with identity convention genesis message.
		// This is an atomic 7-step operation that replaces the old home+center creation.
		selfCampfireID, coldKeyPhrase, err := createSelfCampfire(cfHome, agentID, initDurable)
		if err != nil {
			// Non-fatal: self-campfire creation failure should not block init.
			fmt.Fprintf(os.Stderr, "warning: could not create identity campfire: %v\n", err)
		}

		// Named agent: inherit config files from parent CF_HOME.
		if initName != "" {
			parentHome := initFrom
			if parentHome == "" {
				parentHome = CFHome()
			}
			if err := inheritAgentConfig(parentHome, cfHome, initName); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not inherit config: %v\n", err)
			}
		}

		if jsonOutput {
			out := map[string]any{
				"status":               "created",
				"public_key":           agentID.PublicKeyHex(),
				"location":             cfHome,
				"identity_campfire_id": selfCampfireID,
			}
			if coldKeyPhrase != "" {
				out["cold_key_phrase"] = coldKeyPhrase
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		if selfCampfireID != "" {
			fmt.Printf("Your identity campfire: %s. Share it like any beacon.\n", selfCampfireID)
		}
		if coldKeyPhrase != "" {
			fmt.Printf("\nRecovery phrase (write down and store offline):\n%s\n", coldKeyPhrase)
		}

		fmt.Printf(`
Next: cf discover    find campfires
      cf create      start one
      cf join <id>   join one
`)
		return nil
	},
}

// inheritAgentConfig copies join-policy.json, operator-root.json, and aliases.json
// from parentHome to agentHome, then writes meta.json with name, parent_cf_home, and created_at.
// Missing source files are silently skipped.
func inheritAgentConfig(parentHome, agentHome, name string) error {
	for _, filename := range []string{"join-policy.json", "operator-root.json", "aliases.json"} {
		src := filepath.Join(parentHome, filename)
		data, err := os.ReadFile(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue // silently skip missing files
			}
			return fmt.Errorf("reading %s: %w", filename, err)
		}
		dst := filepath.Join(agentHome, filename)
		if err := os.WriteFile(dst, data, 0600); err != nil {
			return fmt.Errorf("writing %s: %w", filename, err)
		}
	}

	// Write meta.json
	meta := map[string]any{
		"name":           name,
		"parent_cf_home": parentHome,
		"created_at":     time.Now().Unix(),
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentHome, "meta.json"), metaData, 0600); err != nil {
		return fmt.Errorf("writing meta.json: %w", err)
	}
	return nil
}

// createSelfCampfire creates the agent's identity (self-) campfire using the
// 7-step atomic protocol defined in design-identity-as-campfire.md §cf init Collapse.
//
// The self-campfire is typed by its genesis message (message 0): a campfire-key-signed
// identity convention declaration tagged convention:operation. Message 1 is an
// agent-key-signed introduce-me operation. The "home" alias is set to the campfire ID
// and a beacon with tag identity:v1 is published.
//
// When durable is true, the campfire is created with threshold=2. A FROST DKG is run
// locally; the agent holds share 1 and share 2 is returned as a BIP-39 recovery phrase
// for offline cold key storage.
//
// Returns (campfireID, coldKeyPhrase, error). coldKeyPhrase is empty when durable is false.
func createSelfCampfire(cfHome string, agentID *identity.Identity, durable bool) (string, string, error) {
	// Step 2: Create self-campfire keypair (invite-only).
	// threshold=1 for standard identity, threshold=2 for durable identity.
	cfThreshold := uint(1)
	if durable {
		cfThreshold = 2
	}
	selfCF, err := campfire.New("invite-only", nil, cfThreshold)
	if err != nil {
		return "", "", fmt.Errorf("creating self-campfire: %w", err)
	}

	// For durable identity: run local FROST DKG to generate threshold key shares.
	// The agent holds share 1; share 2 is the cold key (output as BIP-39 phrase).
	// DKG runs before transport init — shares are independent of transport.
	var coldKeyPhrase string
	var agentShareData []byte // serialized share 1 for store write after membership add
	if durable {
		results, dkgErr := threshold.RunDKG([]uint32{1, 2}, 2)
		if dkgErr != nil {
			return "", "", fmt.Errorf("running DKG for durable identity: %w", dkgErr)
		}
		// Serialize share 1 (agent's share) for store persistence.
		shareData1, serErr := threshold.MarshalResult(1, results[1])
		if serErr != nil {
			return "", "", fmt.Errorf("serializing agent threshold share: %w", serErr)
		}
		agentShareData = shareData1
		// Encode share 2 secret (32 bytes) as BIP-39 24-word mnemonic for cold storage.
		// MarshalBinary() = 2-byte party ID + 32-byte scalar; last 32 bytes = secret.
		shareRaw2, rawErr := results[2].SecretShare.MarshalBinary()
		if rawErr != nil {
			return "", "", fmt.Errorf("serializing cold key share: %w", rawErr)
		}
		secretBytes := shareRaw2[len(shareRaw2)-32:]
		mnemonic, mnemonicErr := bip39.NewMnemonic(secretBytes)
		if mnemonicErr != nil {
			return "", "", fmt.Errorf("generating BIP-39 mnemonic: %w", mnemonicErr)
		}
		coldKeyPhrase = mnemonic
	}

	// Step 3: Initialize transport and admit agent as member 0.
	transport := fs.New(fs.DefaultBaseDir())
	if err := transport.Init(selfCF); err != nil {
		return "", "", fmt.Errorf("initializing transport: %w", err)
	}
	if err := transport.WriteMember(selfCF.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		return "", "", fmt.Errorf("writing member record: %w", err)
	}

	campfireID := selfCF.PublicKeyHex()
	transportDir := transport.CampfireDir(campfireID)

	// Step 4: Post identity convention declaration as message 0, signed by campfire key.
	// This is the type assertion that makes this a self-campfire — the genesis message
	// is signed by the campfire's own key, not the agent key.
	// We post ALL four identity declarations so the convention is fully registered.
	for i, decl := range convention.IdentityDeclarations() {
		declPayload, err := json.Marshal(decl)
		if err != nil {
			return "", "", fmt.Errorf("marshaling identity declaration %d: %w", i, err)
		}
		msg, err := message.NewMessage(
			selfCF.PrivateKey,
			selfCF.PublicKey,
			declPayload,
			[]string{convention.ConventionOperationTag},
			nil,
		)
		if err != nil {
			return "", "", fmt.Errorf("creating genesis message %d: %w", i, err)
		}
		if err := transport.WriteMessage(campfireID, msg); err != nil {
			return "", "", fmt.Errorf("writing genesis message %d: %w", i, err)
		}
	}

	// Step 5: Post introduce-me as message N (after declarations), signed by agent key.
	// Payload: agent pubkey hex, display_name, home campfire IDs (self initially).
	displayName := "agent:" + agentID.PublicKeyHex()[:6]
	introduceMePayload := map[string]any{
		"pubkey_hex":        agentID.PublicKeyHex(),
		"display_name":      displayName,
		"home_campfire_ids": []string{campfireID},
	}
	introduceMeBytes, err := json.Marshal(introduceMePayload)
	if err != nil {
		return "", "", fmt.Errorf("marshaling introduce-me payload: %w", err)
	}
	introduceMsg, err := message.NewMessage(
		agentID.PrivateKey,
		agentID.PublicKey,
		introduceMeBytes,
		[]string{convention.IdentityIntroductionTag},
		nil,
	)
	if err != nil {
		return "", "", fmt.Errorf("creating introduce-me message: %w", err)
	}
	if err := transport.WriteMessage(campfireID, introduceMsg); err != nil {
		return "", "", fmt.Errorf("writing introduce-me message: %w", err)
	}

	// Open store and record membership (required before any protocol.Client operations).
	s, err := store.Open(store.StorePath(cfHome))
	if err != nil {
		return "", "", fmt.Errorf("opening store: %w", err)
	}
	defer s.Close()

	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  transportDir,
		JoinProtocol:  selfCF.JoinProtocol,
		Role:          store.PeerRoleCreator,
		JoinedAt:      store.NowNano(),
		Threshold:     selfCF.Threshold,
		Description:   "identity campfire",
		TransportType: "filesystem",
	}); err != nil {
		return "", "", fmt.Errorf("recording membership: %w", err)
	}

	// For durable identity: persist the agent's threshold share (participant 1).
	if durable && len(agentShareData) > 0 {
		if err := s.UpsertThresholdShare(store.ThresholdShare{
			CampfireID:    campfireID,
			ParticipantID: 1,
			SecretShare:   agentShareData,
		}); err != nil {
			return "", "", fmt.Errorf("storing agent threshold share: %w", err)
		}
	}

	// Mirror genesis messages to store for local readback.
	msgs, err := transport.ListMessages(campfireID)
	if err == nil {
		for _, msg := range msgs {
			s.AddMessage(store.MessageRecordFromMessage(campfireID, &msg, store.NowNano())) //nolint:errcheck
		}
	}

	// Step 6: Set "home" alias to self-campfire ID.
	aliases := naming.NewAliasStore(cfHome)
	if err := aliases.Set("home", campfireID); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not set home alias: %v\n", err)
	}

	// Step 7: Publish beacon with tag identity:v1.
	b, err := beacon.New(
		selfCF.PublicKey,
		selfCF.PrivateKey,
		selfCF.JoinProtocol,
		selfCF.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "filesystem",
			Config:   map[string]string{"dir": transportDir},
		},
		convention.IdentityBeaconTag,
	)
	if err != nil {
		return "", "", fmt.Errorf("creating identity:v1 beacon: %w", err)
	}
	if err := beacon.Publish(BeaconDir(), b); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not publish identity:v1 beacon: %v\n", err)
	}

	return campfireID, coldKeyPhrase, nil
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
	initCmd.Flags().String("from", "", "inherit config from this CF_HOME path (requires --name)")
	initCmd.Flags().String("remote", "", "URL of remote campfire relay for center campfire (default: filesystem)")
	initCmd.Flags().Bool("durable", false, "create a threshold=2 identity campfire with a cold key recovery phrase")
	rootCmd.AddCommand(initCmd)
}

// resolvePassphrase returns the passphrase for wrapping the identity key.
// It reads CF_PASSPHRASE env var first. If not set, it prompts interactively.
// Returns nil (no passphrase) if neither source provides one.
func resolvePassphrase() ([]byte, error) {
	if env := os.Getenv("CF_PASSPHRASE"); env != "" {
		return []byte(env), nil
	}
	return promptPassphrase()
}

// createCenterCampfire creates the operator's center campfire.
// Uses filesystem transport by default; uses HTTP transport if remoteURL is non-empty.
// Returns the campfire ID hex, the transport label ("fs" or "http"), and any error.
func createCenterCampfire(cfHome string, agentID *identity.Identity, remoteURL string) (string, string, error) {
	// Create campfire keypair (open join protocol, no requirements, threshold=1).
	centerCF, err := campfire.New("open", nil, 1)
	if err != nil {
		return "", "", fmt.Errorf("creating center campfire: %w", err)
	}

	transportLabel := "fs"
	transportDir := ""

	if remoteURL == "" {
		// Filesystem transport
		transport := fs.New(fs.DefaultBaseDir())
		if err := transport.Init(centerCF); err != nil {
			return "", "", fmt.Errorf("initializing fs transport: %w", err)
		}
		if err := transport.WriteMember(centerCF.PublicKeyHex(), campfire.MemberRecord{
			PublicKey: agentID.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
		}); err != nil {
			return "", "", fmt.Errorf("writing member record: %w", err)
		}
		transportDir = transport.CampfireDir(centerCF.PublicKeyHex())
	} else {
		// HTTP transport: store the remote URL as the transport dir.
		// TransportType must be "p2p-http" for transport.ResolveType() to
		// recognize it; "http" is only used for the human-readable output label.
		transportLabel = "p2p-http"
		transportDir = remoteURL
	}

	// Open store and record membership
	s, err := store.Open(store.StorePath(cfHome))
	if err != nil {
		return "", "", fmt.Errorf("opening store: %w", err)
	}
	defer s.Close()

	if err := s.AddMembership(store.Membership{
		CampfireID:    centerCF.PublicKeyHex(),
		TransportDir:  transportDir,
		JoinProtocol:  centerCF.JoinProtocol,
		Role:          store.PeerRoleCreator,
		JoinedAt:      store.NowNano(),
		Threshold:     centerCF.Threshold,
		Description:   "center campfire",
		TransportType: transportLabel,
	}); err != nil {
		return "", "", fmt.Errorf("recording membership: %w", err)
	}

	// Set "center" alias
	aliases := naming.NewAliasStore(cfHome)
	if err := aliases.Set("center", centerCF.PublicKeyHex()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not set center alias: %v\n", err)
	}

	return centerCF.PublicKeyHex(), transportLabel, nil
}
