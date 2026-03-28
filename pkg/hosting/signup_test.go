package hosting

import (
	"context"
	"errors"
	"testing"

	"github.com/campfire-net/campfire/pkg/forge"
)

// mockAccountCreator implements ForgeAccountCreator for tests.
type mockAccountCreator struct {
	account    forge.Account
	accountErr error
	key        forge.Key
	keyErr     error
}

func (m *mockAccountCreator) CreateAccount(_ context.Context, name, _ string) (forge.Account, error) {
	if m.accountErr != nil {
		return forge.Account{}, m.accountErr
	}
	// Echo the name back so tests can verify it was passed.
	a := m.account
	if a.Name == "" {
		a.Name = name
	}
	return a, nil
}

func (m *mockAccountCreator) CreateKey(_ context.Context, _, _ string) (forge.Key, error) {
	return m.key, m.keyErr
}

func TestCreateOperator_HappyPath(t *testing.T) {
	mock := &mockAccountCreator{
		account: forge.Account{AccountID: "acc-new", Name: "Acme Corp"},
		key:     forge.Key{KeyPlaintext: "forge-tk-plaintext", AccountID: "acc-new", Role: RoleTenant},
	}
	svc := NewSignupService(mock)

	identity, apiKey, err := svc.CreateOperator(context.Background(), "Acme Corp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identity.AccountID != "acc-new" {
		t.Errorf("AccountID = %q, want %q", identity.AccountID, "acc-new")
	}
	if identity.Name != "Acme Corp" {
		t.Errorf("Name = %q, want %q", identity.Name, "Acme Corp")
	}
	if identity.Role != RoleTenant {
		t.Errorf("Role = %q, want %q", identity.Role, RoleTenant)
	}
	if apiKey != "forge-tk-plaintext" {
		t.Errorf("apiKey = %q, want %q", apiKey, "forge-tk-plaintext")
	}
}

func TestCreateOperator_CreateAccountError(t *testing.T) {
	mock := &mockAccountCreator{
		accountErr: errors.New("forge: server returned 500"),
	}
	svc := NewSignupService(mock)

	_, _, err := svc.CreateOperator(context.Background(), "Broken Corp")
	if err == nil {
		t.Fatal("expected error when CreateAccount fails, got nil")
	}
}

func TestCreateOperator_CreateKeyError(t *testing.T) {
	mock := &mockAccountCreator{
		account: forge.Account{AccountID: "acc-keyless", Name: "KeyLess"},
		keyErr:  errors.New("forge: server returned 403"),
	}
	svc := NewSignupService(mock)

	_, _, err := svc.CreateOperator(context.Background(), "KeyLess")
	if err == nil {
		t.Fatal("expected error when CreateKey fails, got nil")
	}
}
