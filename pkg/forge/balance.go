package forge

import (
	"context"
	"fmt"
)

// Balance queries GET /v1/accounts/{id}/balance and returns the balance in
// micro-USD. Requires RoleService or higher.
// Retries on 5xx / transport errors; returns immediately on 4xx.
func (c *Client) Balance(ctx context.Context, accountID string) (int64, error) {
	url := c.BaseURL + "/v1/accounts/" + accountID + "/balance"

	var payload struct {
		BalanceMicro int64 `json:"balance_micro"`
	}
	if err := c.getWithRetry(ctx, url, &payload); err != nil {
		return 0, fmt.Errorf("forge: balance for account %s: %w", accountID, err)
	}
	return payload.BalanceMicro, nil
}
