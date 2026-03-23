package botframework

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// tokenRefreshMargin is how early (before expiry) we proactively refresh the token.
	tokenRefreshMargin = 5 * time.Minute

	// aadTokenURLTemplate is the AAD OAuth2 token endpoint.
	aadTokenURLTemplate = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"

	// botFrameworkScope is the scope required to call Bot Framework APIs.
	botFrameworkScope = "https://api.botframework.com/.default"
)

// tokenResponse is the AAD client_credentials token response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// cachedToken holds a token and its expiry time.
type cachedToken struct {
	accessToken string
	expiresAt   time.Time
}

// TokenClient acquires and caches Bot Framework access tokens.
type TokenClient struct {
	appID       string
	appPassword string
	tenantID    string
	tokenURL    string
	httpClient  *http.Client

	mu    sync.Mutex
	cache *cachedToken
}

// NewTokenClient creates a TokenClient for the given Azure Bot Service credentials.
// tenantID may be "botframework.com" for MSA-registered bots or an AAD tenant ID.
func NewTokenClient(appID, appPassword, tenantID string) *TokenClient {
	return &TokenClient{
		appID:       appID,
		appPassword: appPassword,
		tenantID:    tenantID,
		tokenURL:    fmt.Sprintf(aadTokenURLTemplate, tenantID),
		httpClient:  &http.Client{Timeout: 15 * time.Second},
	}
}

// newTokenClientWithClient creates a TokenClient with a custom HTTP client (for testing).
func newTokenClientWithClient(appID, appPassword, tenantID string, client *http.Client) *TokenClient {
	return &TokenClient{
		appID:       appID,
		appPassword: appPassword,
		tenantID:    tenantID,
		tokenURL:    fmt.Sprintf(aadTokenURLTemplate, tenantID),
		httpClient:  client,
	}
}

// GetToken returns a valid access token, refreshing if necessary.
// It is safe for concurrent use.
func (c *TokenClient) GetToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cache != nil && time.Now().Before(c.cache.expiresAt) {
		return c.cache.accessToken, nil
	}

	tok, err := c.fetchToken(ctx)
	if err != nil {
		return "", err
	}

	c.cache = tok
	return tok.accessToken, nil
}

// fetchToken performs the client_credentials OAuth2 flow and returns a cachedToken.
func (c *TokenClient) fetchToken(ctx context.Context) (*cachedToken, error) {
	body := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.appID},
		"client_secret": {c.appPassword},
		"scope":         {botFrameworkScope},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL,
		strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Do not include the raw body in the error — it may contain credential
		// hints or detailed AAD error descriptions that should not be logged.
		return nil, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}

	// Cache until expires_in - tokenRefreshMargin.
	expiry := time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second - tokenRefreshMargin)
	return &cachedToken{
		accessToken: tr.AccessToken,
		expiresAt:   expiry,
	}, nil
}

// Client sends activities to the Bot Framework REST API.
// It holds a TokenClient for bearer-token acquisition and an optional
// custom HTTP client (for testing).
type Client struct {
	tokens     *TokenClient
	httpClient *http.Client
}

