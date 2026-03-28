package forge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a service-to-service HTTP client for the Forge API.
// All requests are authenticated with a forge-sk-* service key.
type Client struct {
	// BaseURL is the root of the Forge API, e.g. "https://forge.example.com".
	// No trailing slash.
	BaseURL string

	// ServiceKey is a forge-sk-* key with RoleService or higher. Required.
	ServiceKey string

	// HTTPClient is used for all requests. If nil, a default client with a
	// 30-second timeout is used.
	HTTPClient *http.Client

	// RetryDelays overrides the default exponential backoff delays (1s, 2s, 4s)
	// used by methods that retry on 5xx. Primarily useful in tests.
	// len(RetryDelays) determines the maximum number of retry pauses;
	// total attempts = len(RetryDelays) + 1.
	RetryDelays []time.Duration
}

var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return defaultHTTPClient
}

func (c *Client) retryDelays() []time.Duration {
	if len(c.RetryDelays) > 0 {
		return c.RetryDelays
	}
	return []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
}

// doRequest performs a single HTTP request. The caller is responsible for
// closing the response body.
func (c *Client) doRequest(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient().Do(req)
}

// doWithRetry executes a request with exponential backoff on 5xx / transport errors.
// Returns immediately on 4xx.
func (c *Client) doWithRetry(ctx context.Context, method, url string, body []byte) error {
	delays := c.retryDelays()
	maxAttempts := len(delays) + 1

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("forge: %w", ctx.Err())
			case <-time.After(delays[attempt-1]):
			}
		}

		resp, err := c.doRequest(ctx, method, url, body)
		if err != nil {
			lastErr = fmt.Errorf("forge: request: %w", err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return fmt.Errorf("forge: server returned %d", resp.StatusCode)
		}
		lastErr = fmt.Errorf("forge: server returned %d", resp.StatusCode)
	}
	return lastErr
}

// getWithRetry executes a GET request with retry, decoding the JSON response into dst.
func (c *Client) getWithRetry(ctx context.Context, url string, dst any) error {
	delays := c.retryDelays()
	maxAttempts := len(delays) + 1

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("forge: %w", ctx.Err())
			case <-time.After(delays[attempt-1]):
			}
		}

		resp, err := c.doRequest(ctx, http.MethodGet, url, nil)
		if err != nil {
			lastErr = fmt.Errorf("forge: request: %w", err)
			continue
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			resp.Body.Close()
			return fmt.Errorf("forge: server returned %d", resp.StatusCode)
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("forge: server returned %d", resp.StatusCode)
			continue
		}

		if err := decodeJSON(resp.Body, dst); err != nil {
			return fmt.Errorf("forge: decode response: %w", err)
		}
		return nil
	}
	return lastErr
}

// postWithRetry executes a POST request with retry, decoding the JSON response into dst.
func (c *Client) postWithRetry(ctx context.Context, url string, body []byte, dst any) error {
	delays := c.retryDelays()
	maxAttempts := len(delays) + 1

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("forge: %w", ctx.Err())
			case <-time.After(delays[attempt-1]):
			}
		}

		resp, err := c.doRequest(ctx, http.MethodPost, url, body)
		if err != nil {
			lastErr = fmt.Errorf("forge: request: %w", err)
			continue
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			resp.Body.Close()
			return fmt.Errorf("forge: server returned %d", resp.StatusCode)
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("forge: server returned %d", resp.StatusCode)
			continue
		}

		if err := decodeJSON(resp.Body, dst); err != nil {
			return fmt.Errorf("forge: decode response: %w", err)
		}
		return nil
	}
	return lastErr
}
