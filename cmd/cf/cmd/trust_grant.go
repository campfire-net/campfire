package cmd

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/delegation"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

var trustGrantTTL time.Duration

var trustGrantCmd = &cobra.Command{
	Use:   "grant <campfire-id> <child-pubkey>",
	Short: "Issue an identity:granted message delegating trust to a child key",
	Long: `Issue a signed identity:granted message from the local identity (the parent)
to a child ed25519 public key, valid in the given campfire for --ttl.

The posted grant is covered by Rule 4 of identity-delegation-v0.1.md §4: TTLs
longer than 7 days are rejected (not silently clamped). The local identity IS
the grant parent — to issue from a different parent, run cf with a different
CF_HOME.

Exit codes:
  0  grant posted
  1  invalid arguments, TTL exceeds 7 days, or send failed

Example:
  cf trust grant aaaa...bbbb cccc...dddd --ttl 24h`,
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

		// Issue the grant.
		msg, err := delegation.PostGrant(
			context.Background(),
			client,
			campfireBytes,
			ed25519.PublicKey(childBytes),
			trustGrantTTL,
		)
		if err != nil {
			// Cobra prints RunE errors to stderr and sets a non-zero exit code.
			// We embed the friendly hint in the error message rather than calling
			// os.Exit directly so the path remains testable in-process.
			if errors.Is(err, delegation.ErrGrantCeilingExceeded) {
				return fmt.Errorf("grant TTL exceeds 7-day ceiling (max TTL is 7 days — specify --ttl <= 168h)")
			}
			if errors.Is(err, delegation.ErrGrantTTLInvalid) {
				return fmt.Errorf("grant TTL must be positive")
			}
			return fmt.Errorf("posting grant: %w", err)
		}

		// Parse payload to retrieve the expires_at we wrote.
		gp, err := delegation.ParseGrantPayload(msg.Payload)
		if err != nil {
			return fmt.Errorf("parsing posted grant payload: %w", err)
		}
		parentHex := hex.EncodeToString(msg.Sender)

		if jsonOutput {
			return emitTrustGrantJSON(msg.ID, parentHex, childArg, campfireID, gp.ExpiresAt, trustGrantTTL)
		}
		return emitTrustGrantHuman(msg.ID, parentHex, childArg, gp.ExpiresAt)
	},
}

func emitTrustGrantHuman(msgID, parentHex, childHex string, expiresAt int64) error {
	short := func(h string) string {
		if len(h) >= 8 {
			return h[:8]
		}
		return h
	}
	fmt.Printf("grant posted: msg=%s parent=%s child=%s expires=%s\n",
		short(msgID),
		short(parentHex),
		short(childHex),
		time.Unix(expiresAt, 0).UTC().Format(time.RFC3339),
	)
	return nil
}

func emitTrustGrantJSON(msgID, parentHex, childHex, campfireID string, expiresAt int64, ttl time.Duration) error {
	type result struct {
		MsgID      string `json:"msg_id"`
		Parent     string `json:"parent"`
		Child      string `json:"child"`
		CampfireID string `json:"campfire_id"`
		ExpiresAt  int64  `json:"expires_at"`
		TTLSeconds int64  `json:"ttl_seconds"`
	}
	r := result{
		MsgID:      msgID,
		Parent:     parentHex,
		Child:      childHex,
		CampfireID: campfireID,
		ExpiresAt:  expiresAt,
		TTLSeconds: int64(ttl / time.Second),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func init() {
	trustGrantCmd.Flags().DurationVar(&trustGrantTTL, "ttl", 24*time.Hour, "grant lifetime (max 168h / 7 days)")
	trustCmd.AddCommand(trustGrantCmd)
}
