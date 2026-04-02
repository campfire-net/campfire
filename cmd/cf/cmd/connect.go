package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/spf13/cobra"
)

// connectDefaultTimeout is the default time to wait for the target to respond
// to a connect-request future.
const connectDefaultTimeout = 5 * time.Minute

// trustVouchTag is the tag used to record a mutual connection vouch.
const trustVouchTag = "trust:vouch"

// trustRevokeTag is the tag used to revoke a trust vouch on disconnect.
const trustRevokeTag = "trust:revoke"

var connectCmd = &cobra.Command{
	Use:   "connect <campfire-id-or-alias>",
	Short: "Request a mutual connection with another campfire's operator",
	Long: `Send a connect-request to the target campfire and await their accept or reject.

The connect ceremony posts a connect-request future on the target's home
campfire. The target must fulfill it with accept-connection or reject-connection.
On acceptance, a trust:vouch is posted on your home campfire.

Example:
  cf connect alice
  cf connect abc123...def456
  cf connect --timeout 10m bob`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		timeoutStr, _ := cmd.Flags().GetString("timeout")
		myName, _ := cmd.Flags().GetString("name")

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		// Resolve target campfire ID.
		targetID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return fmt.Errorf("resolving target campfire ID: %w", err)
		}

		// Resolve home campfire ID (alias "home").
		homeID, err := resolveCampfireID("home", s)
		if err != nil {
			return fmt.Errorf("resolving home campfire (alias 'home'): %w\n\nRun cf init to create your identity campfire", err)
		}

		if homeID == targetID {
			return fmt.Errorf("cannot connect to your own home campfire")
		}

		// Parse timeout.
		timeout := connectDefaultTimeout
		if timeoutStr != "" {
			timeout, err = time.ParseDuration(timeoutStr)
			if err != nil {
				return fmt.Errorf("invalid timeout: %w", err)
			}
		}

		client := protocol.New(s, agentID)

		// Build connect-request payload.
		payload := map[string]string{
			"requester_campfire_id": homeID,
		}
		if myName != "" {
			payload["requester_name"] = myName
		}
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encoding connect-request payload: %w", err)
		}

		// Post connect-request as a future on the target campfire.
		// Non-members are allowed to post connect-requests.
		connectMsg, err := client.Send(protocol.SendRequest{
			CampfireID: targetID,
			Payload:    payloadBytes,
			Tags:       []string{convention.SocialConnectRequestTag, "future"},
		})
		if err != nil {
			return fmt.Errorf("posting connect-request on %s: %w", targetID[:12], err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "connect-request posted → %s (awaiting response, timeout %s)\n",
			connectMsg.ID[:12], timeout)

		// Determine poll interval from target membership (if we are a member).
		interval := 5 * time.Second
		m, _ := s.GetMembership(targetID)
		if m != nil {
			interval = followIntervalForTransport(*m)
		}

		// Await fulfillment of the connect-request future.
		type awaitResult struct {
			msg *protocol.Message
			err error
		}
		resultCh := make(chan awaitResult, 1)
		go func() {
			msg, awaitErr := client.Await(protocol.AwaitRequest{
				CampfireID:   targetID,
				TargetMsgID:  connectMsg.ID,
				Timeout:      timeout,
				PollInterval: interval,
			})
			resultCh <- awaitResult{msg: msg, err: awaitErr}
		}()

		res := <-resultCh
		if errors.Is(res.err, protocol.ErrAwaitTimeout) {
			fmt.Fprintf(os.Stderr, "connect-request timed out after %s — no response from %s\n", timeout, targetID[:12])
			os.Exit(1)
			return nil
		}
		if res.err != nil {
			return fmt.Errorf("awaiting connect-response: %w", res.err)
		}

		fulfillment := res.msg

		// Check if the response is a rejection.
		if isConnectRejection(fulfillment) {
			reason := extractRejectionReason(fulfillment)
			if reason != "" {
				fmt.Fprintf(os.Stderr, "connection rejected: %s\n", reason)
			} else {
				fmt.Fprintf(os.Stderr, "connection rejected\n")
			}
			os.Exit(1)
			return nil
		}

		// Acceptance path: post trust:vouch on our home campfire.
		vouchPayload := map[string]string{
			"subject_campfire_id": targetID,
			"relationship":        "connection",
		}
		vouchBytes, err := json.Marshal(vouchPayload)
		if err != nil {
			return fmt.Errorf("encoding trust:vouch payload: %w", err)
		}
		vouchMsg, err := client.Send(protocol.SendRequest{
			CampfireID: homeID,
			Payload:    vouchBytes,
			Tags:       []string{trustVouchTag},
		})
		if err != nil {
			// Non-fatal: the connection was accepted; the vouch is best-effort.
			fmt.Fprintf(os.Stderr, "warning: accepted but could not post trust:vouch on home: %v\n", err)
		}

		// If the acceptor provided a shared channel, record the alias.
		sharedChannelID := extractSharedChannelID(fulfillment)

		fmt.Fprintf(cmd.OutOrStdout(), "connection accepted!\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  target:       %s\n", targetID)
		fmt.Fprintf(cmd.OutOrStdout(), "  fulfillment:  %s\n", fulfillment.ID[:12])
		if vouchMsg != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  trust:vouch:  %s\n", vouchMsg.ID[:12])
		}
		if sharedChannelID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  shared channel: %s\n", sharedChannelID)
			// Record alias for easy reference: <name>-channel
			aliasName := args[0] + "-channel"
			aliases := naming.NewAliasStore(CFHome())
			if err := aliases.Set(aliasName, sharedChannelID); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not set alias %q: %v\n", aliasName, err)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "  alias:          %s → %s\n", aliasName, sharedChannelID)
			}
		}

		return nil
	},
}

