package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// createAccountRequest is the body for POST /v1/accounts.
// Note: this creates an account + admin key in Forge. The name field is stored
// on the response but Forge uses it for display only. Email is not a Forge field.
type createAccountRequest struct {
	Name             string `json:"name"`
	SovereigntyFloor string `json:"sovereignty_floor,omitempty"`
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

	url := c.BaseURL + "/v1/accounts"

	delays := c.retryDelays()
	maxAttempts := len(delays) + 1

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Account{}, fmt.Errorf("forge: %w", ctx.Err())
			case <-waitCh(delays[attempt-1]):
			}
		}

		resp, err := c.doRequest(ctx, http.MethodPost, url, body)
		if err != nil {
			lastErr = fmt.Errorf("forge: request: %w", err)
			continue
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			resp.Body.Close()
			return Account{}, fmt.Errorf("forge: server returned %d", resp.StatusCode)
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("forge: server returned %d", resp.StatusCode)
			continue
		}

		var cr createAccountResponse
		if decErr := decodeJSON(resp.Body, &cr); decErr != nil {
			return Account{}, fmt.Errorf("forge: decode create account response: %w", decErr)
		}
		return Account{
			AccountID:        cr.AccountID,
			Name:             cr.Name,
			SovereigntyFloor: cr.SovereigntyFloor,
			CreatedAt:        cr.CreatedAt,
		}, nil
	}
	return Account{}, lastErr
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
