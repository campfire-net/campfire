package forge

import (
	"context"
	"encoding/json"
	"fmt"
)

// creditAccountRequest is the body for POST /v1/accounts/{accountID}/credit.
type creditAccountRequest struct {
	AmountMicro int64  `json:"amount_micro"`
	Product     string `json:"product"`
}

// CreditAccount adds promotional credit to a Forge account.
// POST /v1/accounts/{accountID}/credit
// The amount is in micro-USD (e.g., 10_000_000 = $10.00).
// product identifies the source of the credit (e.g., "campfire-signup-credit").
func (c *Client) CreditAccount(ctx context.Context, accountID string, amountMicro int64, product string) error {
	reqBody := creditAccountRequest{AmountMicro: amountMicro, Product: product}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("forge: marshal credit account request: %w", err)
	}

	url := c.BaseURL + "/v1/accounts/" + accountID + "/credit"
	if err := c.doWithRetry(ctx, "POST", url, body); err != nil {
		return fmt.Errorf("forge: credit account %s: %w", accountID, err)
	}
	return nil
}