var disconnectCmd = &cobra.Command{
	Use:   "disconnect <campfire-id-or-alias>",
	Short: "Revoke a previously established connection",
	Long: `Revoke a mutual connection with another campfire's operator.

Posts trust:revoke on your home campfire for the target, and removes any
shared channel alias that was set during the connect ceremony.

Example:
  cf disconnect alice
  cf disconnect abc123...def456`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		// Resolve target campfire ID.
		targetID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return fmt.Errorf("resolving target campfire ID: %w", err)
		}

		// Resolve home campfire ID.
		homeID, err := resolveCampfireID("home", s)
		if err != nil {
			return fmt.Errorf("resolving home campfire (alias 'home'): %w", err)
		}

		client := protocol.New(s, agentID)

		// Post trust:revoke on home campfire.
		revokePayload := map[string]string{
			"subject_campfire_id": targetID,
			"relationship":        "connection",
		}
		revokeBytes, err := json.Marshal(revokePayload)
		if err != nil {
			return fmt.Errorf("encoding trust:revoke payload: %w", err)
		}
		revokeMsg, err := client.Send(protocol.SendRequest{
			CampfireID: homeID,
			Payload:    revokeBytes,
			Tags:       []string{trustRevokeTag},
		})
		if err != nil {
			return fmt.Errorf("posting trust:revoke on home: %w", err)
		}

		// Remove shared channel alias if present.
		aliasName := args[0] + "-channel"
		aliases := naming.NewAliasStore(CFHome())
		_ = aliases.Remove(aliasName) // best-effort; ignore error

		fmt.Fprintf(cmd.OutOrStdout(), "disconnected from %s\n", targetID[:12])
		fmt.Fprintf(cmd.OutOrStdout(), "  trust:revoke: %s\n", revokeMsg.ID[:12])

		return nil
	},
}

// isConnectRejection returns true if the fulfillment message is a rejection.
func isConnectRejection(msg *protocol.Message) bool {
	if msg == nil {
		return false
	}
	for _, tag := range msg.Tags {
		if tag == convention.SocialConnectRejectedTag {
			return true
		}
	}
	return false
}

// extractRejectionReason returns the human-readable reason from a reject-connection
// payload, or "" if not present or unparseable.
func extractRejectionReason(msg *protocol.Message) string {
	if msg == nil || len(msg.Payload) == 0 {
		return ""
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return ""
	}
	reason, _ := payload["reason"].(string)
	return reason
}

// extractSharedChannelID returns the shared_channel_id from an accept-connection
// payload, or "" if not present.
func extractSharedChannelID(msg *protocol.Message) string {
	if msg == nil || len(msg.Payload) == 0 {
		return ""
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return ""
	}
	ch, _ := payload["shared_channel_id"].(string)
	return ch
}

func init() {
	connectCmd.Flags().String("timeout", "", "maximum time to wait for response (default 5m)")
	connectCmd.Flags().String("name", "", "optional display name to include in the request (tainted)")
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(disconnectCmd)
}
