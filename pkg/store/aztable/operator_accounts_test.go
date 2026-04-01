// Package aztable — operator_accounts_test.go
//
// Unit tests for OperatorAccountStore (memory implementation).
// These tests run without any Azure or Azurite dependency.
package aztable

import (
	"context"
	"testing"
	"time"
)

func TestMemoryOperatorAccountStore_GetNotFound(t *testing.T) {
	s := NewMemoryOperatorAccountStore()
	acc, err := s.GetOperatorAccount(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc != nil {
		t.Fatalf("expected nil, got %+v", acc)
	}
}

func TestMemoryOperatorAccountStore_CreateAndGet(t *testing.T) {
	s := NewMemoryOperatorAccountStore()
	now := time.Now().Truncate(time.Millisecond)
	want := &OperatorAccount{
		PubkeyHex:           "aabbcc",
		ForgeAccountID:      "forge-001",
		CreatedAt:           now,
		SignupCreditApplied: false,
	}
	if err := s.CreateOperatorAccount(context.Background(), want); err != nil {
		t.Fatalf("CreateOperatorAccount: %v", err)
	}
	got, err := s.GetOperatorAccount(context.Background(), "aabbcc")
	if err != nil {
		t.Fatalf("GetOperatorAccount: %v", err)
	}
	if got == nil {
		t.Fatal("expected account, got nil")
	}
	if got.PubkeyHex != want.PubkeyHex {
		t.Errorf("PubkeyHex: got %q, want %q", got.PubkeyHex, want.PubkeyHex)
	}
	if got.ForgeAccountID != want.ForgeAccountID {
		t.Errorf("ForgeAccountID: got %q, want %q", got.ForgeAccountID, want.ForgeAccountID)
	}
	if got.SignupCreditApplied {
		t.Errorf("SignupCreditApplied: expected false, got true")
	}
}

func TestMemoryOperatorAccountStore_CreateDuplicateIdempotent(t *testing.T) {
	s := NewMemoryOperatorAccountStore()
	acc := &OperatorAccount{
		PubkeyHex:      "ddeeff",
		ForgeAccountID: "forge-002",
		CreatedAt:      time.Now(),
	}
	// First create
	if err := s.CreateOperatorAccount(context.Background(), acc); err != nil {
		t.Fatalf("first CreateOperatorAccount: %v", err)
	}
	// Second create with different ForgeAccountID — should be ignored
	dup := &OperatorAccount{
		PubkeyHex:      "ddeeff",
		ForgeAccountID: "forge-DIFFERENT",
		CreatedAt:      time.Now(),
	}
	if err := s.CreateOperatorAccount(context.Background(), dup); err != nil {
		t.Fatalf("duplicate CreateOperatorAccount: %v", err)
	}
	// Verify original is preserved
	got, err := s.GetOperatorAccount(context.Background(), "ddeeff")
	if err != nil {
		t.Fatalf("GetOperatorAccount: %v", err)
	}
	if got.ForgeAccountID != "forge-002" {
		t.Errorf("ForgeAccountID after dup create: got %q, want %q", got.ForgeAccountID, "forge-002")
	}
}

func TestMemoryOperatorAccountStore_MarkSignupCreditApplied(t *testing.T) {
	s := NewMemoryOperatorAccountStore()
	acc := &OperatorAccount{
		PubkeyHex:           "112233",
		ForgeAccountID:      "forge-003",
		CreatedAt:           time.Now(),
		SignupCreditApplied: false,
	}
	if err := s.CreateOperatorAccount(context.Background(), acc); err != nil {
		t.Fatalf("CreateOperatorAccount: %v", err)
	}

	// Mark applied
	if err := s.MarkSignupCreditApplied(context.Background(), "112233"); err != nil {
		t.Fatalf("MarkSignupCreditApplied: %v", err)
	}

	// Verify flag is set
	got, err := s.GetOperatorAccount(context.Background(), "112233")
	if err != nil {
		t.Fatalf("GetOperatorAccount: %v", err)
	}
	if !got.SignupCreditApplied {
		t.Error("SignupCreditApplied: expected true after Mark, got false")
	}

	// Idempotent: calling again should not error
	if err := s.MarkSignupCreditApplied(context.Background(), "112233"); err != nil {
		t.Fatalf("idempotent MarkSignupCreditApplied: %v", err)
	}
}

func TestMemoryOperatorAccountStore_MarkSignupCreditApplied_NoAccount(t *testing.T) {
	s := NewMemoryOperatorAccountStore()
	err := s.MarkSignupCreditApplied(context.Background(), "doesnotexist")
	if err == nil {
		t.Fatal("expected error when marking credit on non-existent account, got nil")
	}
}

func TestMemoryOperatorAccountStore_ReturnsCopy(t *testing.T) {
	s := NewMemoryOperatorAccountStore()
	acc := &OperatorAccount{
		PubkeyHex:      "aaaaaa",
		ForgeAccountID: "forge-004",
		CreatedAt:      time.Now(),
	}
	if err := s.CreateOperatorAccount(context.Background(), acc); err != nil {
		t.Fatalf("CreateOperatorAccount: %v", err)
	}
	got, _ := s.GetOperatorAccount(context.Background(), "aaaaaa")
	// Mutate the returned struct — should not affect stored value.
	got.ForgeAccountID = "mutated"
	got2, _ := s.GetOperatorAccount(context.Background(), "aaaaaa")
	if got2.ForgeAccountID == "mutated" {
		t.Error("GetOperatorAccount returned a reference to internal state, not a copy")
	}
}
