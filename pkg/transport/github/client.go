// Package github implements the GitHub Issues transport for the Campfire protocol.
// Each campfire maps to one GitHub Issue; messages are Issue comments.
package github

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	defaultBaseURL   = "https://api.github.com"
	writeThrottleMin = 750 * time.Millisecond
)

// githubComment is a GitHub issue comment as returned by the API.
type githubComment struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// githubIssue is a GitHub issue as returned by the API.
type githubIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// githubFileContent is the GitHub contents API response for a file.
type githubFileContent struct {
	Content string `json:"content"` // base64 encoded, may have newlines
	SHA     string `json:"sha"`
}

// githubClient is a thin wrapper over the GitHub REST API.
// It handles auth headers, ETag-based conditional GETs, and per-issue write throttling.
type githubClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	writeMu    sync.Mutex
	lastWrite  map[string]time.Time // issue key -> last write time
}

// Client is the exported alias for githubClient so callers outside the package
// (e.g. cmd/cf) can create a client for direct API operations (beacon publishing,
// discovery) without going through the Transport struct.
type Client = githubClient

// NewClient creates a new Client. baseURL defaults to "https://api.github.com".
// Use this when you need to call PublishBeacon or DiscoverBeacons directly.
func NewClient(baseURL, token string) *Client {
	return newGithubClient(baseURL, token)
}

// newGithubClient creates a new githubClient. baseURL defaults to "https://api.github.com".
func newGithubClient(baseURL, token string) *githubClient {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &githubClient{
		baseURL:    baseURL,
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		lastWrite:  make(map[string]time.Time),
	}
}

// issueKey returns a per-issue throttle key.
func issueKey(repo string, issueNumber int) string {
	return fmt.Sprintf("%s#%d", repo, issueNumber)
}

// applyWriteThrottle waits if necessary to ensure >= 750ms between writes to the same issue.
// The writeMu must NOT be held by the caller.
func (c *githubClient) applyWriteThrottle(key string) {
	c.writeMu.Lock()
	last, ok := c.lastWrite[key]
	if ok {
		wait := writeThrottleMin - time.Since(last)
		if wait > 0 {
			c.writeMu.Unlock()
			time.Sleep(wait)
			c.writeMu.Lock()
		}
	}
	c.lastWrite[key] = time.Now()
	c.writeMu.Unlock()
}

// do executes an HTTP request with the Bearer token and returns the response.
func (c *githubClient) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return c.httpClient.Do(req)
}

// CreateComment posts a comment to a GitHub issue.
// It enforces the per-issue write throttle (minimum 750ms between calls to the same issue).
func (c *githubClient) CreateComment(repo string, issueNumber int, body string) error {
	key := issueKey(repo, issueNumber)
	c.applyWriteThrottle(key)

	endpoint := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.baseURL, repo, issueNumber)
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("marshal comment body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create comment request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("create comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create comment: HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

// ListComments fetches comments on a GitHub issue since the given time.
// If etag is non-empty, it sends an If-None-Match conditional request.
// A 304 Not Modified response returns an empty slice and the original etag.
// Returns (comments, newEtag, error).
func (c *githubClient) ListComments(repo string, issueNumber int, since time.Time, etag string) ([]githubComment, string, error) {
	u, err := url.Parse(fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.baseURL, repo, issueNumber))
	if err != nil {
		return nil, "", fmt.Errorf("parse URL: %w", err)
	}

	q := u.Query()
	q.Set("per_page", "100")
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("list comments request: %w", err)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.do(req)
	if err != nil {
		return nil, "", fmt.Errorf("list comments: %w", err)
	}
	defer resp.Body.Close()

	returnedEtag := resp.Header.Get("ETag")
	if returnedEtag == "" {
		returnedEtag = etag // preserve caller's etag on 304
	}

	if resp.StatusCode == http.StatusNotModified {
		return []githubComment{}, returnedEtag, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("list comments: HTTP %d: %s", resp.StatusCode, body)
	}

	var comments []githubComment
	if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
		return nil, "", fmt.Errorf("decode comments: %w", err)
	}
	return comments, returnedEtag, nil
}

// CreateIssue creates a new GitHub issue and returns its number.
func (c *githubClient) CreateIssue(repo, title, body string) (int, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/issues", c.baseURL, repo)
	payload, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		return 0, fmt.Errorf("marshal issue: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("create issue request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.do(req)
	if err != nil {
		return 0, fmt.Errorf("create issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("create issue: HTTP %d: %s", resp.StatusCode, b)
	}

	var issue githubIssue
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return 0, fmt.Errorf("decode issue: %w", err)
	}
	return issue.Number, nil
}

// GetFile fetches the content of a file or directory listing from the repository
// via the Contents API.
//
// For a file path, GitHub returns a JSON object with a base64-encoded "content"
// field; GetFile decodes it and returns the raw bytes.
//
// For a directory path, GitHub returns a JSON array of directory entries; GetFile
// returns the raw JSON array bytes so callers can unmarshal them as needed (e.g.
// for beacon discovery in .campfire/beacons/).
func (c *githubClient) GetFile(repo, path string) ([]byte, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/contents/%s", c.baseURL, repo, path)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("get file request: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("get file %q: not found", path)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get file: HTTP %d: %s", resp.StatusCode, b)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read file response: %w", err)
	}

	// GitHub returns a JSON array for directory listings and a JSON object for files.
	// Return array responses as-is so callers can unmarshal directory entries.
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return body, nil
	}

	var fc githubFileContent
	if err := json.Unmarshal(body, &fc); err != nil {
		return nil, fmt.Errorf("decode file content: %w", err)
	}

	// GitHub returns base64 with newlines — strip them before decoding
	raw := bytes.ReplaceAll([]byte(fc.Content), []byte("\n"), nil)
	content, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return nil, fmt.Errorf("decode base64 file content: %w", err)
	}
	return content, nil
}

// PutFile creates or updates a file in the repository via the Contents API.
// content is the raw bytes to store (will be base64-encoded before sending).
func (c *githubClient) PutFile(repo, path, message string, content []byte) error {
	endpoint := fmt.Sprintf("%s/repos/%s/contents/%s", c.baseURL, repo, path)

	// Check if file exists to get its SHA (required for updates)
	var existingSHA string
	getReq, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("put file get-sha request: %w", err)
	}
	getResp, err := c.do(getReq)
	if err != nil {
		return fmt.Errorf("put file get-sha: %w", err)
	}
	if getResp.StatusCode == http.StatusOK {
		var fc githubFileContent
		json.NewDecoder(getResp.Body).Decode(&fc)
		existingSHA = fc.SHA
	}
	getResp.Body.Close()

	payload := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
	}
	if existingSHA != "" {
		payload["sha"] = existingSHA
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal put file: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("put file request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("put file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put file: HTTP %d: %s", resp.StatusCode, b)
	}
	return nil
}
