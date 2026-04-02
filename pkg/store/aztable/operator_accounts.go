// Package aztable — operator_accounts.go
//
// CampfireOperatorAccounts table provides durable operator-to-Forge account
// mapping. When a new operator calls campfire_init, a Forge sub-account is
// auto-created and the mapping is persisted here so it survives instance hops.
//
// Table: CampfireOperatorAccounts
//   PK = encodeKey(pubkeyHex)
//   RK = "account"
package aztable

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// OperatorAccount is the mapping between an operator's Ed25519 public key and
// their Forge sub-account ID.
type OperatorAccount struct {
	PubkeyHex           string
	// CampfireID is the hex-encoded self-campfire ID of the operator (identity address).
	// Empty for legacy accounts created before SenderCampfireID was introduced.
	// When set, enables campfire-ID-based lookup via GetOperatorAccountByCampfireID.
	CampfireID          string
	ForgeAccountID      string
	CreatedAt           time.Time
	SignupCreditApplied bool
}

// OperatorAccountStore is the interface for operator account persistence.
// The aztable implementation persists to Azure Table Storage; a memory
// implementation is provided for tests.
type OperatorAccountStore interface {
	// GetOperatorAccount looks up the Forge account for an operator by public key.
	// Returns (nil, nil) if no account exists.
	GetOperatorAccount(ctx context.Context, pubkeyHex string) (*OperatorAccount, error)

	// GetOperatorAccountByCampfireID looks up the Forge account for an operator
	// by their self-campfire ID. Returns (nil, nil) if no account is found for
	// this campfire ID (e.g., legacy account or campfire ID not yet registered).
	GetOperatorAccountByCampfireID(ctx context.Context, campfireID string) (*OperatorAccount, error)

	// CreateOperatorAccount persists the operator-to-Forge mapping.
	// Insert-if-not-exists: returns nil if the account already exists (idempotent).
	// When account.CampfireID is non-empty, a secondary index entity is also created
	// to enable GetOperatorAccountByCampfireID lookups.
	CreateOperatorAccount(ctx context.Context, account *OperatorAccount) error

	// MarkSignupCreditApplied sets SignupCreditApplied=true for the given operator.
	// Idempotent: safe to call multiple times.
	MarkSignupCreditApplied(ctx context.Context, pubkeyHex string) error
}

// operatorAccountsTable is the Azure Table Storage table name.
const operatorAccountsTable = "CampfireOperatorAccounts"

// operatorAccountsRowKey is the fixed row key for operator account entities.
const operatorAccountsRowKey = "account"

// operatorAccountsByCampfireRowKey is the row key for the secondary index entity
// that maps campfire ID → pubkeyHex. PK = encodeKey(campfireID), RK = this constant.
const operatorAccountsByCampfireRowKey = "account-by-campfire"

// WithOperatorAccounts adds the CampfireOperatorAccounts table to an existing
// TableStore service client and returns an OperatorAccountStore backed by
// Azure Table Storage.
//
// This is called from main.go when AZURE_STORAGE_CONNECTION_STRING is set,
// immediately after the TableStore is created.
func NewOperatorAccountStore(connectionString string) (OperatorAccountStore, error) {
	svc, err := aztables.NewServiceClientFromConnectionString(connectionString, nil)
	if err != nil {
		return nil, fmt.Errorf("aztable: OperatorAccountStore: creating service client: %w", err)
	}
	client := svc.NewClient(operatorAccountsTable)
	ctx := context.Background()
	_, createErr := client.CreateTable(ctx, nil)
	if createErr != nil && !isTableExistsError(createErr) {
		return nil, fmt.Errorf("aztable: OperatorAccountStore: ensuring table %s: %w", operatorAccountsTable, createErr)
	}
	return &tableOperatorAccountStore{client: client}, nil
}

// tableOperatorAccountStore implements OperatorAccountStore against Azure Table Storage.
type tableOperatorAccountStore struct {
	client *aztables.Client
}

