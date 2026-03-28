package hosting

import (
	"context"
	"errors"
	"testing"
)

func TestIdentityGate_ValidIdentityPasses(t *testing.T) {
	gate := &IdentityGate{}
	identity := &OperatorIdentity{
		AccountID: "acct-123",
		Name:      "Test Operator",
		Role:      "operator",
	}
	if err := gate.RequireIdentity(context.Background(), identity); err != nil {
		t.Fatalf("expected nil error for valid identity, got: %v", err)
	}
}

func TestIdentityGate_NilIdentityReturnsError(t *testing.T) {
	gate := &IdentityGate{}
	err := gate.RequireIdentity(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil identity, got nil")
	}
	if !errors.Is(err, ErrAnonymousDurableStorage) {
		t.Fatalf("expected ErrAnonymousDurableStorage, got: %v", err)
	}
}

func TestIdentityGate_EmptyAccountIDReturnsError(t *testing.T) {
	gate := &IdentityGate{}
	identity := &OperatorIdentity{
		AccountID: "",
		Name:      "No Account",
		Role:      "operator",
	}
	err := gate.RequireIdentity(context.Background(), identity)
	if err == nil {
		t.Fatal("expected error for empty AccountID, got nil")
	}
	if !errors.Is(err, ErrAnonymousDurableStorage) {
		t.Fatalf("expected ErrAnonymousDurableStorage, got: %v", err)
	}
}

func TestIdentityGate_ErrorsIsWorksForSentinel(t *testing.T) {
	gate := &IdentityGate{}
	err := gate.RequireIdentity(context.Background(), nil)
	if !errors.Is(err, ErrAnonymousDurableStorage) {
		t.Fatalf("errors.Is must detect ErrAnonymousDurableStorage, got: %v", err)
	}
}
