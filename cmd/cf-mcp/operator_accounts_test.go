// cmd/cf-mcp/operator_accounts_test.go
//
// Tests for the Forge account auto-creation flow (forgeAccountManager).
// Uses a mock Forge client and in-memory OperatorAccountStore.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
	"github.com/campfire-net/campfire/pkg/store/aztable"
)

// ---------------------------------------------------------------------------
// Mock Forge HTTP server helpers
// ---------------------------------------------------------------------------

// mockForgeServer builds a minimal fake Forge API that records calls.
type mockForgeServer struct {
	createCalls int32
	creditCalls int32
	// accountIDToReturn is the account ID returned by CreateSubAccount.
	accountIDToReturn string
	// createErr, if non-zero, returns this HTTP status on CreateSubAccount.
	createErrStatus int
	// creditErr, if non-zero, returns this HTTP status on CreditAccount.
	creditErrStatus int
}

func (m *mockForgeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		atomic.AddInt32(&m.createCalls, 1)
		if m.createErrStatus != 0 {
			http.Error(w, "forced error", m.createErrStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"account_id": m.accountIDToReturn,
			"name":       "campfire-op-test",
		})
	})
	mux.HandleFunc("/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		// Matches /v1/accounts/{id}/credit
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		atomic.AddInt32(&m.creditCalls, 1)
		if m.creditErrStatus != 0 {
			http.Error(w, "forced error", m.creditErrStatus)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// newMockForgeManager creates a forgeAccountManager backed by a mock Forge server.
func newMockForgeManager(t *testing.T, mock *mockForgeServer, store aztable.OperatorAccountStore) (*forgeAccountManager, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(mock.handler())
	t.Cleanup(ts.Close)
	mgr := &forgeAccountManager{
		forge: &forge.Client{
			BaseURL:     ts.URL,
			ServiceKey:  "forge-sk-test",
			RetryDelays: []time.Duration{time.Millisecond}, // fast single retry in tests
		},
		store:           store,
		parentAccountID: "campfire-hosting",
	}
	return mgr, ts
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestEnsureOperatorAccount_NewOperator verifies the happy path:
// new operator → CreateSubAccount called → credit applied → mapping stored.
func TestEnsureOperatorAccount_NewOperator(t *testing.T) {
	mock := &mockForgeServer{accountIDToReturn: "forge-acct-001"}
	store := aztable.NewMemoryOperatorAccountStore()
	mgr, _ := newMockForgeManager(t, mock, store)

	pubkey := "aabbccddeeff1122"
	accountID, err := mgr.EnsureOperatorAccount(context.Background(), pubkey)
	if err != nil {
		t.Fatalf("EnsureOperatorAccount: %v", err)
	}
	if accountID != "forge-acct-001" {
		t.Errorf("accountID: got %q, want %q", accountID, "forge-acct-001")
	}

	// CreateSubAccount called exactly once
	if got := atomic.LoadInt32(&mock.createCalls); got != 1 {
		t.Errorf("CreateSubAccount calls: got %d, want 1", got)
	}
	// Credit applied exactly once
	if got := atomic.LoadInt32(&mock.creditCalls); got != 1 {
		t.Errorf("CreditAccount calls: got %d, want 1", got)
	}

	// Mapping stored in store
	acc, err := store.GetOperatorAccount(context.Background(), pubkey)
	if err != nil {
		t.Fatalf("GetOperatorAccount: %v", err)
	}
	if acc == nil {
		t.Fatal("expected account in store, got nil")
	}
	if acc.ForgeAccountID != "forge-acct-001" {
		t.Errorf("stored ForgeAccountID: got %q, want %q", acc.ForgeAccountID, "forge-acct-001")
	}
	if !acc.SignupCreditApplied {
		t.Error("SignupCreditApplied: expected true, got false")
	}
}

// TestEnsureOperatorAccount_Idempotent verifies that a second call does NOT
// create a new Forge account or apply credit again.
func TestEnsureOperatorAccount_Idempotent(t *testing.T) {
	mock := &mockForgeServer{accountIDToReturn: "forge-acct-002"}
	store := aztable.NewMemoryOperatorAccountStore()
	mgr, _ := newMockForgeManager(t, mock, store)

	pubkey := "112233445566"

	// First call
	if _, err := mgr.EnsureOperatorAccount(context.Background(), pubkey); err != nil {
		t.Fatalf("first EnsureOperatorAccount: %v", err)
	}

	// Second call
	accountID, err := mgr.EnsureOperatorAccount(context.Background(), pubkey)
	if err != nil {
		t.Fatalf("second EnsureOperatorAccount: %v", err)
	}
	if accountID != "forge-acct-002" {
		t.Errorf("accountID on second call: got %q, want %q", accountID, "forge-acct-002")
	}

	// CreateSubAccount called only once despite two EnsureOperatorAccount calls
	if got := atomic.LoadInt32(&mock.createCalls); got != 1 {
		t.Errorf("CreateSubAccount calls: got %d, want 1 (second call should be idempotent)", got)
	}
	// Credit applied only once
	if got := atomic.LoadInt32(&mock.creditCalls); got != 1 {
		t.Errorf("CreditAccount calls: got %d, want 1 (credit must not be doubled)", got)
	}
}

// TestEnsureOperatorAccount_DeferredCredit verifies that if an account exists
// in the store but credit was not yet applied, EnsureOperatorAccount retries
// the credit without creating a new Forge account.
func TestEnsureOperatorAccount_DeferredCredit(t *testing.T) {
	mock := &mockForgeServer{accountIDToReturn: "forge-acct-003"}
	store := aztable.NewMemoryOperatorAccountStore()
	mgr, _ := newMockForgeManager(t, mock, store)

	pubkey := "aabbccddeeff9988"

	// Pre-seed an account with credit NOT applied (simulates crash mid-provisioning).
	if err := store.CreateOperatorAccount(context.Background(), &aztable.OperatorAccount{
		PubkeyHex:           pubkey,
		ForgeAccountID:      "forge-acct-003",
		CreatedAt:           time.Now(),
		SignupCreditApplied: false,
	}); err != nil {
		t.Fatalf("pre-seed CreateOperatorAccount: %v", err)
	}

	accountID, err := mgr.EnsureOperatorAccount(context.Background(), pubkey)
	if err != nil {
		t.Fatalf("EnsureOperatorAccount: %v", err)
	}
	if accountID != "forge-acct-003" {
		t.Errorf("accountID: got %q, want %q", accountID, "forge-acct-003")
	}

	// No new account created (account already exists in store)
	if got := atomic.LoadInt32(&mock.createCalls); got != 0 {
		t.Errorf("CreateSubAccount calls: got %d, want 0", got)
	}
	// Credit applied to cover the deferred case
	if got := atomic.LoadInt32(&mock.creditCalls); got != 1 {
		t.Errorf("CreditAccount calls: got %d, want 1 (deferred credit must be applied)", got)
	}

	// Flag now set in store
	acc, _ := store.GetOperatorAccount(context.Background(), pubkey)
	if acc == nil || !acc.SignupCreditApplied {
		t.Error("SignupCreditApplied: expected true after deferred credit, got false")
	}
}

// TestEnsureOperatorAccount_ForgeError verifies that a Forge API error is
// returned and no mapping is stored.
func TestEnsureOperatorAccount_ForgeError(t *testing.T) {
	mock := &mockForgeServer{createErrStatus: http.StatusInternalServerError}
	store := aztable.NewMemoryOperatorAccountStore()
	mgr, _ := newMockForgeManager(t, mock, store)

	pubkey := "deadbeef0011"
	_, err := mgr.EnsureOperatorAccount(context.Background(), pubkey)
	if err == nil {
		t.Fatal("expected error when Forge returns 500, got nil")
	}

	// Verify nothing was stored
	acc, storeErr := store.GetOperatorAccount(context.Background(), pubkey)
	if storeErr != nil {
		t.Fatalf("GetOperatorAccount: %v", storeErr)
	}
	if acc != nil {
		t.Errorf("expected no stored account on Forge error, got %+v", acc)
	}
}

// TestNewForgeAccountManager_NoKey verifies that newForgeAccountManager returns
// nil when FORGE_SERVICE_KEY is unset (development mode).
func TestNewForgeAccountManager_NoKey(t *testing.T) {
	// Ensure the env var is not set.
	t.Setenv("FORGE_SERVICE_KEY", "")
	store := aztable.NewMemoryOperatorAccountStore()
	mgr := newForgeAccountManager(store)
	if mgr != nil {
		t.Errorf("expected nil manager when FORGE_SERVICE_KEY is empty, got %+v", mgr)
	}
}

// TestNewForgeAccountManager_WithKey verifies that newForgeAccountManager returns
// a non-nil manager when FORGE_SERVICE_KEY is set.
func TestNewForgeAccountManager_WithKey(t *testing.T) {
	t.Setenv("FORGE_SERVICE_KEY", "forge-sk-testkey")
	t.Setenv("FORGE_BASE_URL", "https://forge.example.com")
	store := aztable.NewMemoryOperatorAccountStore()
	mgr := newForgeAccountManager(store)
	if mgr == nil {
		t.Fatal("expected non-nil manager when FORGE_SERVICE_KEY is set")
	}
	if mgr.forge.BaseURL != "https://forge.example.com" {
		t.Errorf("BaseURL: got %q, want %q", mgr.forge.BaseURL, "https://forge.example.com")
	}
	if mgr.forge.ServiceKey != "forge-sk-testkey" {
		t.Errorf("ServiceKey: got %q", mgr.forge.ServiceKey)
	}
}

// TestEnsureOperatorAccount_CreditErrorNonFatal verifies that a credit error does
// not prevent the account ID from being returned.
func TestEnsureOperatorAccount_CreditErrorNonFatal(t *testing.T) {
	mock := &mockForgeServer{
		accountIDToReturn: "forge-acct-noncredit",
		creditErrStatus:   http.StatusInternalServerError,
	}
	store := aztable.NewMemoryOperatorAccountStore()
	mgr, _ := newMockForgeManager(t, mock, store)

	pubkey := "cc00ff112233"
	accountID, err := mgr.EnsureOperatorAccount(context.Background(), pubkey)
	if err != nil {
		t.Fatalf("EnsureOperatorAccount: expected non-fatal credit error, got: %v", err)
	}
	if accountID != "forge-acct-noncredit" {
		t.Errorf("accountID: got %q, want %q", accountID, "forge-acct-noncredit")
	}

	// Mapping should still be stored even though credit failed
	acc, _ := store.GetOperatorAccount(context.Background(), pubkey)
	if acc == nil {
		t.Fatal("expected stored account even after credit error, got nil")
	}
	// Credit flag NOT set (credit failed)
	if acc.SignupCreditApplied {
		t.Error("SignupCreditApplied should be false when credit failed")
	}
}

// ---------------------------------------------------------------------------
// handleInit integration: verify forgeAccounts field is wired and called
// ---------------------------------------------------------------------------

// callCountStore wraps MemoryOperatorAccountStore and counts EnsureOperatorAccount
// invocations indirectly by counting GetOperatorAccount calls.
type callCountStore struct {
	*aztable.MemoryOperatorAccountStore
	getCalls int32
}

func (c *callCountStore) GetOperatorAccount(ctx context.Context, pubkeyHex string) (*aztable.OperatorAccount, error) {
	atomic.AddInt32(&c.getCalls, 1)
	return c.MemoryOperatorAccountStore.GetOperatorAccount(ctx, pubkeyHex)
}

// TestHandleInit_ForgeAccountsWired verifies that when forgeAccounts is set on
// the server, handleInit triggers EnsureOperatorAccount (observed via a Forge
// server call). Uses a real test server and waits briefly for the goroutine.
func TestHandleInit_ForgeAccountsWired(t *testing.T) {
	createCalls := int32(0)
	fakeForgeSvr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts" {
			atomic.AddInt32(&createCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"account_id": "forge-wired-001"}) //nolint:errcheck
			return
		}
		// /v1/accounts/{id}/credit
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(fakeForgeSvr.Close)

	memStore := aztable.NewMemoryOperatorAccountStore()
	mgr := &forgeAccountManager{
		forge: &forge.Client{
			BaseURL:     fakeForgeSvr.URL,
			ServiceKey:  "forge-sk-test",
			RetryDelays: []time.Duration{},
		},
		store:           memStore,
		parentAccountID: "campfire-hosting",
	}

	srv := newTestServer(t)
	srv.forgeAccounts = mgr

	resp := srv.handleInit(1, map[string]interface{}{})
	if resp.Error != nil {
		t.Fatalf("handleInit error: %+v", resp.Error)
	}

	// Give the background goroutine time to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&createCalls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&createCalls); got == 0 {
		t.Error("expected at least one CreateSubAccount call from handleInit, got 0")
	}
}

// Verify that an error in fmt.Sprintf is surfaced — just a compile-time
// sanity check that the forge package types are accessible.
var _ = fmt.Sprintf
