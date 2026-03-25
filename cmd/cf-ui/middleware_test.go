// cmd/cf-ui/middleware_test.go — tests for SessionMiddleware and CSRFMiddleware.
package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- SessionMiddleware tests ---

// identityHandler is a test handler that writes the authenticated user's email
// (from context) into the response so we can verify context injection.
func identityHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, "no identity in context", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(id.Email)) //nolint:errcheck
	})
}

func TestSessionMiddlewareValidSession(t *testing.T) {
	store := NewMemSessionStore()
	sessionToken := "valid-token"
	identity := Identity{Email: "alice@example.com", Provider: "magic"}
	store.Store(sessionToken, identity, time.Hour)

	mw := SessionMiddleware(store)
	srv := httptest.NewServer(mw(identityHandler()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for valid session, got %d", resp.StatusCode)
	}

	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if string(buf[:n]) != identity.Email {
		t.Errorf("expected identity email %q in body, got %q", identity.Email, string(buf[:n]))
	}
}

func TestSessionMiddlewareInjectsIdentityIntoContext(t *testing.T) {
	store := NewMemSessionStore()
	sessionToken := "ctx-token"
	identity := Identity{Email: "bob@example.com", Provider: "github", DisplayName: "Bob"}
	store.Store(sessionToken, identity, time.Hour)

	mw := SessionMiddleware(store)
	srv := httptest.NewServer(mw(identityHandler()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if string(buf[:n]) != "bob@example.com" {
		t.Errorf("expected bob@example.com from context, got %q", string(buf[:n]))
	}
}

func TestSessionMiddlewareExpiredSession(t *testing.T) {
	store := NewMemSessionStore()
	sessionToken := "expired-token"
	store.Store(sessionToken, Identity{Email: "carol@example.com"}, -1*time.Second) // already expired

	mw := SessionMiddleware(store)
	srv := httptest.NewServer(mw(identityHandler()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired session, got %d", resp.StatusCode)
	}
}

func TestSessionMiddlewareMissingCookie(t *testing.T) {
	store := NewMemSessionStore()
	mw := SessionMiddleware(store)
	srv := httptest.NewServer(mw(identityHandler()))
	defer srv.Close()

	// No session cookie at all.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing session cookie, got %d", resp.StatusCode)
	}
}

func TestSessionMiddlewareInvalidToken(t *testing.T) {
	store := NewMemSessionStore()
	mw := SessionMiddleware(store)
	srv := httptest.NewServer(mw(identityHandler()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-a-valid-token"})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid session token, got %d", resp.StatusCode)
	}
}

func TestSessionMiddlewareRenewal(t *testing.T) {
	store := NewMemSessionStore()
	sessionToken := "renewal-token"
	identity := Identity{Email: "dave@example.com", Provider: "magic"}

	// Store with a short remaining TTL — just under half of sessionTTL (1 hour / 2 = 30 min).
	// Use 20 minutes remaining so it's clearly below the half-TTL threshold.
	shortTTL := 20 * time.Minute
	store.Store(sessionToken, identity, shortTTL)

	mw := SessionMiddleware(store)

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// The session should have been renewed to a full sessionTTL.
	// Verify the renewed TTL via LookupWithExpiry.
	_, remaining, ok := store.LookupWithExpiry(sessionToken)
	if !ok {
		t.Fatal("expected session to still be valid after renewal")
	}
	// After renewal, remaining should be close to sessionTTL (≥ sessionTTL - 5s slack).
	if remaining < sessionTTL-5*time.Second {
		t.Errorf("expected renewed TTL near %v, got %v", sessionTTL, remaining)
	}

	// Verify cookie renewal header is set.
	renewed := false
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName && c.MaxAge == int(sessionTTL.Seconds()) {
			renewed = true
		}
	}
	if !renewed {
		t.Error("expected renewed session cookie in response with full MaxAge")
	}
}

func TestSessionMiddlewareNoRenewalWhenFresh(t *testing.T) {
	store := NewMemSessionStore()
	sessionToken := "fresh-token"
	store.Store(sessionToken, Identity{Email: "eve@example.com"}, sessionTTL) // full TTL

	mw := SessionMiddleware(store)
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mw(okHandler))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// No Set-Cookie header should be present for renewal when TTL is fresh.
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			t.Error("expected no session cookie renewal for fresh session")
		}
	}
}

// --- Public routes bypass session middleware ---

func TestPublicRoutesNoSession(t *testing.T) {
	srv, _ := newTestServer(t)

	// Public routes must be reachable without a session cookie.
	publicRoutes := []string{
		"/healthz",
		"/static/campfire.css",
		"/static/htmx.min.js",
		"/auth/magic",
		"/auth/github",
	}

	for _, route := range publicRoutes {
		resp, err := http.Get(srv.URL + route)
		if err != nil {
			t.Fatalf("GET %s: %v", route, err)
		}
		resp.Body.Close()
		// GitHub and magic auth may redirect, but they must not return 401.
		if resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("route %s returned 401 without session — public routes must bypass session middleware", route)
		}
	}
}

