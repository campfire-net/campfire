package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// createKeyRequest is the body for POST /v1/keys.
type createKeyRequest struct {
	Role            string `json:"role,omitempty"`
	TargetAccountID string `json:"target_account_id,omitempty"`
}

// CreateKey creates a new API key under the given account with the specified role.
// The caller must have a role higher than the requested role (privilege ceiling).
// Returns the Key including the one-time plaintext key value.
func (c *Client) CreateKey(ctx context.Context, accountID, role string) (Key, error) {
	reqBody := createKeyRequest{
		Role:            role,
		TargetAccountID: accountID,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return Key{}, fmt.Errorf("forge: marshal create key request: %w", err)
	}

	url := c.BaseURL + "/v1/keys"

	delays := c.retryDelays()
	maxAttempts := len(delays) + 1

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Key{}, fmt.Errorf("forge: %w", ctx.Err())
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
			return Key{}, fmt.Errorf("forge: server returned %d", resp.StatusCode)
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("forge: server returned %d", resp.StatusCode)
			continue
		}

		var k Key
		if decErr := decodeJSON(resp.Body, &k); decErr != nil {
			return Key{}, fmt.Errorf("forge: decode create key response: %w", decErr)
		}
		return k, nil
	}
	return Key{}, lastErr
}

// keyListResponse is the shape of GET /v1/keys response.
type keyListResponse struct {
	Object string      `json:"object"`
	Data   []KeyRecord `json:"data"`
}

// ResolveKey validates an API key by calling GET /v1/keys with that key as the
// bearer token. If the key is valid, returns the first matching KeyRecord.
// Returns an error on 401 (invalid key), 403 (insufficient permissions), or
// any 5xx response after retries.
//
// This is used by campfire-hosting to validate operator keys before accepting
// requests. The apiKey must have at least RoleAgent to call GET /v1/keys.
func (c *Client) ResolveKey(ctx context.Context, apiKey string) (KeyRecord, error) {
	url := c.BaseURL + "/v1/keys"

	// Temporarily override the service key with the operator key being resolved.
	probe := &Client{
		BaseURL:     c.BaseURL,
		ServiceKey:  apiKey,
		HTTPClient:  c.HTTPClient,
		RetryDelays: c.RetryDelays,
	}

	delays := probe.retryDelays()
	maxAttempts := len(delays) + 1

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return KeyRecord{}, fmt.Errorf("forge: %w", ctx.Err())
			case <-waitCh(delays[attempt-1]):
			}
		}

		resp, err := probe.doRequest(ctx, http.MethodGet, url, nil)
		if err != nil {
			lastErr = fmt.Errorf("forge: request: %w", err)
			continue
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			resp.Body.Close()
			return KeyRecord{}, fmt.Errorf("forge: server returned %d", resp.StatusCode)
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("forge: server returned %d", resp.StatusCode)
			continue
		}

		var list keyListResponse
		if decErr := decodeJSON(resp.Body, &list); decErr != nil {
			return KeyRecord{}, fmt.Errorf("forge: decode key list response: %w", decErr)
		}
		if len(list.Data) == 0 {
			return KeyRecord{}, fmt.Errorf("forge: no keys found for account")
		}
		return list.Data[0], nil
	}
	return KeyRecord{}, lastErr
}
