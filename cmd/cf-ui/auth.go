// cmd/cf-ui/auth.go — GitHub OAuth and magic link authentication handlers.
//
// Flow:
//  1. GitHub OAuth: GET /auth/github → GitHub OAuth → GET /auth/github/callback → session cookie
//  2. Magic link:   GET /auth/magic  → form → POST /auth/magic → (logged link) →
//                   GET /auth/magic/verify?token=... → session cookie
//
// Environment variables required for OAuth:
//
//	GITHUB_CLIENT_ID     — GitHub OAuth application client ID
//	GITHUB_CLIENT_SECRET — GitHub OAuth application client secret
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName   = "cf_session"
	sessionTTL          = time.Hour
	magicLinkTTL        = 15 * time.Minute
	oauthStateCookieName = "cf_oauth_state"
	oauthStateTTL        = 10 * time.Minute
)

// AuthProvider bridges authentication identity to a campfire session token.
// The real implementation wires to the campfire store; tests use a fake.
type AuthProvider interface {
	// CreateSession creates (or resumes) a campfire session for the given identity.
	// It returns an opaque session token that identifies the session server-side.
	CreateSession(ctx context.Context, identity Identity) (token string, err error)

	// InvalidateSession invalidates a previously issued session token.
	// Implementations should be idempotent — if the token is unknown, return nil.
	InvalidateSession(ctx context.Context, token string) error
}

// Identity carries the authenticated user's identity from any provider.
type Identity struct {
	// Email is the verified email address. Required.
	Email string
	// DisplayName is the human-readable name (GitHub login, or email local-part).
	DisplayName string
	// AvatarURL is an optional profile image URL.
	AvatarURL string
	// Provider identifies the login method: "github" or "magic".
	Provider string
}

// SessionStore manages server-side session tokens.
// The in-memory implementation is suitable for single-instance deployments.
// A durable implementation backed by Azure Table Storage is a future item.
type SessionStore interface {
	// Store persists a session token with the given TTL.
	Store(token string, identity Identity, ttl time.Duration)
	// Lookup retrieves the identity for a session token if it is still valid.
	Lookup(token string) (Identity, bool)
	// Delete removes a session token.
	Delete(token string)
}

// MemSessionStore is an in-memory SessionStore implementation.
type MemSessionStore struct {
	mu      sync.Mutex
	entries map[string]sessionEntry
}

type sessionEntry struct {
	identity  Identity
	expiresAt time.Time
}

// NewMemSessionStore returns an initialized MemSessionStore.
func NewMemSessionStore() *MemSessionStore {
	return &MemSessionStore{entries: make(map[string]sessionEntry)}
}

func (s *MemSessionStore) Store(token string, identity Identity, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[token] = sessionEntry{identity: identity, expiresAt: time.Now().Add(ttl)}
}

func (s *MemSessionStore) Lookup(token string) (Identity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[token]
	if !ok || time.Now().After(e.expiresAt) {
		delete(s.entries, token)
		return Identity{}, false
	}
	return e.identity, true
}

func (s *MemSessionStore) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, token)
}

// magicTokenEntry holds a pending magic-link token.
type magicTokenEntry struct {
	email     string
	expiresAt time.Time
}

// MagicStore manages short-lived magic-link tokens (in-memory).
type MagicStore struct {
	mu     sync.Mutex
	tokens map[string]magicTokenEntry
}

func newMagicStore() *MagicStore {
	return &MagicStore{tokens: make(map[string]magicTokenEntry)}
}

func (m *MagicStore) store(token, email string, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[token] = magicTokenEntry{email: email, expiresAt: time.Now().Add(ttl)}
}

// consume atomically validates and removes the token.
func (m *MagicStore) consume(token string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tokens[token]
	if !ok || time.Now().After(e.expiresAt) {
		delete(m.tokens, token)
		return "", false
	}
	delete(m.tokens, token)
	return e.email, true
}

// AuthConfig holds dependencies for auth handlers.
type AuthConfig struct {
	Logger         *slog.Logger
	GitHubClientID string
	GitHubSecret   string
	// BaseURL is the public-facing base URL, e.g. "https://app.getcampfire.dev".
	// Used to construct OAuth redirect URIs and magic-link verify URLs.
	BaseURL        string
	Sessions       SessionStore
	Auth           AuthProvider
	magic          *MagicStore
	// httpClient is overridden in tests to use a local httptest server.
	httpClient *http.Client
}

