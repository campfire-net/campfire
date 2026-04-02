package cmd

// identity_upgrade.go — cf identity upgrade command (campfire-agent-bey).
//
// Upgrades an existing keypair-only identity to a self-campfire identity.
// The command:
//  1. Checks idempotency: if "home" alias already points to an identity campfire, exits early.
//  2. Saves any existing "home" alias (old home campfire ID).
//  3. Calls createSelfCampfire to create a new identity campfire with genesis message,
//     introduce-me, membership record, alias, and beacon.
//  4. If an old home campfire exists and the agent is a member, links it via declare-home ceremony.
//  5. Prints: "Identity upgraded. New address: <campfire-id>"
//
// The existing identity.json keypair (agent key) is unchanged. The self-campfire
// gets a new campfire keypair.

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var identityUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade a keypair-only identity to a self-campfire identity",
	Long: `Upgrade an existing keypair-only identity to a self-campfire (identity campfire).

Creates a new self-campfire for this identity:
  - Message 0: identity convention declaration signed by campfire key (the type assertion)
  - Message 1: introduce-me signed by agent key
  - Sets the "home" alias to the new self-campfire
  - Links any existing home campfire via the declare-home ceremony

If the identity is already upgraded (home alias points to an identity campfire),
the command exits with "already upgraded".

The existing identity keypair is unchanged. The self-campfire gets a new keypair.

Example:
  cf identity upgrade`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfHome := CFHome()

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		// Step 1: Idempotency check.
		// If "home" alias already points to an identity campfire (message 0 is
		// campfire-key-signed identity convention declaration), already upgraded.
		aliases := naming.NewAliasStore(cfHome)
		existingHomeID, homeErr := aliases.Get("home")
		if homeErr == nil && existingHomeID != "" {
			// Check if message 0 of the existing home is an identity campfire genesis.
			mHome, memberErr := s.GetMembership(existingHomeID)
			if memberErr == nil && mHome != nil && mHome.TransportDir != "" {
				trHome := fs.ForDir(mHome.TransportDir)
				msgs, listErr := trHome.ListMessages(existingHomeID)
				if listErr == nil && len(msgs) > 0 {
					msg0 := msgs[0]
					if isUpgradeIdentityGenesis(existingHomeID, msg0.SenderHex(), msg0.Payload) {
						if jsonOutput {
							fmt.Printf(`{"status":"already_upgraded","campfire_id":%q}%s`, existingHomeID, "\n")
						} else {
							fmt.Fprintf(cmd.OutOrStdout(), "already upgraded. Home address: %s\n", existingHomeID)
						}
						return nil
					}
				}
			}
		}

		// Step 2: Save old home campfire ID (if any) before creating the new one.
		// createSelfCampfire will overwrite the "home" alias.
		oldHomeID := ""
		if homeErr == nil && existingHomeID != "" {
			oldHomeID = existingHomeID
		}

		// Step 3: Create the new self-campfire.
		// This posts the identity convention genesis (campfire-key-signed) and
		// introduce-me (agent-key-signed), records membership, sets "home" alias,
		// and publishes identity:v1 beacon.
		newCampfireID, _, err := createSelfCampfire(cfHome, agentID, false)
		if err != nil {
			return fmt.Errorf("creating self-campfire: %w", err)
		}

		// Step 4: Link old home campfire (if any) via declare-home ceremony.
		// Only run the ceremony if the agent is still a member of the old campfire.
		if oldHomeID != "" {
			// Re-open store since createSelfCampfire closed its own handle.
			s2, openErr := store.Open(store.StorePath(cfHome))
			if openErr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not re-open store for home linking: %v\n", openErr)
			} else {
				defer s2.Close()
				linkErr := performHomeLinkCeremony(cmd, s2, cfHome, newCampfireID, oldHomeID)
				if linkErr != nil {
					// Non-fatal: warn but don't fail the upgrade.
					fmt.Fprintf(os.Stderr, "warning: could not link old home campfire (%s): %v\n", oldHomeID[:12], linkErr)
				}
			}
		}

		// Step 5: Output result.
		if jsonOutput {
			out := map[string]interface{}{
				"status":      "upgraded",
				"campfire_id": newCampfireID,
			}
			if oldHomeID != "" {
				out["old_home_id"] = oldHomeID
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			return enc.Encode(out)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Identity upgraded. New address: %s\n", newCampfireID)
		return nil
	},
}

