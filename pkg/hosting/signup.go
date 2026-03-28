package hosting

import (
	"context"
	"fmt"

	"github.com/campfire-net/campfire/pkg/forge"
)

const (
	// RoleTenant is the Forge role assigned to operator keys created during sign-up.
	RoleTenant = "tenant"
)

// ForgeAccountCreator is the subset of forge.Client methods needed for sign-up.
// Defining an interface here lets tests inject a mock without a real Forge server.
type ForgeAccountCreator interface {
	CreateAccount(ctx context.Context, name, email string) (forge.Account, error)
	CreateKey(ctx context.Context, accountID, role string) (forge.Key, error)
}

// SignupService creates new operator accounts in Forge.
type SignupService struct {
	client ForgeAccountCreator
}

// NewSignupService returns a SignupService backed by client.
func NewSignupService(client ForgeAccountCreator) *SignupService {
	return &SignupService{client: client}
}

// CreateOperator provisions a new operator in Forge. It creates an account with
// the given name, then creates a tenant-role API key for that account. The
// returned apiKey is the one-time plaintext value — the caller is responsible
// for delivering it to the operator; it cannot be retrieved again.
func (s *SignupService) CreateOperator(ctx context.Context, name string) (OperatorIdentity, string, error) {
	// Create the Forge account. Email is not stored by Forge; pass empty string.
	account, err := s.client.CreateAccount(ctx, name, "")
	if err != nil {
		return OperatorIdentity{}, "", fmt.Errorf("hosting: create account: %w", err)
	}

	// Create a tenant-role API key for the new account.
	key, err := s.client.CreateKey(ctx, account.AccountID, RoleTenant)
	if err != nil {
		return OperatorIdentity{}, "", fmt.Errorf("hosting: create key for account %s: %w", account.AccountID, err)
	}

	identity := OperatorIdentity{
		AccountID: account.AccountID,
		Name:      account.Name,
		Role:      key.Role,
	}
	return identity, key.KeyPlaintext, nil
}