// NewClient creates a Client backed by the given TokenClient.
func NewClient(tokens *TokenClient) *Client {
	return &Client{
		tokens:     tokens,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewClientWithHTTP creates a Client with a custom HTTP client.
// Intended for testing and local integration scenarios.
func NewClientWithHTTP(tokens *TokenClient, httpClient *http.Client) *Client {
	return &Client{tokens: tokens, httpClient: httpClient}
}

// newClientWithHTTP is an alias for internal test use within this package.
func newClientWithHTTP(tokens *TokenClient, httpClient *http.Client) *Client {
	return NewClientWithHTTP(tokens, httpClient)
}

// NewTokenClientForTest creates a TokenClient pointed at a custom token URL
// with a custom HTTP client. Intended for integration tests.
func NewTokenClientForTest(tokenURL string, httpClient *http.Client) *TokenClient {
	tc := &TokenClient{
		appID:       "test-app-id",
		appPassword: "test-app-secret",
		tenantID:    "test-tenant",
		tokenURL:    tokenURL,
		httpClient:  httpClient,
	}
	return tc
}

// SendActivity posts a new activity to an existing conversation.
// serviceURL is the Bot Framework service URL from the inbound activity.
// Returns the activity ID assigned by the service.
func (c *Client) SendActivity(ctx context.Context, serviceURL, conversationID string, activity *Activity) (*ResourceResponse, error) {
	endpoint := fmt.Sprintf("%sv3/conversations/%s/activities",
		ensureTrailingSlash(serviceURL), conversationID)
	return c.doActivity(ctx, http.MethodPost, endpoint, activity)
}

// ReplyToActivity posts a reply into a specific thread within a conversation.
// replyToID is the activity ID of the parent message.
func (c *Client) ReplyToActivity(ctx context.Context, serviceURL, conversationID, replyToID string, activity *Activity) (*ResourceResponse, error) {
	endpoint := fmt.Sprintf("%sv3/conversations/%s/activities/%s",
		ensureTrailingSlash(serviceURL), conversationID, replyToID)
	return c.doActivity(ctx, http.MethodPost, endpoint, activity)
}

// UpdateActivity replaces an existing activity (e.g., to update an Adaptive Card state).
func (c *Client) UpdateActivity(ctx context.Context, serviceURL, conversationID, activityID string, activity *Activity) (*ResourceResponse, error) {
	endpoint := fmt.Sprintf("%sv3/conversations/%s/activities/%s",
		ensureTrailingSlash(serviceURL), conversationID, activityID)
	return c.doActivity(ctx, http.MethodPut, endpoint, activity)
}

// CreateConversation starts a new proactive conversation (required before the first
// outbound message when no inbound activity has occurred).
func (c *Client) CreateConversation(ctx context.Context, serviceURL string, params *ConversationParameters) (*ConversationResourceResponse, error) {
	endpoint := fmt.Sprintf("%sv3/conversations", ensureTrailingSlash(serviceURL))

	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshalling conversation params: %w", err)
	}

	var result ConversationResourceResponse
	if err := c.doRequestWithRetry(ctx, http.MethodPost, endpoint, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// doActivity serialises activity, calls doRequestWithRetry, and returns a ResourceResponse.
func (c *Client) doActivity(ctx context.Context, method, endpoint string, activity *Activity) (*ResourceResponse, error) {
	body, err := json.Marshal(activity)
	if err != nil {
		return nil, fmt.Errorf("marshalling activity: %w", err)
	}

	var result ResourceResponse
	if err := c.doRequestWithRetry(ctx, method, endpoint, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// doRequestWithRetry executes an HTTP request with bearer auth, handling:
//   - 401: force-refresh the token and retry once
//   - 429: wait Retry-After seconds and retry once
func (c *Client) doRequestWithRetry(ctx context.Context, method, endpoint string, body []byte, out any) error {
	tok, err := c.tokens.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("acquiring token: %w", err)
	}

	statusCode, respBody, err := c.doRequest(ctx, method, endpoint, body, tok)
	if err != nil {
		return err
	}

	switch statusCode {
	case http.StatusUnauthorized:
		// Force a token refresh by clearing the cache and retrying once.
		c.tokens.mu.Lock()
		c.tokens.cache = nil
		c.tokens.mu.Unlock()

		tok, err = c.tokens.GetToken(ctx)
		if err != nil {
			return fmt.Errorf("re-acquiring token after 401: %w", err)
		}
		statusCode, respBody, err = c.doRequest(ctx, method, endpoint, body, tok)
		if err != nil {
			return err
		}

	case http.StatusTooManyRequests:
		retryAfter := parseRetryAfter(respBody)
		if retryAfter > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryAfter):
			}
		}
		statusCode, respBody, err = c.doRequest(ctx, method, endpoint, body, tok)
		if err != nil {
			return err
		}
	}

	if statusCode < 200 || statusCode >= 300 {
		return fmt.Errorf("bot framework returned %d: %s", statusCode, respBody)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	}
	return nil
}

// doRequest performs a single authenticated HTTP request and returns the status code
// and raw response body.  It does NOT interpret the status code.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body []byte, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("reading response body: %w", err)
	}
	return resp.StatusCode, raw, nil
}

// ensureTrailingSlash normalises a service URL so path concatenation is safe.
func ensureTrailingSlash(u string) string {
	if !strings.HasSuffix(u, "/") {
		return u + "/"
	}
	return u
}

// parseRetryAfter extracts the Retry-After duration from a 429 response body.
// The Bot Framework typically encodes it as {"retryAfter": N} (seconds).
// Falls back to 1 second if the field is absent or unparseable.
func parseRetryAfter(body []byte) time.Duration {
	var v struct {
		RetryAfter any `json:"retryAfter"`
	}
	if err := json.Unmarshal(body, &v); err == nil {
		switch n := v.RetryAfter.(type) {
		case float64:
			return time.Duration(n) * time.Second
		case string:
			if secs, err := strconv.ParseFloat(n, 64); err == nil {
				return time.Duration(secs) * time.Second
			}
		}
	}
	return time.Second
}
