// cmd/cf-ui/auth_test.go — tests for the authentication flow.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Test helpers ---

// fakeAuthProvider records CreateSession calls and returns a fixed token.
type fakeAuthProvider struct {
	token  string
	calls  []Identity
	failOn string // if non-empty, CreateSession returns an error for this provider
}

func (f *fakeAuthProvider) CreateSession(_ context.Context, id Identity) (string, error) {
	if f.failOn != "" && id.Provider == f.failOn {
		return "", fmt.Errorf("fake: CreateSession forced failure for provider %s", id.Provider)
	}
	f.calls = append(f.calls, id)
	return f.token, nil
}

func (f *fakeAuthProvider) InvalidateSession(_ context.Context, _ string) error { return nil }

// newTestAuthConfig returns an AuthConfig wired to a fake GitHub server and in-memory stores.
// The fakeGH server is used for both token exchange and user API calls.
func newTestAuthConfig(t *testing.T, ghServer *httptest.Server) (*AuthConfig, *fakeAuthProvider) {
	t.Helper()
	logger := newDiscardLogger()
	provider := &fakeAuthProvider{token: "test-session-token-" + t.Name()}
	sessions := NewMemSessionStore()
	cfg := newAuthConfig(logger, func(k string) string {
		switch k {
		case "GITHUB_CLIENT_ID":
			return "test-client-id"
		case "GITHUB_CLIENT_SECRET":
			return "test-client-secret"
		}
		return ""
	}, "http://localhost", sessions, provider)

	if ghServer != nil {
		// Override GitHub API endpoints by using a custom transport that rewrites
		// the host. We accomplish this with a simple round-tripper.
		cfg.httpClient = &http.Client{
			Transport: &rewriteTransport{target: ghServer.URL},
		}
	}
	return cfg, provider
}

// rewriteTransport rewrites all requests to point at a fixed target host.
type rewriteTransport struct {
	target string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(rt.target)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = u.Scheme
	clone.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopWriter{}, nil))
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// newTestServerWithAuth builds an httptest.Server using a custom AuthConfig.
func newTestServerWithAuth(t *testing.T, cfg *AuthConfig) *httptest.Server {
	t.Helper()
	mux := buildMuxWithAuth(nil, cfg)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// --- GitHub OAuth redirect tests ---

func TestGitHubOAuthRedirectURL(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/auth/github", nil)
	w := httptest.NewRecorder()

	cfg.handleGitHubLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	if parsed.Host != "github.com" {
		t.Errorf("expected github.com host, got %q", parsed.Host)
	}
	if parsed.Path != "/login/oauth/authorize" {
		t.Errorf("expected /login/oauth/authorize path, got %q", parsed.Path)
	}

	q := parsed.Query()
	if q.Get("client_id") != "test-client-id" {
		t.Errorf("missing or wrong client_id: %q", q.Get("client_id"))
	}
	scope := q.Get("scope")
	if !strings.Contains(scope, "read:user") || !strings.Contains(scope, "user:email") {
		t.Errorf("expected scope to contain read:user and user:email, got %q", scope)
	}
	if q.Get("state") == "" {
		t.Error("expected non-empty state parameter for CSRF protection")
	}
}

func TestGitHubOAuthStateCookie(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/auth/github", nil)
	w := httptest.NewRecorder()

	cfg.handleGitHubLogin(w, req)

	var stateCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == oauthStateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("expected oauth state cookie to be set")
	}
	if stateCookie.Value == "" {
		t.Error("state cookie value is empty")
	}
	if !stateCookie.HttpOnly {
		t.Error("state cookie must be HttpOnly")
	}
	if stateCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax, got %v", stateCookie.SameSite)
	}
}

// --- GitHub OAuth callback tests ---