// isUpgradeIdentityGenesis checks if message 0 of a campfire is a campfire-key-signed
// identity convention declaration. This is the same check as isIdentityCampfireGenesis
// in pkg/protocol/disband.go, inlined here to avoid import coupling.
func isUpgradeIdentityGenesis(campfireID, senderHex string, payload []byte) bool {
	if senderHex != campfireID {
		return false
	}
	var decl struct {
		Convention string `json:"convention"`
	}
	if err := json.Unmarshal(payload, &decl); err != nil {
		return false
	}
	return decl.Convention == "identity"
}

// performHomeLinkCeremony links oldHomeID to newCampfireID via the declare-home ceremony.
// This is the same 3-step ceremony as `cf home link` but adapted for the upgrade path:
//   1. Post declare-home(old) on new self-campfire → M_new
//   2. Post declare-home(new) on old home campfire → M_old
//   3. Post echo message on new self-campfire (signed by old campfire's private key)
//
// If the agent is not a member of the old campfire or the old campfire has no local
// private key, the function returns an error (caller treats as non-fatal warning).
func performHomeLinkCeremony(cmd *cobra.Command, s store.Store, cfHome string, newCampfireID, oldHomeID string) error {
	// Verify agent is a member of both campfires.
	mNew, err := s.GetMembership(newCampfireID)
	if err != nil || mNew == nil {
		return fmt.Errorf("not a member of new self-campfire %s", newCampfireID[:12])
	}
	mOld, err := s.GetMembership(oldHomeID)
	if err != nil || mOld == nil {
		return fmt.Errorf("not a member of old home campfire %s (skipping link)", oldHomeID[:12])
	}

	// We need old campfire's private key for the echo signature.
	trOld := fs.ForDir(mOld.TransportDir)
	stateOld, err := trOld.ReadState(oldHomeID)
	if err != nil {
		return fmt.Errorf("reading old home campfire state: %w", err)
	}
	if len(stateOld.PrivateKey) == 0 {
		return fmt.Errorf("old home campfire (%s) has no local private key — echo ceremony requires threshold=1 filesystem campfire", oldHomeID[:12])
	}

	// Use cf home link logic via homeLinkCeremony helper.
	// We update the "home" alias to newCampfireID so homeLinkCmd's resolver finds it.
	// However, we call the ceremony directly to avoid alias dependency.
	//
	// The ceremony: declare-home on new pointing to old, declare-home on old pointing to new, echo.
	// We use the same approach as homeLinkCmd.RunE but with explicit IDs (A=new, B=old).
	aliases := naming.NewAliasStore(cfHome)
	// Temporarily the alias is already set to newCampfireID (by createSelfCampfire above).
	_ = aliases

	// Delegate to homeLinkCmd's mechanism by setting args and re-executing.
	// But that would require a cobra execution cycle. Instead, call the shared internals.
	// The home.go homeLinkCmd.RunE resolves campfireAID from alias "home" (which is now newCampfireID)
	// and campfireBID from the argument. We run it as a sub-invocation.
	//
	// Since homeLinkCmd.RunE references requireAgentAndStore() (which re-opens the store) and
	// the alias is already correct, we invoke homeLinkCmd programmatically.
	homeLinkCmd.SetArgs([]string{oldHomeID})
	if err := homeLinkCmd.RunE(homeLinkCmd, []string{oldHomeID}); err != nil {
		return fmt.Errorf("home link ceremony: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "linked old home campfire %s\n", oldHomeID[:12])
	return nil
}

func init() {
	identityCmd.AddCommand(identityUpgradeCmd)
}
