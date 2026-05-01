// session.go — session campfire CLI commands (cf 0.30+).
//
// cf 0.30 MIGRATION: The shared-key bearer token (cfs1_...) is replaced by
// cfs2_ tokens. cfs2_ tokens carry the session campfire address but NO shared
// private key. Workers participate via per-worker Ed25519 keys with parent-bounded
// grants (lazy-mint) from the session creator.
//
// See cf-conventions/cf-session for the full session orchestration API.
//
// Commands:
//
//	cf session create --ttl 2h       Create a session and print a cfs2_ token
//	cf session send <token> <msg>    Post a message (requires CF_HOME identity)
//	cf session read <token>          List messages (requires CF_HOME identity)
//	cf session end <token>           Close a session campfire
//
// <token> may be a cfs2_ token OR a raw 64-char hex campfire ID.
// For worker participation, use the cf-session convention SDK to handle
// identity:introduce → delegation:grant flows with per-worker keys.
package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/session"
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage session campfires (cfs2_ token format, lazy-mint grants)",
	Long: `Session campfire management (cf 0.30+ cfs2_ token model).

A session is a short-lived campfire where each worker has its own Ed25519
identity key with a parent-bounded grant from the orchestrator. Sessions
replace the old shared-key bearer token model (cfs1_).

The cfs2_ token carries the session campfire address but NO shared private key.
Workers must have their own identity (provisioned via cf-session lazy-mint)
and a grant from the session creator before they can participate.

SECURITY:
  A cfs2_ token is an address, not a credential. Sharing it does not grant
  access — workers still need their own key and a delegation:grant.

Commands:
  cf session create --ttl 2h    Create a session and print a cfs2_ token
  cf session send <tok> <msg>   Post a message (requires CF_HOME identity)
  cf session read <tok>         List messages (requires CF_HOME identity)
  cf session end <tok>          Close a session campfire

<tok> may be a cfs2_ token (from cf session create) or a raw 64-char hex ID.
For swarm coordination with worker grants, use the cf-session convention SDK.`,
	GroupID: groupAdvanced,
}

var sessionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a session campfire and print a cfs2_ token",
	Long: `Create a new session campfire and print a cfs2_ token to stdout.

The cfs2_ token carries the session campfire address and no shared private key.
Workers join via their own Ed25519 keys (provisioned by cf-conventions/cf-session
lazy-mint). Each worker has full per-message attribution — no shared signing key.

TTL (--ttl):
  Duration the session campfire is valid. Max 24h. Supports Go duration syntax
  (e.g. 30m, 2h, 12h).

Token format:
  cfs2_<base64url(CBOR payload + creator_sig[64])>
  The token carries: campfire ID, transport config, TTL, creator public key.
  It does NOT carry a private key (anti-feature removed from cfs1_).

Example:
  TOKEN=$(cf session create --ttl 2h)
  cf session send $TOKEN "hello from creator"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ttlStr, _ := cmd.Flags().GetString("ttl")
		if ttlStr == "" {
			ttlStr = "1h"
		}

		ttl, err := time.ParseDuration(ttlStr)
		if err != nil {
			return fmt.Errorf("invalid --ttl %q: %w (use Go duration syntax: 30m, 2h, 12h)", ttlStr, err)
		}
		if ttl <= 0 {
			return fmt.Errorf("--ttl must be positive")
		}
		if ttl > protocol.MaxSessionTTL {
			return fmt.Errorf("--ttl %v exceeds maximum allowed %v", ttl, protocol.MaxSessionTTL)
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		client := protocol.New(s, agentID)
		sess, campfireID, err := client.NewSession(ttl)
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		defer sess.Close()

		// Encode the filesystem transport directory so other processes (cf session
		// send/read/end) can find and join the session campfire via the token alone.
		transportConfig, err := json.Marshal(map[string]string{
			"protocol": "filesystem",
			"dir":      sess.CampfireDir(),
		})
		if err != nil {
			return fmt.Errorf("encoding transport config: %w", err)
		}

		// Encode as a cfs2_ token (campfire address + transport config, no private key).
		tok, err := session.EncodeTokenV2(session.TokenV2Params{
			CampfireID:      campfireID,
			TransportConfig: transportConfig,
			TTL:             ttl,
			CreatorPub:      agentID.PublicKey,
			CreatorPriv:     agentID.PrivateKey,
		})
		if err != nil {
			return fmt.Errorf("encoding cfs2_ token: %w", err)
		}

		// Record membership in the CF_HOME store so this process (if it also calls
		// session send/read) can find the campfire via the standard store path.
		// CreatorPubkey is set to the agent's pubkey so Disband() succeeds (it
		// checks CreatorPubkey == caller pubkey before allowing teardown).
		if addErr := s.AddMembership(store.Membership{
			CampfireID:    campfireID,
			TransportDir:  sess.CampfireDir(),
			JoinProtocol:  "open",
			Role:          campfire.RoleFull,
			JoinedAt:      time.Now().UnixNano(),
			Threshold:     1,
			TransportType: "filesystem",
			CreatorPubkey: agentID.PublicKeyHex(),
		}); addErr != nil {
			// Non-fatal: the session still works; other processes use the token.
			// Log to stderr so the user can see if something went wrong.
			fmt.Fprintf(cmd.ErrOrStderr(), "note: recording session membership in store: %v\n", addErr)
		}

		// Print the cfs2_ token to stdout for capture with $().
		fmt.Println(tok)
		return nil
	},
}

// resolvedSession holds the campfire ID and optional transport dir extracted
// from a session token argument. The transport dir is non-empty only when the
// cfs2_ token carries filesystem transport config.
type resolvedSession struct {
	campfireID   string
	campfireDir  string // empty for raw hex ID args (no token transport config)
}

// resolveCampfireIDFromArg resolves a campfire ID from either a cfs2_ token
// or a raw hex campfire ID string. Used by session send/read/end.
//
// cfs1_ tokens are rejected with a migration error.
func resolveCampfireIDFromArg(arg string) (resolvedSession, error) {
	// cfs1_ token — reject with migration message.
	if strings.HasPrefix(arg, session.TokenPrefixV1) {
		return resolvedSession{}, fmt.Errorf("legacy cfs1_ token not supported in cf 0.30+; " +
			"re-create the session: TOKEN=$(cf session create --ttl 2h)")
	}
	// cfs2_ token — decode and extract campfire ID + transport config.
	if strings.HasPrefix(arg, session.TokenPrefixV2) {
		decoded, err := session.DecodeTokenV2(arg, nil)
		if err != nil {
			return resolvedSession{}, fmt.Errorf("decoding cfs2_ session token: %w", err)
		}
		rs := resolvedSession{campfireID: decoded.CampfireID}
		// Extract filesystem transport dir if present in token.
		if len(decoded.TransportConfig) > 0 {
			var tc map[string]string
			if jsonErr := json.Unmarshal(decoded.TransportConfig, &tc); jsonErr == nil {
				if dir, ok := tc["dir"]; ok && dir != "" {
					rs.campfireDir = dir
				}
			}
		}
		return rs, nil
	}
	// JSON-encoded session object (from cf session create --json).
	if strings.HasPrefix(arg, "{") {
		var obj struct {
			Token      string `json:"token"`
			CampfireID string `json:"campfire_id"`
		}
		if err := json.Unmarshal([]byte(arg), &obj); err == nil {
			if obj.CampfireID != "" {
				return resolvedSession{campfireID: obj.CampfireID}, nil
			}
			if obj.Token != "" {
				return resolveCampfireIDFromArg(obj.Token)
			}
		}
	}
	// Raw hex campfire ID (64 chars or short prefix).
	if !strings.Contains(arg, "_") && len(arg) >= 8 {
		return resolvedSession{campfireID: arg}, nil
	}
	return resolvedSession{}, fmt.Errorf("unrecognised session argument %q (expected cfs2_ token or hex campfire ID)",
		arg[:min(len(arg), 20)])
}

// ensureSessionMembership ensures the CF_HOME store has a membership record for
// the session campfire, adding one from the token's transport config if needed.
// This allows cf session send/read/end (running as separate processes) to find
// the session campfire that was created by cf session create.
//
// creatorPubkeyHex is the caller's identity pubkey hex. It is recorded in
// the membership so that Disband() (called by cf session end) can verify
// the creator matches — required for teardown to succeed.
func ensureSessionMembership(s store.Store, rs resolvedSession, creatorPubkeyHex string) error {
	if rs.campfireDir == "" {
		// No transport dir in the token — the caller must already have membership
		// (e.g. raw hex ID). Nothing to add.
		return nil
	}
	// Check if membership already exists.
	existing, err := s.GetMembership(rs.campfireID)
	if err == nil && existing != nil {
		return nil // already a member
	}
	// Add temporary membership so the protocol client can resolve the transport.
	// CreatorPubkey is set so that Disband() succeeds when called by the same
	// identity that ran cf session create.
	return s.AddMembership(store.Membership{
		CampfireID:    rs.campfireID,
		TransportDir:  rs.campfireDir,
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
		CreatorPubkey: creatorPubkeyHex,
	})
}

var sessionSendCmd = &cobra.Command{
	Use:   "send <token-or-id> <message>",
	Short: "Post a message to a session campfire",
	Long: `Post a message to a session campfire.