// fakeGitHubServer creates an httptest.Server that mimics GitHub's OAuth + API endpoints.
func fakeGitHubServer(t *testing.T, user githubUser, emails []githubEmail) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "gh-fake-token"}) //nolint:errcheck
	})

	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(user) //nolint:errcheck
	})

	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(emails) //nolint:errcheck
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubCallbackSuccess(t *testing.T) {
	ghServer := fakeGitHubServer(t,
		githubUser{Login: "octocat", Name: "The Octocat", Email: "octocat@github.com", AvatarURL: "https://avatars.example.com/octocat"},
		nil,
	)
	cfg, provider := newTestAuthConfig(t, ghServer)
	srv := newTestServerWithAuth(t, cfg)

	// Step 1: GET /auth/github — do NOT follow the redirect to GitHub.
	// We need the state cookie value and the state query param.
	noRedirectClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Jar:           newCookieJar(),
	}
	resp, err := noRedirectClient.Get(srv.URL + "/auth/github")
	if err != nil {
		t.Fatalf("GET /auth/github: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 redirect to GitHub, got %d", resp.StatusCode)
	}

	// Extract state from the GitHub authorization redirect location.
	location := resp.Header.Get("Location")
	parsed, _ := url.Parse(location)
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("expected state parameter in GitHub redirect URL")
	}

	// The state cookie is now in the jar (set by /auth/github response).

	// Step 2: Simulate GitHub calling back with the same state.
	callbackURL := fmt.Sprintf("%s/auth/github/callback?code=fake-code&state=%s", srv.URL, url.QueryEscape(state))
	resp2, err := noRedirectClient.Get(callbackURL)
	if err != nil {
		t.Fatalf("GET /auth/github/callback: %v", err)
	}
	defer resp2.Body.Close()

	// Callback should redirect to / on success.
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect after successful OAuth, got %d", resp2.StatusCode)
	}

	// CreateSession should have been called once.
	if len(provider.calls) != 1 {
		t.Fatalf("expected 1 CreateSession call, got %d", len(provider.calls))
	}
	id := provider.calls[0]
	if id.Email != "octocat@github.com" {
		t.Errorf("expected email octocat@github.com, got %q", id.Email)
	}
	if id.Provider != "github" {
		t.Errorf("expected provider=github, got %q", id.Provider)
	}
}

func TestGitHubCallbackFetchEmailFallback(t *testing.T) {
	// User has no public email — should fall back to /user/emails.
	ghServer := fakeGitHubServer(t,
		githubUser{Login: "private-user", Name: "Private User", Email: ""},
		[]githubEmail{
			{Email: "private@example.com", Primary: true, Verified: true},
		},
	)
	cfg, provider := newTestAuthConfig(t, ghServer)

	// Directly call exchangeGitHubCode + fetchGitHubIdentity to unit-test the fallback.
	token, err := cfg.exchangeGitHubCode(context.Background(), "any-code")
	if err != nil {
		t.Fatalf("exchangeGitHubCode: %v", err)
	}
	id, err := cfg.fetchGitHubIdentity(context.Background(), token)
	if err != nil {
		t.Fatalf("fetchGitHubIdentity: %v", err)
	}
	if id.Email != "private@example.com" {
		t.Errorf("expected email private@example.com, got %q", id.Email)
	}
	_ = provider // not used in this path
}

func TestGitHubCallbackStateMismatch(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Jar:           newCookieJar(),
	}

	// Set a state cookie manually.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/auth/github/callback?code=x&state=wrong-state", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: "correct-state"})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for state mismatch, got %d", resp.StatusCode)
	}
}

func TestGitHubCallbackMissingStateCookie(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(srv.URL + "/auth/github/callback?code=x&state=some-state")
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing state cookie, got %d", resp.StatusCode)
	}
}

// --- Magic link tests ---

func TestMagicLinkFormRendered(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	resp, err := http.Get(srv.URL + "/auth/magic")
	if err != nil {
		t.Fatalf("GET /auth/magic: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html, got %q", ct)
	}
}

func TestMagicLinkTokenGenerationAndValidation(t *testing.T) {
	store := newMagicStore()
	email := "alice@example.com"

	token, err := randomToken(32)
	if err != nil {
		t.Fatalf("randomToken: %v", err)
	}

	store.store(token, email, magicLinkTTL)

	// Token should be consumable once.
	got, ok := store.consume(token)
	if !ok {
		t.Fatal("expected token to be valid")
	}
	if got != email {
		t.Errorf("expected email %q, got %q", email, got)
	}

	// Second consume should fail (single-use).
	_, ok2 := store.consume(token)
	if ok2 {
		t.Error("expected token to be consumed (single-use)")
	}
}

func TestMagicLinkTokenExpiry(t *testing.T) {
	store := newMagicStore()
	token, _ := randomToken(32)
	store.store(token, "bob@example.com", -1*time.Second) // already expired

	_, ok := store.consume(token)
	if ok {
		t.Error("expected expired token to be rejected")
	}
}

func TestMagicLinkPostGeneratesToken(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	form := url.Values{}
	form.Set("email", "alice@example.com")
	resp, err := http.PostForm(srv.URL+"/auth/magic", form)
	if err != nil {
		t.Fatalf("POST /auth/magic: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMagicLinkPostInvalidEmail(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	form := url.Values{}
	form.Set("email", "notanemail")
	resp, err := http.PostForm(srv.URL+"/auth/magic", form)
	if err != nil {
		t.Fatalf("POST /auth/magic: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid email, got %d", resp.StatusCode)
	}
}

func TestMagicLinkVerifyFlow(t *testing.T) {
	cfg, provider := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	// Store a valid token directly.
	token, _ := randomToken(32)
	cfg.magic.store(token, "charlie@example.com", magicLinkTTL)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Jar:           newCookieJar(),
	}
	resp, err := client.Get(srv.URL + "/auth/magic/verify?token=" + url.QueryEscape(token))
	if err != nil {
		t.Fatalf("GET /auth/magic/verify: %v", err)
	}
	defer resp.Body.Close()

	// Should redirect to /.
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", resp.StatusCode)
	}

	// Session should have been created.
	if len(provider.calls) != 1 {
		t.Fatalf("expected 1 CreateSession call, got %d", len(provider.calls))
	}
	if provider.calls[0].Email != "charlie@example.com" {
		t.Errorf("expected email charlie@example.com, got %q", provider.calls[0].Email)
	}
	if provider.calls[0].Provider != "magic" {
		t.Errorf("expected provider=magic, got %q", provider.calls[0].Provider)
	}
}

