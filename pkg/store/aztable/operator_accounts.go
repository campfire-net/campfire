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

	// CreateOperatorAccount persists the operator-to-Forge mapping.
	// Insert-if-not-exists: returns nil if the account already exists (idempotent).
	CreateOperatorAccount(ctx context.Context, account *OperatorAccount) error

	// MarkSignupCreditApplied sets SignupCreditApplied=true for the given operator.
	// Idempotent: safe to call multiple times.
	MarkSignupCreditApplied(ctx context.Context, pubkeyHex string) error
}

// operatorAccountsTable is the Azure Table Storage table name.
const operatorAccountsTable = "CampfireOperatorAccounts"

// operatorAccountsRowKey is the fixed row key for operator account entities.
const operatorAccountsRowKey = "account"

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
		ForgeAccountID:      str(raw, "ForgeAccountID"),
		CreatedAt:           time.Unix(0, toInt64(raw["CreatedAtNs"])),
		SignupCreditApplied: creditApplied,
	}, nil
}

// CreateOperatorAccount inserts the mapping if it does not already exist.
// Returns nil if the account already exists (idempotent).
func (s *tableOperatorAccountStore) CreateOperatorAccount(ctx context.Context, account *OperatorAccount) error {
	creditApplied := int64(0)
	if account.SignupCreditApplied {
		creditApplied = 1
	}
	entity := map[string]any{
		"PartitionKey":        encodeKey(account.PubkeyHex),
		"RowKey":              operatorAccountsRowKey,
		"PubkeyHex":           account.PubkeyHex,
		"ForgeAccountID":      account.ForgeAccountID,
		"CreatedAtNs":         account.CreatedAt.UnixNano(),
		"SignupCreditApplied": creditApplied,
	}
	if err := insertEntity(ctx, s.client, entity); err != nil {
		return fmt.Errorf("aztable: CreateOperatorAccount: %w", err)
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
	mu       sync.RWMutex
	accounts map[string]*OperatorAccount // pubkeyHex → account
}

// NewMemoryOperatorAccountStore returns an empty MemoryOperatorAccountStore.
func NewMemoryOperatorAccountStore() *MemoryOperatorAccountStore {
	return &MemoryOperatorAccountStore{
		accounts: make(map[string]*OperatorAccount),
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

// CreateOperatorAccount inserts the account if it does not already exist.
// Returns nil if the account already exists (idempotent).
func (m *MemoryOperatorAccountStore) CreateOperatorAccount(_ context.Context, account *OperatorAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.accounts[account.PubkeyHex]; exists {
		return nil // idempotent
	}
	copy := *account
	m.accounts[account.PubkeyHex] = &copy
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