// GetOperatorAccount looks up the operator's account by public key.
// Returns (nil, nil) if not found.
func (s *tableOperatorAccountStore) GetOperatorAccount(ctx context.Context, pubkeyHex string) (*OperatorAccount, error) {
	pk := encodeKey(pubkeyHex)
	raw, err := getEntity(ctx, s.client, pk, operatorAccountsRowKey)
	if err != nil {
		return nil, fmt.Errorf("aztable: GetOperatorAccount: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	creditApplied := toInt64(raw["SignupCreditApplied"]) != 0
	return &OperatorAccount{
		PubkeyHex:           str(raw, "PubkeyHex"),
		CampfireID:          str(raw, "CampfireID"),
		ForgeAccountID:      str(raw, "ForgeAccountID"),
		CreatedAt:           time.Unix(0, toInt64(raw["CreatedAtNs"])),
		SignupCreditApplied: creditApplied,
	}, nil
}

// GetOperatorAccountByCampfireID looks up the operator's account by their self-campfire ID.
// Returns (nil, nil) if no account is registered for this campfire ID.
//
// Implementation: reads a secondary index entity (PK=encodeKey(campfireID), RK="account-by-campfire")
// to get the pubkeyHex, then fetches the primary account entity. This is a two-hop lookup
// but avoids table scans. Returns (nil, nil) for legacy accounts (no campfire ID).
func (s *tableOperatorAccountStore) GetOperatorAccountByCampfireID(ctx context.Context, campfireID string) (*OperatorAccount, error) {
	if campfireID == "" {
		return nil, nil
	}
	// Step 1: read secondary index to find the pubkeyHex.
	pk := encodeKey(campfireID)
	raw, err := getEntity(ctx, s.client, pk, operatorAccountsByCampfireRowKey)
	if err != nil {
		return nil, fmt.Errorf("aztable: GetOperatorAccountByCampfireID: secondary index lookup: %w", err)
	}
	if raw == nil {
		return nil, nil // no account for this campfire ID
	}
	pubkeyHex := str(raw, "PubkeyHex")
	if pubkeyHex == "" {
		return nil, nil
	}
	// Step 2: fetch the primary account entity.
	return s.GetOperatorAccount(ctx, pubkeyHex)
}

// CreateOperatorAccount inserts the mapping if it does not already exist.
// Returns nil if the account already exists (idempotent).
//
// When account.CampfireID is non-empty, a secondary index entity is also written
// to enable GetOperatorAccountByCampfireID lookups. The two entities are in
// different partitions (PK=pubkey vs PK=campfireID), so they cannot be written
// in a single batch transaction. The secondary entity is written with eventual
// consistency: if the primary succeeds but the secondary fails, the campfire-ID
// lookup falls back to nil (safe degradation). Callers should retry on failure.
func (s *tableOperatorAccountStore) CreateOperatorAccount(ctx context.Context, account *OperatorAccount) error {
	creditApplied := int64(0)
	if account.SignupCreditApplied {
		creditApplied = 1
	}
	entity := map[string]any{
		"PartitionKey":        encodeKey(account.PubkeyHex),
		"RowKey":              operatorAccountsRowKey,
		"PubkeyHex":           account.PubkeyHex,
		"CampfireID":          account.CampfireID,
		"ForgeAccountID":      account.ForgeAccountID,
		"CreatedAtNs":         account.CreatedAt.UnixNano(),
		"SignupCreditApplied": creditApplied,
	}
	if err := insertEntity(ctx, s.client, entity); err != nil {
		return fmt.Errorf("aztable: CreateOperatorAccount: %w", err)
	}

	// Write secondary index entity for campfire-ID-based lookup.
	// This is a best-effort write: failure is logged but not fatal.
	if account.CampfireID != "" {
		secondary := map[string]any{
			"PartitionKey": encodeKey(account.CampfireID),
			"RowKey":       operatorAccountsByCampfireRowKey,
			"PubkeyHex":    account.PubkeyHex,
			"CampfireID":   account.CampfireID,
		}
		if err := insertEntity(ctx, s.client, secondary); err != nil {
			return fmt.Errorf("aztable: CreateOperatorAccount: secondary index: %w", err)
		}
	}
	return nil
}

// MarkSignupCreditApplied sets SignupCreditApplied=true for the operator.
// Reads the current entity to preserve other fields, then upserts with the flag set.
// Idempotent: safe to call multiple times.
func (s *tableOperatorAccountStore) MarkSignupCreditApplied(ctx context.Context, pubkeyHex string) error {
	pk := encodeKey(pubkeyHex)
	raw, err := getEntity(ctx, s.client, pk, operatorAccountsRowKey)
	if err != nil {
		return fmt.Errorf("aztable: MarkSignupCreditApplied: get: %w", err)
	}
	if raw == nil {
		return fmt.Errorf("aztable: MarkSignupCreditApplied: no account found for %s", pubkeyHex)
	}
	raw["SignupCreditApplied"] = int64(1)
	if err := upsertEntity(ctx, s.client, raw); err != nil {
		return fmt.Errorf("aztable: MarkSignupCreditApplied: upsert: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// In-memory implementation for tests
// ---------------------------------------------------------------------------

// MemoryOperatorAccountStore is a thread-safe in-memory OperatorAccountStore
// for use in tests. It does not require Azure Table Storage.
type MemoryOperatorAccountStore struct {
	mu         sync.RWMutex
	accounts   map[string]*OperatorAccount // pubkeyHex → account
	byCampfire map[string]string           // campfireID → pubkeyHex (secondary index)
}

// NewMemoryOperatorAccountStore returns an empty MemoryOperatorAccountStore.
func NewMemoryOperatorAccountStore() *MemoryOperatorAccountStore {
	return &MemoryOperatorAccountStore{
		accounts:   make(map[string]*OperatorAccount),
		byCampfire: make(map[string]string),
	}
}

// GetOperatorAccount returns the account for pubkeyHex, or (nil, nil) if not found.
func (m *MemoryOperatorAccountStore) GetOperatorAccount(_ context.Context, pubkeyHex string) (*OperatorAccount, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	acc, ok := m.accounts[pubkeyHex]
	if !ok {
		return nil, nil
	}
	// Return a copy to avoid aliasing.
	copy := *acc
	return &copy, nil
}

// GetOperatorAccountByCampfireID returns the account for campfireID, or (nil, nil) if not found.
func (m *MemoryOperatorAccountStore) GetOperatorAccountByCampfireID(_ context.Context, campfireID string) (*OperatorAccount, error) {
	if campfireID == "" {
		return nil, nil
	}
	m.mu.RLock()
	pubkeyHex, ok := m.byCampfire[campfireID]
	if !ok {
		m.mu.RUnlock()
		return nil, nil
	}
	acc, ok := m.accounts[pubkeyHex]
	m.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	copy := *acc
	return &copy, nil
}

// CreateOperatorAccount inserts the account if it does not already exist.
// Returns nil if the account already exists (idempotent).
// Also registers a campfire-ID secondary index when account.CampfireID is set.
func (m *MemoryOperatorAccountStore) CreateOperatorAccount(_ context.Context, account *OperatorAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.accounts[account.PubkeyHex]; exists {
		return nil // idempotent
	}
	copy := *account
	m.accounts[account.PubkeyHex] = &copy
	if account.CampfireID != "" {
		m.byCampfire[account.CampfireID] = account.PubkeyHex
	}
	return nil
}

// MarkSignupCreditApplied sets SignupCreditApplied=true for the operator.
func (m *MemoryOperatorAccountStore) MarkSignupCreditApplied(_ context.Context, pubkeyHex string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[pubkeyHex]
	if !ok {
		return fmt.Errorf("memory: MarkSignupCreditApplied: no account found for %s", pubkeyHex)
	}
	acc.SignupCreditApplied = true
	return nil
}