func TestMagicLinkVerifyExpiredToken(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	token, _ := randomToken(32)
	cfg.magic.store(token, "expired@example.com", -1*time.Second)

	resp, err := http.Get(srv.URL + "/auth/magic/verify?token=" + url.QueryEscape(token))
	if err != nil {
		t.Fatalf("GET /auth/magic/verify: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired token, got %d", resp.StatusCode)
	}
}

func TestMagicLinkVerifyMissingToken(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	resp, err := http.Get(srv.URL + "/auth/magic/verify")
	if err != nil {
		t.Fatalf("GET /auth/magic/verify (no token): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing token, got %d", resp.StatusCode)
	}
}

// --- Session cookie tests ---

func TestSessionCookieProperties(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	// Trigger a magic-link verify to get a session cookie back.
	token, _ := randomToken(32)
	cfg.magic.store(token, "dave@example.com", magicLinkTTL)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Jar:           newCookieJar(),
	}
	resp, err := client.Get(srv.URL + "/auth/magic/verify?token=" + url.QueryEscape(token))
	if err != nil {
		t.Fatalf("GET /auth/magic/verify: %v", err)
	}
	defer resp.Body.Close()

	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected session cookie to be set after successful auth")
	}
	if !sessionCookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("expected SameSite=Strict, got %v", sessionCookie.SameSite)
	}
	if sessionCookie.MaxAge <= 0 {
		t.Error("expected positive MaxAge on session cookie")
	}
}

// --- Logout tests ---

func TestLogout(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv := newTestServerWithAuth(t, cfg)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Jar:           newCookieJar(),
	}

	// Seed a session cookie.
	sessionToken := "existing-session-token"
	cfg.Sessions.Store(sessionToken, Identity{Email: "eve@example.com"}, sessionTTL)
	// Add it to the jar manually by hitting a route that sets it... or inject directly.
	// We inject it by adding to the jar via a synthetic response cookie.
	u, _ := url.Parse(srv.URL)
	client.Jar.SetCookies(u, []*http.Cookie{
		{Name: sessionCookieName, Value: sessionToken},
	})

	resp, err := client.Get(srv.URL + "/logout")
	if err != nil {
		t.Fatalf("GET /logout: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", resp.StatusCode)
	}
	// Verify the session cookie is cleared (MaxAge=-1).
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			if c.MaxAge != -1 {
				t.Errorf("expected session cookie MaxAge=-1 (clear), got %d", c.MaxAge)
			}
		}
	}
	// Verify session was removed from store.
	if _, ok := cfg.Sessions.Lookup(sessionToken); ok {
		t.Error("expected session to be removed from store after logout (via InvalidateSession)")
	}
}

// --- MemSessionStore tests ---

func TestMemSessionStore(t *testing.T) {
	store := NewMemSessionStore()
	id := Identity{Email: "frank@example.com", Provider: "github"}

	store.Store("tok1", id, time.Second)
	got, ok := store.Lookup("tok1")
	if !ok {
		t.Fatal("expected to find tok1")
	}
	if got.Email != id.Email {
		t.Errorf("expected email %q, got %q", id.Email, got.Email)
	}

	store.Delete("tok1")
	_, ok2 := store.Lookup("tok1")
	if ok2 {
		t.Error("expected tok1 to be deleted")
	}
}

func TestMemSessionStoreExpiry(t *testing.T) {
	store := NewMemSessionStore()
	store.Store("expiring", Identity{Email: "g@example.com"}, -1*time.Second)
	_, ok := store.Lookup("expiring")
	if ok {
		t.Error("expected expired entry to be rejected")
	}
}

// --- cookieJar helper ---

// newCookieJar returns a simple in-memory cookie jar.
func newCookieJar() http.CookieJar {
	jar := &simpleCookieJar{cookies: make(map[string][]*http.Cookie)}
	return jar
}

type simpleCookieJar struct {
	mu      sync.Mutex
	cookies map[string][]*http.Cookie
}

func (j *simpleCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	key := u.Host
	j.cookies[key] = append(j.cookies[key], cookies...)
}

func (j *simpleCookieJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cookies[u.Host]
}
