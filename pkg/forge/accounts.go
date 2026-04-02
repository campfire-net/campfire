package forge

import (
	"context"
	"encoding/json"
	"fmt"
)

// createAccountRequest is the body for POST /v1/accounts.
// Note: this creates an account + admin key in Forge. The name field is stored
// on the response but Forge uses it for display only. Email is not a Forge field.
type createAccountRequest struct {
	Name             string `json:"name"`
	SovereigntyFloor string `json:"sovereignty_floor,omitempty"`
	ParentAccountID  string `json:"parent_account_id,omitempty"`
}

// createAccountResponse is returned by POST /v1/accounts.
type createAccountResponse struct {
	AccountID         string `json:"account_id"`
	Name              string `json:"name"`
	SovereigntyFloor  string `json:"sovereignty_floor"`
	AdminKeyPlaintext string `json:"admin_key"` // shown once at creation
	CreatedAt         string `json:"created_at"`
}

// CreateAccount creates a new Forge account with the given name and returns it.
// The caller must have RoleTenant or higher.
//
// Note: Forge does not store email addresses. The email parameter is accepted
// for interface compatibility but is not sent to Forge. Use GetAccount if you
// need to retrieve an existing account.
func (c *Client) CreateAccount(ctx context.Context, name, _ string) (Account, error) {
	reqBody := createAccountRequest{Name: name}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return Account{}, fmt.Errorf("forge: marshal create account request: %w", err)
	}

	var cr createAccountResponse
	if err := c.postWithRetry(ctx, c.BaseURL+"/v1/accounts", body, &cr); err != nil {
		return Account{}, err
	}
	return Account{
		AccountID:        cr.AccountID,
		Name:             cr.Name,
		SovereigntyFloor: cr.SovereigntyFloor,
		CreatedAt:        cr.CreatedAt,
	}, nil
}

// CreateSubAccount creates a new Forge account as a child of parentAccountID.
// This is used to create per-tenant accounts nested under the operator's root account.
// The caller must have RoleAdmin or higher on the parent account.
func (c *Client) CreateSubAccount(ctx context.Context, name, parentAccountID string) (Account, error) {
	reqBody := createAccountRequest{Name: name, ParentAccountID: parentAccountID}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return Account{}, fmt.Errorf("forge: marshal create sub-account request: %w", err)
	}

	var cr createAccountResponse
	if err := c.postWithRetry(ctx, c.BaseURL+"/v1/accounts", body, &cr); err != nil {
		return Account{}, err
	}
	return Account{
		AccountID:        cr.AccountID,
		Name:             cr.Name,
		SovereigntyFloor: cr.SovereigntyFloor,
		CreatedAt:        cr.CreatedAt,
	}, nil
}

// GetAccount retrieves an account by ID from GET /v1/billing/accounts/{id}.
// The caller must have RoleAdmin or higher (or RoleTenant for accounts in their subtree).
func (c *Client) GetAccount(ctx context.Context, id string) (Account, error) {
	url := c.BaseURL + "/v1/billing/accounts/" + id

	var raw struct {
		AccountID        string  `json:"account_id"`
		ParentAccountID  string  `json:"parent_account_id,omitempty"`
		SovereigntyFloor string  `json:"sovereignty_floor,omitempty"`
		BalanceMicro     *int64  `json:"balance_micro,omitempty"`
		CreatedAt        string  `json:"created_at,omitempty"`
		UpdatedAt        string  `json:"updated_at,omitempty"`
	}
	if err := c.getWithRetry(ctx, url, &raw); err != nil {
		return Account{}, fmt.Errorf("forge: get account %s: %w", id, err)
	}

	return Account{
		AccountID:        raw.AccountID,
		ParentAccountID:  raw.ParentAccountID,
		SovereigntyFloor: raw.SovereigntyFloor,
		BalanceMicro:     raw.BalanceMicro,
		CreatedAt:        raw.CreatedAt,
		UpdatedAt:        raw.UpdatedAt,
	}, nil
}