// newAuthConfig constructs AuthConfig reading GitHub credentials from the environment.
// env is a func(string)string so tests can inject values without mutating os.Getenv.
func newAuthConfig(logger *slog.Logger, env func(string) string, baseURL string, sessions SessionStore, auth AuthProvider) *AuthConfig {
	clientID := env("GITHUB_CLIENT_ID")
	secret := env("GITHUB_CLIENT_SECRET")
	return &AuthConfig{
		Logger:         logger,
		GitHubClientID: clientID,
		GitHubSecret:   secret,
		BaseURL:        baseURL,
		Sessions:       sessions,
		Auth:           auth,
		magic:          newMagicStore(),
		httpClient:     http.DefaultClient,
	}
}

// registerAuthRoutes adds auth routes to mux.
// csrfMW is applied to the magic login GET (to inject the token into context
// for templates) and POST (to validate the submitted token).
// When csrfMW is nil, no CSRF protection is applied (used in pre-middleware tests).
func registerAuthRoutes(mux *http.ServeMux, cfg *AuthConfig, csrfMW func(http.Handler) http.Handler) {
	mux.HandleFunc("GET /auth/github", cfg.handleGitHubLogin)
	mux.HandleFunc("GET /auth/github/callback", cfg.handleGitHubCallback)
	if csrfMW != nil {
		// Wrap both GET and POST for the magic login — GET injects the CSRF
		// token into context so the template can embed it; POST validates it.
		mux.Handle("GET /auth/magic", csrfMW(http.HandlerFunc(cfg.handleMagicForm)))
		mux.Handle("POST /auth/magic", csrfMW(http.HandlerFunc(cfg.handleMagicRequest)))
	} else {
		mux.HandleFunc("GET /auth/magic", cfg.handleMagicForm)
		mux.HandleFunc("POST /auth/magic", cfg.handleMagicRequest)
	}
	mux.HandleFunc("GET /auth/magic/verify", cfg.handleMagicVerify)
	mux.HandleFunc("GET /logout", cfg.handleLogout)
}

// --- GitHub OAuth ---

// handleGitHubLogin redirects the browser to GitHub's OAuth authorization page.
func (c *AuthConfig) handleGitHubLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken(32)
	if err != nil {
		c.Logger.Error("auth: generate oauth state", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Store state in a short-lived cookie for CSRF validation in the callback.
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   int(oauthStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode, // Lax required — GitHub redirects back via GET
	})

	redirectURI := c.BaseURL + "/auth/github/callback"
	authURL := githubAuthURL(c.GitHubClientID, redirectURI, state)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// githubAuthURL constructs the GitHub OAuth authorization URL.
func githubAuthURL(clientID, redirectURI, state string) string {
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("scope", "read:user user:email")
	v.Set("state", state)
	return "https://github.com/login/oauth/authorize?" + v.Encode()
}

// handleGitHubCallback handles the OAuth callback from GitHub.
func (c *AuthConfig) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	// Validate CSRF state.
	stateCookie, err := r.Cookie(oauthStateCookieName)
	if err != nil || stateCookie.Value == "" {
		http.Error(w, "missing oauth state", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "oauth state mismatch", http.StatusBadRequest)
		return
	}
	// Clear state cookie immediately.
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing oauth code", http.StatusBadRequest)
		return
	}

	token, err := c.exchangeGitHubCode(r.Context(), code)
	if err != nil {
		c.Logger.Error("auth: github token exchange", "err", err)
		http.Error(w, "oauth token exchange failed", http.StatusBadGateway)
		return
	}

	identity, err := c.fetchGitHubIdentity(r.Context(), token)
	if err != nil {
		c.Logger.Error("auth: fetch github identity", "err", err)
		http.Error(w, "failed to fetch user identity", http.StatusBadGateway)
		return
	}
	identity.Provider = "github"

	c.createSessionAndRedirect(w, r, identity)
}

// exchangeGitHubCode exchanges an authorization code for an access token.
func (c *AuthConfig) exchangeGitHubCode(ctx context.Context, code string) (string, error) {
	v := url.Values{}
	v.Set("client_id", c.GitHubClientID)
	v.Set("client_secret", c.GitHubSecret)
	v.Set("code", code)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://github.com/login/oauth/access_token",
		strings.NewReader(v.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github token exchange status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("github oauth error %s: %s", result.Error, result.ErrorDesc)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("empty access token in response")
	}
	return result.AccessToken, nil
}

// githubUser is the subset of the GitHub user API response we need.
type githubUser struct {
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Email     string `json:"email"`
}

