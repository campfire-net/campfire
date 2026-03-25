// cmd/cf-ui/middleware.go — Session validation and CSRF protection middleware.
//
// SessionMiddleware validates the session cookie on every request, rejects
// invalid/expired sessions with 401, and injects the authenticated Identity
// into the request context. It also implements sliding-window TTL renewal:
// if the session is valid but past half its TTL, the cookie MaxAge is
// refreshed.
//
// CSRFMiddleware generates a per-session CSRF token and validates it on all
// POST requests. The token is injected into templates via a context value
// and must be submitted as a hidden field named "_csrf".
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

// contextKey is an unexported type for context keys in this package.
type contextKey int

const (
	// contextKeyIdentity is the context key for the authenticated Identity.
	contextKeyIdentity contextKey = iota
	// contextKeyCSRFToken is the context key for the CSRF token string.
	contextKeyCSRFToken
)

// IdentityFromContext retrieves the authenticated Identity from the context.
// Returns the zero value and false if not present.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	v, ok := ctx.Value(contextKeyIdentity).(Identity)
	return v, ok
}

// CSRFTokenFromContext retrieves the CSRF token from the context.
// Returns empty string if not present.
func CSRFTokenFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyCSRFToken).(string)
	return v
}

// SessionRenewer is an optional interface that SessionStore implementations
// may satisfy to support sliding-window TTL renewal. The middleware checks
// whether the store also implements SessionRenewer via type assertion.
type SessionRenewer interface {
	// LookupWithExpiry retrieves the identity and remaining TTL for a token.
	// Returns (identity, remaining, true) if valid, (_, 0, false) if not found or expired.
	LookupWithExpiry(token string) (Identity, time.Duration, bool)
}

// LookupWithExpiry implements SessionRenewer for MemSessionStore.
func (s *MemSessionStore) LookupWithExpiry(token string) (Identity, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[token]
	if !ok {
		return Identity{}, 0, false
	}
	remaining := time.Until(e.expiresAt)
	if remaining <= 0 {
		delete(s.entries, token)
		return Identity{}, 0, false
	}
	return e.identity, remaining, true
}

// SessionMiddleware returns an HTTP middleware that validates the session cookie.
// Protected routes wrapped with this middleware require a valid session.
// On success it injects the Identity into the request context.
// On failure it responds with 401 Unauthorized.
//
// Sliding window renewal: if the remaining TTL is less than half of sessionTTL,
// the cookie MaxAge and server-side TTL are refreshed to sessionTTL.
func SessionMiddleware(sessions SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || cookie.Value == "" {
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}

			token := cookie.Value

			// Try extended lookup first (sliding window support).
			var identity Identity
			var needsRenewal bool

			if renewer, ok := sessions.(SessionRenewer); ok {
				var remaining time.Duration
				identity, remaining, ok = renewer.LookupWithExpiry(token)
				if !ok {
					http.Error(w, "session expired or invalid", http.StatusUnauthorized)
					return
				}
				// Renew if past half the TTL.
				if remaining < sessionTTL/2 {
					needsRenewal = true
				}
			} else {
				var found bool
				identity, found = sessions.Lookup(token)
				if !found {
					http.Error(w, "session expired or invalid", http.StatusUnauthorized)
					return
				}
			}

			if needsRenewal {
				// Refresh server-side TTL.
				sessions.Store(token, identity, sessionTTL)
				// Refresh cookie TTL.
				http.SetCookie(w, &http.Cookie{
					Name:     sessionCookieName,
					Value:    token,
					Path:     "/",
					MaxAge:   int(sessionTTL.Seconds()),
					HttpOnly: true,
					Secure:   r.TLS != nil,
					SameSite: http.SameSiteStrictMode,
				})
			}

			// Inject identity into context and pass to the next handler.
			ctx := context.WithValue(r.Context(), contextKeyIdentity, identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// csrfStore holds per-session CSRF tokens in memory.
// CSRF tokens are derived from the session token using HMAC-SHA256 so we
// don't need additional storage — we just verify on POST.
// The secret is generated once per server start.
type csrfStore struct {
	mu     sync.Mutex
	secret []byte
}

// newCSRFStore creates a csrfStore with a random secret.
func newCSRFStore() (*csrfStore, error) {
	secret, err := randomBytes(32)
	if err != nil {
		return nil, err
	}
	return &csrfStore{secret: secret}, nil
}

// tokenFor generates the CSRF token for a given session token.
// The token is HMAC-SHA256(secret, sessionToken) encoded as base64url.
func (s *csrfStore) tokenFor(sessionToken string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(sessionToken))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validate checks whether csrfToken matches the expected token for sessionToken.
func (s *csrfStore) validate(sessionToken, csrfToken string) bool {
	expected := s.tokenFor(sessionToken)
	// Use constant-time comparison.
	return hmac.Equal([]byte(expected), []byte(csrfToken))
}

// randomBytes generates n cryptographically random bytes.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// CSRFMiddleware returns an HTTP middleware that:
//   - Generates a CSRF token derived from the session cookie and injects it
//     into the request context (for templates to embed in forms).
//   - Validates the _csrf field on all POST requests.
//   - Returns 403 Forbidden if the CSRF token is missing or invalid.
//
// This middleware must run after SessionMiddleware on protected routes, so
// that the session cookie is guaranteed to be present. It can also be used
// on public POST routes: in that case no session cookie exists and we derive
// the token from an empty string (per-server secret is still applied).
func CSRFMiddleware(store *csrfStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Determine the session token (may be empty for unauthenticated requests).
			sessionToken := ""
			if cookie, err := r.Cookie(sessionCookieName); err == nil {
				sessionToken = cookie.Value
			}

			// Generate a CSRF token and inject into context.
			csrfToken := store.tokenFor(sessionToken)
			ctx := context.WithValue(r.Context(), contextKeyCSRFToken, csrfToken)

			// On POST requests, validate the submitted CSRF token.
			if r.Method == http.MethodPost {
				if err := r.ParseForm(); err != nil {
					http.Error(w, "invalid form", http.StatusBadRequest)
					return
				}
				submitted := r.FormValue("_csrf")
				if submitted == "" {
					http.Error(w, "CSRF token missing", http.StatusForbidden)
					return
				}
				if !store.validate(sessionToken, submitted) {
					http.Error(w, "CSRF token invalid", http.StatusForbidden)
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
