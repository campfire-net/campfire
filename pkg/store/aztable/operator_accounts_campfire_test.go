package aztable

import (
	"context"
	"testing"
	"time"
)

// TestMemoryOperatorAccount_DualKeyLookup_WithCampfireID verifies that an account
// created with a CampfireID can be retrieved by both pubkey and campfire ID.
func TestMemoryOperatorAccount_DualKeyLookup_WithCampfireID(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryOperatorAccountStore()

	account := &OperatorAccount{
		PubkeyHex:           "deadbeef1234",
		CampfireID:          "cafebabe5678",
		ForgeAccountID:      "forge-account-1",
		CreatedAt:           time.Now(),
		SignupCreditApplied: false,
	}

	if err := s.CreateOperatorAccount(ctx, account); err != nil {
		t.Fatalf("CreateOperatorAccount: %v", err)
	}

	// Lookup by pubkey — must work.
	byPub, err := s.GetOperatorAccount(ctx, "deadbeef1234")
	if err != nil {
		t.Fatalf("GetOperatorAccount(pubkey): %v", err)
	}
	if byPub == nil {
		t.Fatal("GetOperatorAccount(pubkey): expected non-nil, got nil")
	}
	if byPub.ForgeAccountID != "forge-account-1" {
		t.Errorf("ForgeAccountID: got %q, want %q", byPub.ForgeAccountID, "forge-account-1")
	}
	if byPub.CampfireID != "cafebabe5678" {
		t.Errorf("CampfireID: got %q, want %q", byPub.CampfireID, "cafebabe5678")
	}

	// Lookup by campfire ID — must work.
	byCF, err := s.GetOperatorAccountByCampfireID(ctx, "cafebabe5678")
	if err != nil {
		t.Fatalf("GetOperatorAccountByCampfireID: %v", err)
	}
	if byCF == nil {
		t.Fatal("GetOperatorAccountByCampfireID: expected non-nil, got nil")
	}
	if byCF.ForgeAccountID != "forge-account-1" {
		t.Errorf("ForgeAccountID (by campfire ID): got %q, want %q", byCF.ForgeAccountID, "forge-account-1")
	}
	if byCF.PubkeyHex != "deadbeef1234" {
		t.Errorf("PubkeyHex (by campfire ID): got %q, want %q", byCF.PubkeyHex, "deadbeef1234")
	}
}

// TestMemoryOperatorAccount_DualKeyLookup_LegacyAccount verifies that an account
// without a CampfireID (legacy) returns nil from GetOperatorAccountByCampfireID.
func TestMemoryOperatorAccount_DualKeyLookup_LegacyAccount(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryOperatorAccountStore()

	account := &OperatorAccount{
		PubkeyHex:      "legacypubkey",
		CampfireID:     "", // not set — legacy account
		ForgeAccountID: "forge-legacy",
		CreatedAt:      time.Now(),
	}

	if err := s.CreateOperatorAccount(ctx, account); err != nil {
		t.Fatalf("CreateOperatorAccount: %v", err)
	}

	// Lookup by pubkey — must work.
	byPub, err := s.GetOperatorAccount(ctx, "legacypubkey")
	if err != nil {
		t.Fatalf("GetOperatorAccount(pubkey): %v", err)
	}
	if byPub == nil {
		t.Fatal("GetOperatorAccount(pubkey): expected non-nil for legacy account")
	}

	// Lookup by campfire ID — must return nil (no secondary index).
	byCF, err := s.GetOperatorAccountByCampfireID(ctx, "somecampfireid")
	if err != nil {
		t.Fatalf("GetOperatorAccountByCampfireID: %v", err)
	}
	if byCF != nil {
		t.Errorf("GetOperatorAccountByCampfireID: expected nil for legacy account, got %+v", byCF)
	}
}

// TestMemoryOperatorAccount_GetByCampfireID_NotFound verifies (nil, nil) for unknown campfire ID.
func TestMemoryOperatorAccount_GetByCampfireID_NotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryOperatorAccountStore()

	got, err := s.GetOperatorAccountByCampfireID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetOperatorAccountByCampfireID: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for unknown campfire ID, got %+v", got)
	}
}

// TestMemoryOperatorAccount_GetByCampfireID_EmptyID verifies (nil, nil) for empty campfire ID.
func TestMemoryOperatorAccount_GetByCampfireID_EmptyID(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryOperatorAccountStore()

	got, err := s.GetOperatorAccountByCampfireID(ctx, "")
	if err != nil {
		t.Fatalf("GetOperatorAccountByCampfireID(empty): %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty campfire ID, got %+v", got)
	}
}

// TestMemoryOperatorAccount_Idempotent_WithCampfireID verifies CreateOperatorAccount
// is idempotent when an account with CampfireID already exists.
func TestMemoryOperatorAccount_Idempotent_WithCampfireID(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryOperatorAccountStore()

	account := &OperatorAccount{
		PubkeyHex:      "pubkey1",
		CampfireID:     "campfire1",
		ForgeAccountID: "forge1",
		CreatedAt:      time.Now(),
	}

	if err := s.CreateOperatorAccount(ctx, account); err != nil {
		t.Fatalf("first CreateOperatorAccount: %v", err)
	}

	// Second create with different ForgeAccountID — should be idempotent (first wins).
	account2 := &OperatorAccount{
		PubkeyHex:      "pubkey1",
		CampfireID:     "campfire1",
		ForgeAccountID: "forge-different",
		CreatedAt:      time.Now(),
	}
	if err := s.CreateOperatorAccount(ctx, account2); err != nil {
		t.Fatalf("second CreateOperatorAccount: %v", err)
	}

	// Must return original forge account ID.
	got, err := s.GetOperatorAccount(ctx, "pubkey1")
	if err != nil {
		t.Fatalf("GetOperatorAccount: %v", err)
	}
	if got.ForgeAccountID != "forge1" {
		t.Errorf("ForgeAccountID: got %q, want %q (idempotent — first insert wins)", got.ForgeAccountID, "forge1")
	}
}
