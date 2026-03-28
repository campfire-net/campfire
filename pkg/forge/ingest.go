package forge

import (
	"context"
	"encoding/json"
	"fmt"
)

// Ingest posts a single UsageEvent to POST /v1/usage/ingest.
// Retries up to 3 times with exponential backoff (1s, 2s, 4s) on 5xx responses
// or transport errors. Returns immediately on 4xx errors.
//
// Required fields: AccountID, ServiceID, IdempotencyKey. Timestamp is stamped
// by the server if zero.
func (c *Client) Ingest(ctx context.Context, event UsageEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("forge: marshal usage event: %w", err)
	}

	url := c.BaseURL + "/v1/usage/ingest"
	if err := c.doWithRetry(ctx, "POST", url, body); err != nil {
		return fmt.Errorf("forge: ingest: %w", err)
	}
	return nil
}
