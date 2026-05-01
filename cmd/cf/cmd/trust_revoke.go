package cmd

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extension/delegation"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

var trustRevokeCmd = &cobra.Command{
	Use:   "revoke <campfire-id> <child-pubkey>",
	Short: "Post an identity:revoked message invalidating a delegated child key",
	Long: `Post a signed identity:revoked message from the local identity (the parent)
revoking a previously delegated child ed25519 public key in the given campfire.

The revocation is effective from the moment it is posted. Any identity:granted
messages for the child key that predate this message are no longer accepted by
Resolve. Revocation does not require a matching grant to exist.

The local identity IS the revoking parent — to revoke from a different parent,
run cf with a different CF_HOME.

Exit codes:
  0  revocation posted
  1  invalid arguments or send failed

Example:
  cf trust revoke aaaa...bbbb cccc...dddd`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireArg := args[0]
		childArg := args[1]

		// Decode child pubkey from hex.
		childBytes, err := hex.DecodeString(childArg)
		if err != nil {
			return fmt.Errorf("invalid child pubkey %q: %w", childArg, err)
		}
		if len(childBytes) != ed25519.PublicKeySize {
			return fmt.Errorf("child pubkey must be 64 hex chars (32 bytes), got %d bytes", len(childBytes))
		}

		// Initialize protocol client — opens store, loads identity, resolves config.
		client, initResult, err := protocol.Init(CFHome())
		if err != nil {
			return fmt.Errorf("initializing protocol client: %w", err)
		}
		printInitResultVerbose(initResult)
		defer client.Close()

		// Resolve campfire ID (supports aliases).
		s, err := openStore()
		if err != nil {
			return err
		}
		campfireID, err := resolveCampfireID(campfireArg, s)
		s.Close()
		if err != nil {
			return fmt.Errorf("resolving campfire %q: %w", campfireArg, err)
		}

		campfireBytes, err := hex.DecodeString(campfireID)
		if err != nil {
			return fmt.Errorf("invalid campfire ID %q: %w", campfireID, err)
		}

		// Post the revocation.
		msg, err := delegation.PostRevoke(
			context.Background(),
			client,
			campfireBytes,
			ed25519.PublicKey(childBytes),
		)
		if err != nil {
			return fmt.Errorf("posting revocation: %w", err)
		}

		parentHex := hex.EncodeToString(msg.Sender)

		if jsonOutput {
			return emitTrustRevokeJSON(msg.ID, parentHex, childArg, campfireID)
		}
		return emitTrustRevokeHuman(msg.ID, parentHex, childArg)
	},
}

func emitTrustRevokeHuman(msgID, parentHex, childHex string) error {
	short := func(h string) string {
		if len(h) >= 8 {
			return h[:8]
		}
		return h
	}
	fmt.Printf("revoked: msg=%s parent=%s child=%s\n",
		short(msgID),
		short(parentHex),
		short(childHex),
	)
	return nil
}

func emitTrustRevokeJSON(msgID, parentHex, childHex, campfireID string) error {
	type result struct {
		MsgID      string `json:"msg_id"`
		Parent     string `json:"parent"`
		Child      string `json:"child"`
		CampfireID string `json:"campfire_id"`
	}
	r := result{
		MsgID:      msgID,
		Parent:     parentHex,
		Child:      childHex,
		CampfireID: campfireID,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func init() {
	trustCmd.AddCommand(trustRevokeCmd)
}
