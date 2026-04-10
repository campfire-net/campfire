package cmd

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/convention/delegation"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/spf13/cobra"
)

var trustResolveCmd = &cobra.Command{
	Use:   "resolve <campfire-id> <sender-pubkey>",
	Short: "Resolve whether a sender is trusted via the identity delegation chain",
	Long: `Walk the identity delegation chain to determine whether a sender pubkey
is trusted within a campfire.

Trust anchors are loaded from Config.Identity.TrustAnchors ([identity.trust]
anchors in config.toml). A sender is trusted if it IS a trust anchor, or
if there is a valid, unrevoked grant chain from the sender back to an anchor.

Exit codes:
  0  Resolved — sender traces back to a trust anchor
  1  DeadEnd, InvalidGrant, or DepthExceeded — sender is not trusted`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireArg := args[0]
		senderArg := args[1]

		// Decode sender pubkey from hex.
		senderBytes, err := hex.DecodeString(senderArg)
		if err != nil {
			return fmt.Errorf("invalid sender pubkey %q: %w", senderArg, err)
		}
		if len(senderBytes) != 32 {
			return fmt.Errorf("sender pubkey must be 64 hex chars (32 bytes), got %d bytes", len(senderBytes))
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

		// Decode campfire ID from hex.
		campfireBytes, err := hex.DecodeString(campfireID)
		if err != nil {
			return fmt.Errorf("invalid campfire ID %q: %w", campfireID, err)
		}

		// Load trust anchors from config cascade.
		cwd, _ := os.Getwd()
		cfg, _, _, cfgErr := protocol.LoadConfig(CFHome(), cwd)
		var anchorKeys []ed25519.PublicKey
		if cfgErr == nil && cfg != nil {
			anchorKeys = cfg.Identity.TrustAnchors
		}

		// Call delegation.Resolve.
		outcome := delegation.Resolve(
			context.Background(),
			client,
			campfireBytes,
			ed25519.PublicKey(senderBytes),
			anchorKeys,
		)

		if jsonOutput {
			return printResolveJSON(outcome, campfireID, senderArg)
		}
		return printResolveHuman(outcome, campfireID, senderArg)
	},
}

func printResolveHuman(outcome delegation.Outcome, campfireID, senderArg string) error {
	short := func(h string) string {
		if len(h) > 12 {
			return h[:12] + "..."
		}
		return h
	}
	switch o := outcome.(type) {
	case delegation.Resolved:
		fmt.Printf("Resolved: %s is trusted in campfire %s\n", short(senderArg), short(campfireID))
		if len(o.Chain) == 0 {
			fmt.Printf("  (sender is a direct trust anchor)\n")
		} else {
			fmt.Printf("  Chain (%d hop(s)):\n", len(o.Chain))
			for i, gi := range o.Chain {
				fmt.Printf("    [%d] %s → %s\n", i+1,
					short(hex.EncodeToString(gi.ChildPubkey)),
					short(hex.EncodeToString(gi.ParentPubkey)))
			}
		}
		fmt.Printf("  Anchor: %s\n", short(hex.EncodeToString(o.Anchor)))
		return nil

	case delegation.DeadEnd:
		fmt.Printf("DeadEnd: %s is NOT trusted in campfire %s\n", short(senderArg), short(campfireID))
		if len(o.Chain) > 0 {
			fmt.Printf("  Walked %d hop(s) before the chain ended.\n", len(o.Chain))
		}
		fmt.Printf("  No valid unrevoked grant found for: %s\n", short(hex.EncodeToString(o.LastResolved)))
		os.Exit(1)

	case delegation.InvalidGrant:
		fmt.Printf("InvalidGrant: %s has an invalid grant in campfire %s\n", short(senderArg), short(campfireID))
		fmt.Printf("  Error: %v\n", o.Err)
		if o.BadGrant != nil {
			fmt.Printf("  Bad grant message: %s\n", short(o.BadGrant.ID))
		}
		os.Exit(1)

	case delegation.DepthExceeded:
		fmt.Printf("DepthExceeded: chain for %s exceeded maximum depth in campfire %s\n", short(senderArg), short(campfireID))
		fmt.Printf("  Walked %d hop(s) without reaching a trust anchor.\n", len(o.Chain))
		os.Exit(1)

	default:
		fmt.Printf("Unknown outcome type: %T\n", outcome)
		os.Exit(1)
	}
	return nil
}

func printResolveJSON(outcome delegation.Outcome, campfireID, senderArg string) error {
	type chainEntry struct {
		MessageID    string `json:"message_id"`
		ParentPubkey string `json:"parent_pubkey"`
		ChildPubkey  string `json:"child_pubkey"`
	}
	type result struct {
		Status     string       `json:"status"`
		CampfireID string       `json:"campfire_id"`
		Sender     string       `json:"sender"`
		Chain      []chainEntry `json:"chain,omitempty"`
		Anchor     string       `json:"anchor,omitempty"`
		LastKey    string       `json:"last_resolved,omitempty"`
		Error      string       `json:"error,omitempty"`
		BadGrant   string       `json:"bad_grant,omitempty"`
	}
	chainOf := func(gis []delegation.GrantInfo) []chainEntry {
		out := make([]chainEntry, len(gis))
		for i, gi := range gis {
			out[i] = chainEntry{
				MessageID:    gi.MessageID,
				ParentPubkey: hex.EncodeToString(gi.ParentPubkey),
				ChildPubkey:  hex.EncodeToString(gi.ChildPubkey),
			}
		}
		return out
	}

	var r result
	r.CampfireID = campfireID
	r.Sender = senderArg

	exitCode := 0
	switch o := outcome.(type) {
	case delegation.Resolved:
		r.Status = "Resolved"
		r.Chain = chainOf(o.Chain)
		r.Anchor = hex.EncodeToString(o.Anchor)
	case delegation.DeadEnd:
		r.Status = "DeadEnd"
		r.Chain = chainOf(o.Chain)
		r.LastKey = hex.EncodeToString(o.LastResolved)
		exitCode = 1
	case delegation.InvalidGrant:
		r.Status = "InvalidGrant"
		r.Chain = chainOf(o.Chain)
		if o.Err != nil {
			r.Error = o.Err.Error()
		}
		if o.BadGrant != nil {
			r.BadGrant = o.BadGrant.ID
		}
		exitCode = 1
	case delegation.DepthExceeded:
		r.Status = "DepthExceeded"
		r.Chain = chainOf(o.Chain)
		exitCode = 1
	default:
		r.Status = "Unknown"
		r.Error = fmt.Sprintf("unknown outcome type: %T", outcome)
		exitCode = 1
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return err
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

func init() {
	trustCmd.AddCommand(trustResolveCmd)
}