func TestProtectedRoutesRequireSession(t *testing.T) {
	srv, _ := newTestServer(t)

	// Without a session, protected routes return 401.
	protectedRoutes := []string{"/", "/c/test-id"}
	for _, route := range protectedRoutes {
		resp, err := http.Get(srv.URL + route)
		if err != nil {
			t.Fatalf("GET %s: %v", route, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("route %s returned %d without session — expected 401", route, resp.StatusCode)
		}
	}
}

// --- CSRFMiddleware tests ---

func newTestCSRFStore(t *testing.T) *csrfStore {
	t.Helper()
	store, err := newCSRFStore()
	if err != nil {
		t.Fatalf("newCSRFStore: %v", err)
	}
	return store
}

// echoCSRFHandler writes the CSRF token from context into the response body.
func echoCSRFHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := CSRFTokenFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(token)) //nolint:errcheck
	})
}

func TestCSRFMiddlewareInjectsToken(t *testing.T) {
	csrf := newTestCSRFStore(t)
	mw := CSRFMiddleware(csrf)
	srv := httptest.NewServer(mw(echoCSRFHandler()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	token := string(buf[:n])
	if token == "" {
		t.Error("expected CSRF token in response body, got empty string")
	}
}

func TestCSRFMiddlewareValidToken(t *testing.T) {
	csrf := newTestCSRFStore(t)
	mw := CSRFMiddleware(csrf)
	srv := httptest.NewServer(mw(echoCSRFHandler()))
	defer srv.Close()

	// Generate the expected token for no session.
	expectedToken := csrf.tokenFor("")

	form := url.Values{}
	form.Set("_csrf", expectedToken)
	form.Set("email", "test@example.com")

	resp, err := http.PostForm(srv.URL+"/", form)
	if err != nil {
		t.Fatalf("POST /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for valid CSRF token, got %d", resp.StatusCode)
	}
}

func TestCSRFMiddlewareMissingToken(t *testing.T) {
	csrf := newTestCSRFStore(t)
	mw := CSRFMiddleware(csrf)
	srv := httptest.NewServer(mw(echoCSRFHandler()))
	defer srv.Close()

	// POST without _csrf field.
	form := url.Values{}
	form.Set("email", "test@example.com")

	resp, err := http.PostForm(srv.URL+"/", form)
	if err != nil {
		t.Fatalf("POST /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for missing CSRF token, got %d", resp.StatusCode)
	}
}

func TestCSRFMiddlewareInvalidToken(t *testing.T) {
	csrf := newTestCSRFStore(t)
	mw := CSRFMiddleware(csrf)
	srv := httptest.NewServer(mw(echoCSRFHandler()))
	defer srv.Close()

	form := url.Values{}
	form.Set("_csrf", "not-a-valid-csrf-token")

	resp, err := http.PostForm(srv.URL+"/", form)
	if err != nil {
		t.Fatalf("POST /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for invalid CSRF token, got %d", resp.StatusCode)
	}
}

func TestCSRFMiddlewareTokenBoundToSession(t *testing.T) {
	csrf := newTestCSRFStore(t)
	mw := CSRFMiddleware(csrf)
	srv := httptest.NewServer(mw(echoCSRFHandler()))
	defer srv.Close()

	// Token generated for session A should not validate for session B.
	tokenForSessionA := csrf.tokenFor("session-a")

	// POST using session B's cookie but session A's CSRF token.
	form := url.Values{}
	form.Set("_csrf", tokenForSessionA)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-b"})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 when CSRF token is from a different session, got %d", resp.StatusCode)
	}
}

func TestCSRFMiddlewareGETNotValidated(t *testing.T) {
	csrf := newTestCSRFStore(t)
	mw := CSRFMiddleware(csrf)
	srv := httptest.NewServer(mw(echoCSRFHandler()))
	defer srv.Close()

	// GET requests should not require CSRF validation.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for GET (no CSRF required), got %d", resp.StatusCode)
	}
}

// --- CSRF token injection into magic login form ---

func TestMagicLoginFormIncludesCSRFField(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv, _ := newTestServerWithAuth(t, cfg)

	resp, err := http.Get(srv.URL + "/auth/magic")
	if err != nil {
		t.Fatalf("GET /auth/magic: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Read the full body to check for CSRF hidden field.
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	if !strings.Contains(body, `name="_csrf"`) {
		t.Error("magic login form must include a hidden _csrf field for CSRF protection")
	}
}

// --- CSRF rejection on POST /auth/magic ---

func TestMagicLinkPostMissingCSRF(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv, _ := newTestServerWithAuth(t, cfg)

	// POST without _csrf — must be rejected with 403.
	form := url.Values{}
	form.Set("email", "alice@example.com")

	resp, err := http.PostForm(srv.URL+"/auth/magic", form)
	if err != nil {
		t.Fatalf("POST /auth/magic: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for POST without CSRF token, got %d", resp.StatusCode)
	}
}

func TestMagicLinkPostInvalidCSRF(t *testing.T) {
	cfg, _ := newTestAuthConfig(t, nil)
	srv, _ := newTestServerWithAuth(t, cfg)

	form := url.Values{}
	form.Set("email", "alice@example.com")
	form.Set("_csrf", "garbage-token")

	resp, err := http.PostForm(srv.URL+"/auth/magic", form)
	if err != nil {
		t.Fatalf("POST /auth/magic: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for invalid CSRF token, got %d", resp.StatusCode)
	}
}