// githubEmail is one entry from the /user/emails endpoint.
type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// fetchGitHubIdentity calls the GitHub API to retrieve user info.
func (c *AuthConfig) fetchGitHubIdentity(ctx context.Context, token string) (Identity, error) {
	user, err := c.fetchGitHubUser(ctx, token)
	if err != nil {
		return Identity{}, err
	}

	email := user.Email
	if email == "" {
		// User has hidden their email; query the emails endpoint.
		email, err = c.fetchPrimaryGitHubEmail(ctx, token)
		if err != nil {
			return Identity{}, fmt.Errorf("fetch github email: %w", err)
		}
	}

	displayName := user.Name
	if displayName == "" {
		displayName = user.Login
	}

	return Identity{
		Email:       email,
		DisplayName: displayName,
		AvatarURL:   user.AvatarURL,
	}, nil
}

func (c *AuthConfig) fetchGitHubUser(ctx context.Context, token string) (githubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return githubUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return githubUser{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return githubUser{}, fmt.Errorf("github user API status %d: %s", resp.StatusCode, body)
	}
	var u githubUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return githubUser{}, fmt.Errorf("decode github user: %w", err)
	}
	return u, nil
}

func (c *AuthConfig) fetchPrimaryGitHubEmail(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/emails", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github emails API status %d: %s", resp.StatusCode, body)
	}
	var emails []githubEmail
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", fmt.Errorf("decode github emails: %w", err)
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, nil
		}
	}
	// Fall back to any verified email.
	for _, e := range emails {
		if e.Verified {
			return e.Email, nil
		}
	}
	return "", fmt.Errorf("no verified email found in github account")
}

// --- Magic link ---

// handleMagicForm renders the magic link email entry form.
func (c *AuthConfig) handleMagicForm(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Title     string
		Version   string
		CSRFToken string
	}{
		Title:     "Sign in — Campfire",
		Version:   Version,
		CSRFToken: CSRFTokenFromContext(r.Context()),
	}
	if err := renderPage(w, "magic_login.html", data); err != nil {
		c.Logger.Error("template error", "template", "magic_login.html", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// handleMagicRequest generates a magic link token and logs the verify URL.
func (c *AuthConfig) handleMagicRequest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" || !strings.Contains(email, "@") {
		http.Error(w, "valid email required", http.StatusBadRequest)
		return
	}

	token, err := randomToken(32)
	if err != nil {
		c.Logger.Error("auth: generate magic token", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	c.magic.store(token, email, magicLinkTTL)

	verifyURL := c.BaseURL + "/auth/magic/verify?token=" + url.QueryEscape(token)
	// Email sending is deferred — log the link for operator convenience.
	c.Logger.Info("auth: magic link generated (email delivery pending)", "email", email, "verify_url", verifyURL)

	// Respond with a simple confirmation — real email would be sent here.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Check your email for a sign-in link. (Dev: %s)\n", verifyURL)
}

// handleMagicVerify validates the magic-link token and creates a session.
func (c *AuthConfig) handleMagicVerify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	email, ok := c.magic.consume(token)
	if !ok {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}

	// Derive display name from email local-part.
	localPart := email
	if idx := strings.Index(email, "@"); idx > 0 {
		localPart = email[:idx]
	}

	identity := Identity{
		Email:       email,
		DisplayName: localPart,
		Provider:    "magic",
	}
	c.createSessionAndRedirect(w, r, identity)
}

// --- Logout ---

func (c *AuthConfig) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" {
		// Remove from the local session store first.
		c.Sessions.Delete(cookie.Value)
		// Notify the campfire auth provider (best-effort).
		if authErr := c.Auth.InvalidateSession(r.Context(), cookie.Value); authErr != nil {
			c.Logger.Warn("auth: invalidate session", "err", authErr)
		}
	}

	// Clear the session cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- Shared session creation ---

// createSessionAndRedirect calls AuthProvider.CreateSession, sets the session cookie,
// and redirects to the home page.
func (c *AuthConfig) createSessionAndRedirect(w http.ResponseWriter, r *http.Request, identity Identity) {
	sessionToken, err := c.Auth.CreateSession(r.Context(), identity)
	if err != nil {
		c.Logger.Error("auth: create campfire session", "err", err)
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}

	// Store session in our local store so middleware can look it up.
	c.Sessions.Store(sessionToken, identity, sessionTTL)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- Utilities ---

// randomToken generates a cryptographically random URL-safe base64 token of nBytes entropy.
func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
