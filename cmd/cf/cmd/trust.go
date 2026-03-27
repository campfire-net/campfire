package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/trust"
	"github.com/spf13/cobra"
)

// trustCmd is the parent command for local trust state operations.
//
//	cf trust show    — display adopted conventions, sources, fingerprints
//	cf trust reset   — clear TOFU pins (scoped by campfire, convention, or all)
var trustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Manage local trust policy (adopted conventions and TOFU pins)",
	Long: `Inspect and manage the local trust policy.

  cf trust show            display adopted conventions and TOFU pin status
  cf trust reset --all     clear all TOFU pins (requires confirmation)`,
}

func init() {
	rootCmd.AddCommand(trustCmd)
}

// loadPinStore opens the TOFU pin store from the agent's identity home.
// The HMAC key is derived from the agent's private key for tamper detection.
func loadPinStore() (*trust.PinStore, error) {
	agentID, err := identity.Load(IdentityPath())
	if err != nil {
		return nil, fmt.Errorf("loading identity: %w", err)
	}
	pinPath := filepath.Join(CFHome(), "pins.json")
	return trust.NewPinStore(pinPath, agentID.PrivateKey)
}

// loadLocalPolicyEngine builds a PolicyEngine seeded with the agent's home
// campfire declarations. Returns a new, initialized-but-empty engine on error
// (best-effort: missing home campfire is not fatal for join).
//
// The home campfire's convention:operation messages are the agent's locally
// adopted conventions per Trust Convention v0.2 §4.
func loadLocalPolicyEngine(s store.Store) *trust.PolicyEngine {
	engine := trust.NewPolicyEngine()

	// Resolve "home" alias to get the home campfire ID.
	homeID, err := resolveCampfireID("home", s)
	if err != nil {
		// No home campfire — engine usable but all statuses will be "unknown".
		engine.MarkInitialized()
		return engine
	}

	// Read declarations from the home campfire.
	decls, err := listConventionOperations(context.Background(), s, homeID)
	if err != nil || len(decls) == 0 {
		engine.MarkInitialized()
		return engine
	}

	// Seed the engine with home campfire declarations (local policy).
	engine.SeedConventions(decls)
	return engine
}