<token-or-id> may be a cfs2_ token (from cf session create) or a raw 64-char
hex campfire ID. cfs1_ tokens are rejected with a migration error.

Requires a CF_HOME identity. The sender must be the session creator or a
worker with a valid per-worker grant (provisioned via cf-conventions/cf-session).

Example:
  TOKEN=$(cf session create --ttl 2h)
  cf session send $TOKEN "hello world"`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		rs, err := resolveCampfireIDFromArg(args[0])
		if err != nil {
			return err
		}
		payload := args[1]

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		// Register session membership in the CF_HOME store if the token carries
		// a filesystem transport dir we haven't seen yet.
		if memErr := ensureSessionMembership(s, rs, agentID.PublicKeyHex()); memErr != nil {
			return fmt.Errorf("joining session campfire: %w", memErr)
		}

		client := protocol.New(s, agentID)
		msg, err := client.Send(protocol.SendRequest{
			CampfireID: rs.campfireID,
			Payload:    []byte(payload),
		})
		if err != nil {
			return fmt.Errorf("sending message: %w", err)
		}

		if jsonOutput {
			fmt.Printf(`{"id":%q,"campfire_id":%q}`+"\n", msg.ID, rs.campfireID)
		} else {
			fmt.Printf("sent: %s\n", msg.ID[:8])
		}
		return nil
	},
}

var sessionReadCmd = &cobra.Command{
	Use:   "read <token-or-id>",
	Short: "List messages in a session campfire",
	Long: `Read messages from a session campfire.

<token-or-id> may be a cfs2_ token (from cf session create) or a raw 64-char
hex campfire ID. cfs1_ tokens are rejected with a migration error.

Requires a CF_HOME identity. The sender must be the session creator or a
worker with a valid per-worker grant.

Example:
  TOKEN=$(cf session create --ttl 2h)
  cf session read $TOKEN`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		rs, err := resolveCampfireIDFromArg(args[0])
		if err != nil {
			return err
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		if memErr := ensureSessionMembership(s, rs, agentID.PublicKeyHex()); memErr != nil {
			return fmt.Errorf("joining session campfire: %w", memErr)
		}

		client := protocol.New(s, agentID)
		result, err := client.Read(protocol.ReadRequest{
			CampfireID: rs.campfireID,
		})
		if err != nil {
			return fmt.Errorf("reading messages: %w", err)
		}

		msgs := result.Messages
		if jsonOutput {
			for _, m := range msgs {
				fmt.Printf(`{"id":%q,"payload":%q}`+"\n", m.ID, string(m.Payload))
			}
		} else {
			for _, m := range msgs {
				id := m.ID
				if len(id) > 8 {
					id = id[:8]
				}
				fmt.Printf("[%s] %s\n", id, string(m.Payload))
			}
		}
		return nil
	},
}

var sessionEndCmd = &cobra.Command{
	Use:   "end <token-or-id>",
	Short: "Close a session campfire",
	Long: `Close a session campfire and release its resources.

<token-or-id> may be a cfs2_ token (from cf session create) or a raw 64-char
hex campfire ID. cfs1_ tokens are rejected with a migration error.

Posts a session:close event and disbands the campfire.
Requires a CF_HOME identity (must be the session creator).

Example:
  TOKEN=$(cf session create --ttl 2h)
  cf session end $TOKEN`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		rs, err := resolveCampfireIDFromArg(args[0])
		if err != nil {
			return err
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		if memErr := ensureSessionMembership(s, rs, agentID.PublicKeyHex()); memErr != nil {
			return fmt.Errorf("joining session campfire: %w", memErr)
		}

		client := protocol.New(s, agentID)
		if err := client.Disband(rs.campfireID); err != nil {
			return fmt.Errorf("ending session: %w", err)
		}

		if jsonOutput {
			fmt.Printf(`{"campfire_id":%q,"status":"ended"}`+"\n", rs.campfireID)
		} else {
			suffix := rs.campfireID
			if len(suffix) > 12 {
				suffix = suffix[:12]
			}
			fmt.Printf("session ended: %s\n", suffix)
		}
		return nil
	},
}

func init() {
	sessionCreateCmd.Flags().String("ttl", "1h", "session duration (max 24h; e.g. 30m, 2h, 12h)")

	sessionCmd.AddCommand(sessionCreateCmd)
	sessionCmd.AddCommand(sessionSendCmd)
	sessionCmd.AddCommand(sessionReadCmd)
	sessionCmd.AddCommand(sessionEndCmd)

	rootCmd.AddCommand(sessionCmd)
}
