// session.go — session campfire CLI commands (cf 0.30+).
//
// cf 0.30 MIGRATION: The shared-key bearer token (cfs1_...) is removed.
// Sessions are now campfires that use per-worker Ed25519 keys with parent-bounded
// grants from the session creator. See cf-conventions/cf-session for the full
// session orchestration API (lazy-mint, jail-write, signing-proxy).
//
// These commands manage session campfires from the creator's perspective:
//   cf session create --ttl 2h     Create a session and print the campfire ID
//   cf session send <id> <msg>     Post a message (requires CF_HOME identity)
//   cf session read <id>           List messages (requires CF_HOME identity)
//   cf session end <id>            Close a session campfire
//
// For worker participation, use the cf-session convention SDK to handle
// identity:introduce → delegation:grant flows with per-worker keys.
package cmd

import (
	"fmt"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage session campfires (per-worker identity, lazy-mint grants)",
	Long: `Session campfire management (cf 0.30+ per-worker key model).

A session is a short-lived campfire where each worker has its own Ed25519
identity key with a parent-bounded grant from the orchestrator. Sessions
replace the old shared-key bearer token model.

SECURITY:
  The session campfire ID is a public identifier — not a bearer credential.
  Workers must have their own identity (provisioned via cf-session jail or
  signing-proxy backend) and a grant from the session creator before they
  can participate. Use cf-conventions/cf-session for worker provisioning.

Commands:
  cf session create --ttl 2h     Create a session and print the campfire ID
  cf session send <id> <msg>     Post a message (requires CF_HOME identity)
  cf session read <id>           List messages (requires CF_HOME identity)
  cf session end <id>            Close a session campfire

For swarm coordination with worker grants, use the cf-session convention SDK.`,
	GroupID: groupAdvanced,
}

var sessionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a session campfire and print its campfire ID",
	Long: `Create a new session campfire and print its campfire ID to stdout.

The session campfire uses the creator's own identity key for signing.
Workers participate via per-worker keys provisioned by cf-conventions/cf-session.

TTL (--ttl):
  Duration the session campfire is valid. Max 24h. Supports Go duration syntax
  (e.g. 30m, 2h, 12h).

Example:
  SESSION_ID=$(cf session create --ttl 2h)
  cf session send $SESSION_ID "hello from creator"`,
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

		// Print the campfire ID to stdout for capture with $().
		fmt.Println(campfireID)
		return nil
	},
}

var sessionSendCmd = &cobra.Command{
	Use:   "send <campfire-id> <message>",
	Short: "Post a message to a session campfire",
	Long: `Post a message to a session campfire.

Requires a CF_HOME identity. The sender must be the session creator or a
worker with a valid per-worker grant (provisioned via cf-conventions/cf-session).

Example:
  cf session send <campfire-id> "hello world"`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID := args[0]
		payload := args[1]

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		client := protocol.New(s, agentID)
		msg, err := client.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte(payload),
		})
		if err != nil {
			return fmt.Errorf("sending message: %w", err)
		}

		if jsonOutput {
			fmt.Printf(`{"id":%q,"campfire_id":%q}`+"\n", msg.ID, campfireID)
		} else {
			fmt.Printf("sent: %s\n", msg.ID[:8])
		}
		return nil
	},
}

var sessionReadCmd = &cobra.Command{
	Use:   "read <campfire-id>",
	Short: "List messages in a session campfire",
	Long: `Read messages from a session campfire.

Requires a CF_HOME identity. The sender must be the session creator or a
worker with a valid per-worker grant.

Example:
  cf session read <campfire-id>`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID := args[0]

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		client := protocol.New(s, agentID)
		result, err := client.Read(protocol.ReadRequest{
			CampfireID: campfireID,
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
	Use:   "end <campfire-id>",
	Short: "Close a session campfire",
	Long: `Close a session campfire and release its resources.

Posts a session:close event and disbands the campfire.
Requires a CF_HOME identity (must be the session creator).

Example:
  cf session end <campfire-id>`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID := args[0]

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		client := protocol.New(s, agentID)
		if err := client.Disband(campfireID); err != nil {
			return fmt.Errorf("ending session: %w", err)
		}

		if jsonOutput {
			fmt.Printf(`{"campfire_id":%q,"status":"ended"}`+"\n", campfireID)
		} else {
			suffix := campfireID
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
