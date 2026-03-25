// cmd/cf-ui/main_test.go — tests for the cf-ui HTTP server
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newTestServer returns an httptest.Server backed by buildMuxWithAuth, along
// with the AuthConfig so tests can seed sessions for protected routes.
func newTestServer(t *testing.T) (*httptest.Server, *AuthConfig) {
	t.Helper()
	logger := newDiscardLogger()
	authCfg := newAuthConfig(logger, func(string) string { return "" }, "http://localhost", NewMemSessionStore(), noopAuthProvider{})
	bundle := buildMuxWithAuth(logger, authCfg)
	srv := httptest.NewServer(bundle.handler)
	t.Cleanup(srv.Close)
	return srv, authCfg
}

// authenticatedClient returns an http.Client with a valid session cookie pre-loaded
// for the given server URL. The session is seeded in the provided SessionStore.
func authenticatedClient(t *testing.T, srvURL string, sessions SessionStore) *http.Client {
	t.Helper()
	sessionToken := "test-session-" + t.Name()
	sessions.Store(sessionToken, Identity{Email: "test@example.com", DisplayName: "Test User", Provider: "magic"}, time.Hour)

	jar := newCookieJar()
	u, _ := url.Parse(srvURL)
	jar.SetCookies(u, []*http.Cookie{{Name: sessionCookieName, Value: sessionToken}})
	return &http.Client{Jar: jar}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected application/json content-type, got %q", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status=ok, got %q", body["status"])
	}
}

func TestRouteIndex(t *testing.T) {
	srv, authCfg := newTestServer(t)

	client := authenticatedClient(t, srv.URL, authCfg.Sessions)
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRouteCampfireDetail(t *testing.T) {
	srv, authCfg := newTestServer(t)

	client := authenticatedClient(t, srv.URL, authCfg.Sessions)
	id := "abc123"
	resp, err := client.Get(srv.URL + "/c/" + id)
	if err != nil {
		t.Fatalf("GET /c/%s: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRouteStaticAsset(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/static/htmx.min.js")
	if err != nil {
		t.Fatalf("GET /static/htmx.min.js: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestCampfireCSS(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/static/campfire.css")
	if err != nil {
		t.Fatalf("GET /static/campfire.css: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("expected text/css content-type, got %q", ct)
	}
}

func TestBaseTemplateIncludesCSS(t *testing.T) {
	srv, authCfg := newTestServer(t)

	client := authenticatedClient(t, srv.URL, authCfg.Sessions)
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	if !strings.Contains(body, "campfire.css") {
		t.Fatal("index page does not include campfire.css link")
	}
}

func TestRouteNotFound(t *testing.T) {
	srv, authCfg := newTestServer(t)

	// /does-not-exist falls through to the session-protected / handler which
	// returns 404 after verifying the session. Use an authenticated client.
	client := authenticatedClient(t, srv.URL, authCfg.Sessions)
	resp, err := client.Get(srv.URL + "/does-not-exist")
	if err != nil {
		t.Fatalf("GET /does-not-exist: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}
