package cmd

// session.go — zero-ceremony session token CLI commands.
//
// SECURITY: Session tokens are bearer credentials. Anyone holding a valid
// token can send and read messages in the session campfire.
// - Do not log tokens.
// - Do not embed tokens in URLs.
// - Transmit only over encrypted channels.
//
// There is NO per-sender attribution inside a session — all participants share
// the same ephemeral signing key. Use cf join for attributable, durable campfires.
//
// Token format is versioned (cfs1_ prefix) to allow future evolution.
// Maximum TTL is 24 hours, enforced at creation time.

import (
	"fmt"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage zero-ceremony session campfires",
	Long: `Zero-ceremony multi-agent coordination via session tokens.

A session is a short-lived campfire identified by a bearer token.
No cf init or CF_HOME required for participants — just share the token.

SECURITY (BEARER CREDENTIAL):
  The token is equivalent to a password. Anyone holding it can post and
  read messages. Do not log it, do not embed it in URLs, transmit only
  over encrypted channels.

  All participants share the same ephemeral signing key — there is NO
  per-sender attribution inside a session. Use cf join for attributable,
  durable campfires.

Commands:
  cf session create --ttl 2h     Create a session and print the token
  cf session send <token> <msg>  Post a message (no CF_HOME required)
  cf session read <token>        List messages (no CF_HOME required)
  cf session end <token>         Disband the session campfire`,
	GroupID: groupAdvanced,
}

var sessionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a session and print the bearer token",
	Long: `Create a new session campfire and print the bearer token to stdout.

The token embeds everything needed to send and receive messages — campfire ID,
ephemeral signing key, transport config, and TTL. Share it with participants
via a secure channel.

SECURITY (BEARER CREDENTIAL):
  Anyone holding this token can post to and read from the session.
  - Do not log the token.
  - Do not pass it via environment variables that are logged.
  - Transmit only over encrypted channels (HTTPS, SSH, etc.).

  All participants share one ephemeral key — no per-sender attribution.

TTL (--ttl):
  Duration the session is valid. Max 24h. Supports Go duration syntax
  (e.g. 30m, 2h, 12h). Tokens cannot be used after expiry.

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
		sess, token, err := client.NewSession(ttl)
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		defer sess.Close()

		// Print only the token to stdout so callers can capture it with $().
		fmt.Println(token)
		return nil
	},
}

var sessionSendCmd = &cobra.Command{
	Use:   "send <token> <message>",
	Short: "Post a message to a session (no CF_HOME required)",
	Long: `Post a message to a session campfire using a bearer token.

Does not require cf init or CF_HOME — the token contains all necessary state.

SECURITY: The token is a bearer credential — anyone holding it can post.
Transmit only over encrypted channels.

Example:
  cf session send cfs1_<token> "hello world"`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		token := args[0]
		payload := args[1]

		// JoinSession does not require CF_HOME or an existing identity.
		sess, err := protocol.JoinSession(token, nil)
		if err != nil {
			return fmt.Errorf("joining session: %w", err)
		}
		defer sess.Close()

		msg, err := sess.Send(payload)
		if err != nil {
			return fmt.Errorf("sending message: %w", err)
		}

		if jsonOutput {
			fmt.Printf(`{"id":%q,"campfire_id":%q}`+"\n", msg.ID, sess.CampfireID())
		} else {
			fmt.Printf("sent: %s\n", msg.ID[:8])
		}
		return nil
	},
}

var sessionReadCmd = &cobra.Command{
	Use:   "read <token>",
	Short: "List messages in a session (no CF_HOME required)",
	Long: `Read messages from a session campfire using a bearer token.

Does not require cf init or CF_HOME — the token contains all necessary state.

SECURITY: The token is a bearer credential — anyone holding it can read.
Transmit only over encrypted channels.

Example:
  cf session read cfs1_<token>`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		token := args[0]

		sess, err := protocol.JoinSession(token, nil)
		if err != nil {
			return fmt.Errorf("joining session: %w", err)
		}
		defer sess.Close()

		msgs, err := sess.Read()
		if err != nil {
			return fmt.Errorf("reading messages: %w", err)
		}

		if jsonOutput {
			type jsonMsg struct {
				ID      string `json:"id"`
				Payload string `json:"payload"`
			}
			out := make([]jsonMsg, len(msgs))
			for i, m := range msgs {
				out[i] = jsonMsg{ID: m.ID, Payload: string(m.Payload)}
			}
			for _, m := range out {
				fmt.Printf(`{"id":%q,"payload":%q}`+"\n", m.ID, m.Payload)
			}
		} else {
			for _, m := range msgs {
				fmt.Printf("[%s] %s\n", m.ID[:8], string(m.Payload))
			}
		}
		return nil
	},
}

var sessionEndCmd = &cobra.Command{
	Use:   "end <token>",
	Short: "Disband a session campfire",
	Long: `Disband a session campfire and release its resources.

Only the creator of the session can end it (the token must have been created
by the calling agent). After ending, the session token is invalid.

Example:
  cf session end cfs1_<token>`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		token := args[0]

		sess, err := protocol.JoinSession(token, nil)
		if err != nil {
			return fmt.Errorf("joining session: %w", err)
		}

		campfireID := sess.CampfireID()
		if err := sess.End(); err != nil {
			return fmt.Errorf("ending session: %w", err)
		}

		if jsonOutput {
			fmt.Printf(`{"campfire_id":%q,"status":"ended"}`+"\n", campfireID)
		} else {
			fmt.Printf("session ended: %s\n", campfireID[:12])
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
