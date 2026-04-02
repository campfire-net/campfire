// cmd/cf-mcp/operator_accounts.go — Forge account auto-creation at campfire_init
//
// When a new operator calls campfire_init for the first time, cf-mcp creates a
// Forge sub-account under the campfire-hosting parent account and applies a
// $10 signup credit. The mapping is persisted to CampfireOperatorAccounts so
// subsequent inits are idempotent and the account ID is available for metering.
//
// Environment variables consumed at startup:
//   FORGE_SERVICE_KEY — forge-sk-* service key (required for account creation)
//   FORGE_BASE_URL    — Forge API base URL, e.g. "https://forge.example.com"
//   FORGE_PARENT_ACCOUNT_ID — parent Forge account ID for sub-account nesting
//
// When FORGE_SERVICE_KEY is unset, account auto-creation is silently skipped
// (development / stdio mode). Production (Azure Functions) must set the key.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
	"github.com/campfire-net/campfire/pkg/store/aztable"
	"golang.org/x/sync/singleflight"
)

// signupCreditMicroUSD is the one-time signup credit amount in micro-USD.
// 10_000_000 micro-USD = $10.00.
const signupCreditMicroUSD = 10_000_000

// signupCreditProduct is the product identifier for the signup credit.
const signupCreditProduct = "campfire-signup-credit"

// OperatorAccountStore is the interface for operator account persistence.
// Re-exported from aztable so cmd/cf-mcp code can use it without importing aztable
// directly everywhere.
type OperatorAccountStore = aztable.OperatorAccountStore

// OperatorAccount is the struct for operator account records.
// Re-exported so cmd/cf-mcp code can reference it without importing aztable directly.
type OperatorAccount = aztable.OperatorAccount

// forgeAccountManager holds the Forge client and operator account store used
// during campfire_init to auto-provision Forge sub-accounts.
// It is non-nil only when FORGE_SERVICE_KEY is set.
type forgeAccountManager struct {
	forge           *forge.Client
	store           OperatorAccountStore
	parentAccountID string // Forge parent account ID for sub-account nesting
	ensureGroup     singleflight.Group // dedup concurrent EnsureOperatorAccount calls per key
}

// newForgeAccountManager creates a forgeAccountManager from environment variables.
// Returns nil (no error) when FORGE_SERVICE_KEY is not set — auto-creation is
// silently disabled in that case.
func newForgeAccountManager(store OperatorAccountStore) *forgeAccountManager {
	key := os.Getenv("FORGE_SERVICE_KEY")
	if key == "" {
		return nil
	}
	baseURL := os.Getenv("FORGE_BASE_URL")
	if baseURL == "" {
		baseURL = "https://forge.getcampfire.dev"
	}
	parentAccountID := os.Getenv("FORGE_PARENT_ACCOUNT_ID")
	if parentAccountID == "" {
		// Default parent — campfire-hosting root account. Override in prod.
		parentAccountID = "campfire-hosting"
	}
	return &forgeAccountManager{
		forge: &forge.Client{
			BaseURL:    baseURL,
			ServiceKey: key,
		},
		store:           store,
		parentAccountID: parentAccountID,
	}
}

// EnsureOperatorAccount checks whether the operator already has a Forge account
// and creates one if not. Returns the Forge account ID.
//
// Idempotency: if the mapping row exists, returns the stored account ID without
// calling Forge. If the Forge account was created but the mapping row was not yet
// stored (crash between steps), CreateOperatorAccount is idempotent (insert-if-not-exists)
// and will attempt to re-store on the next call with the same Forge account ID.
//
// The caller should cache the returned account ID in the session to avoid a DB
// round-trip on every request.
func (m *forgeAccountManager) EnsureOperatorAccount(ctx context.Context, pubkeyHex string) (string, error) {
	// singleflight deduplicates concurrent calls for the same operator key,
	// preventing the double-create / double-credit race when two requests
	// arrive before either has persisted the mapping.
	v, err, _ := m.ensureGroup.Do(pubkeyHex, func() (interface{}, error) {
		return m.ensureOperatorAccountOnce(ctx, pubkeyHex)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// ensureOperatorAccountOnce is the actual implementation, called at most once
// per key concurrently thanks to singleflight in EnsureOperatorAccount.
func (m *forgeAccountManager) ensureOperatorAccountOnce(ctx context.Context, pubkeyHex string) (string, error) {
	// 1. Check for existing mapping.
	existing, err := m.store.GetOperatorAccount(ctx, pubkeyHex)
	if err != nil {
		return "", fmt.Errorf("operator accounts: lookup: %w", err)
	}
	if existing != nil {
		// Account already provisioned — apply credit if it was not applied yet.
		// This handles the edge case where cf-mcp crashed after creating the account
		// but before applying the credit.
		if !existing.SignupCreditApplied {
			if creditErr := m.applySignupCredit(ctx, existing.ForgeAccountID, pubkeyHex); creditErr != nil {
				// Log but don't fail — the account exists and is usable.
				log.Printf("operator accounts: warning: could not apply deferred signup credit to %s: %v", pubkeyHex, creditErr)
			}
		}
		return existing.ForgeAccountID, nil
	}

	// 2. Create Forge sub-account.
	accountName := "campfire-op-" + pubkeyHex
	if len(accountName) > 27 {
		// Forge name limit safety: use first 16 chars of pubkey.
		accountName = "campfire-op-" + pubkeyHex[:16]
	}
	acc, err := m.forge.CreateSubAccount(ctx, accountName, m.parentAccountID)
	if err != nil {
		return "", fmt.Errorf("operator accounts: create forge sub-account: %w", err)
	}

	// 3. Persist the mapping (insert-if-not-exists).
	record := &OperatorAccount{
		PubkeyHex:           pubkeyHex,
		ForgeAccountID:      acc.AccountID,
		CreatedAt:           time.Now(),
		SignupCreditApplied: false,
	}
	if err := m.store.CreateOperatorAccount(ctx, record); err != nil {
		// Log but don't fail — the Forge account exists; we'll re-store on next call.
		log.Printf("operator accounts: warning: could not persist account mapping for %s: %v", pubkeyHex, err)
	}

	// 4. Apply signup credit.
	if creditErr := m.applySignupCredit(ctx, acc.AccountID, pubkeyHex); creditErr != nil {
		// Log but don't fail — credit is best-effort.
		log.Printf("operator accounts: warning: could not apply signup credit to %s (forge: %s): %v", pubkeyHex, acc.AccountID, creditErr)
	}

	return acc.AccountID, nil
}

// applySignupCredit credits the Forge account and marks the flag in the store.
func (m *forgeAccountManager) applySignupCredit(ctx context.Context, forgeAccountID, pubkeyHex string) error {
	if err := m.forge.CreditAccount(ctx, forgeAccountID, signupCreditMicroUSD, signupCreditProduct); err != nil {
		return fmt.Errorf("credit forge account: %w", err)
	}
	if err := m.store.MarkSignupCreditApplied(ctx, pubkeyHex); err != nil {
		// Log — not fatal; the credit was applied in Forge, just the flag is stale.
		log.Printf("operator accounts: warning: could not mark signup credit applied for %s: %v", pubkeyHex, err)
	}
	return nil
}
