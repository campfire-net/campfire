// cmd/cf-ui/send_test.go — tests for POST /c/{id}/send
package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeSender is a test double for MessageSender.
type fakeSender struct {
	// sent holds arguments from each call to Send.
	sent []fakeMsg
	// err, if non-nil, is returned by Send.
	err error
}

type fakeMsg struct {
	campfireID string
	sender     string
	text       string
	tags       []string
}

func (f *fakeSender) Send(campfireID, sender, text string, tags []string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.sent = append(f.sent, fakeMsg{campfireID: campfireID, sender: sender, text: text, tags: tags})
	return "test-msg-id", nil
}

// newTestServerWithSender builds a test server with an injected MessageSender.
// It returns the server, bundle (for CSRF token generation), and session store.
func newTestServerWithSender(t *testing.T, sender MessageSender) (*httptest.Server, *muxBundle) {
	t.Helper()
	logger := newDiscardLogger()
	authCfg := newAuthConfig(logger, func(string) string { return "" }, "http://localhost", NewMemSessionStore(), noopAuthProvider{})

	// Build a minimal mux that includes only the routes needed for send tests.
	reg := NewMetricsRegistry()
	hub := NewSSEHub(authCfg.Sessions, logger).WithMetrics(reg)
	csrf, err := newCSRFStore()
	if err != nil {
		t.Fatalf("newCSRFStore: %v", err)
	}
	detail := NewCampfireDetailHandlers(logger, nil)

	mux := http.NewServeMux()
	sessionMW := SessionMiddleware(authCfg.Sessions)
	csrfMW := CSRFMiddleware(csrf)

	registerAuthRoutes(mux, authCfg, csrfMW)
	mux.Handle("GET /healthz", http.HandlerFunc(handleHealthz))
	mux.Handle("GET /c/{id}", sessionMW(csrfMW(http.HandlerFunc(detail.HandleDetail))))
	mux.Handle("POST /c/{id}/send", sessionMW(csrfMW(handleSend(logger, sender, hub))))

	patchedBundle := &muxBundle{
		handler:   mux,
		authCfg:   authCfg,
		csrfStore: csrf,
		hub:       hub,
		metrics:   reg,
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, patchedBundle
}

// sendRequest posts to /c/{id}/send with the given form values.
// It sets the session cookie and optionally the CSRF token.
func sendRequest(t *testing.T, srvURL string, sessions SessionStore, csrf *csrfStore, campfireID string, form url.Values) *http.Response {
	t.Helper()

	sessionToken := "send-test-session-" + t.Name()
	sessions.Store(sessionToken, Identity{Email: "test@example.com", DisplayName: "Tester"}, time.Hour)

	// Set valid CSRF token if not already provided.
	if form.Get("_csrf") == "" {
		form.Set("_csrf", csrf.tokenFor(sessionToken))
	}

	req, err := http.NewRequest(http.MethodPost, srvURL+"/c/"+campfireID+"/send", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /c/%s/send: %v", campfireID, err)
	}
	return resp
}

func TestSendValidMessage(t *testing.T) {
	fake := &fakeSender{}
	srv, bundle := newTestServerWithSender(t, fake)

	form := url.Values{}
	form.Set("message", "hello world")
	form.Set("tag", "status")

	resp := sendRequest(t, srv.URL, bundle.authCfg.Sessions, bundle.csrfStore, "test-cf-id", form)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if len(fake.sent) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(fake.sent))
	}
	got := fake.sent[0]
	if got.campfireID != "test-cf-id" {
		t.Errorf("campfireID: got %q, want %q", got.campfireID, "test-cf-id")
	}
	if got.text != "hello world" {
		t.Errorf("text: got %q, want %q", got.text, "hello world")
	}
	if len(got.tags) != 1 || got.tags[0] != "status" {
		t.Errorf("tags: got %v, want [status]", got.tags)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "message-card") {
		t.Errorf("response should contain message-card HTML, got: %s", bodyStr)
	}
}

func TestSendMissingCSRF(t *testing.T) {
	fake := &fakeSender{}
	srv, bundle := newTestServerWithSender(t, fake)

	sessionToken := "csrf-test-session"
	bundle.authCfg.Sessions.Store(sessionToken, Identity{Email: "test@example.com"}, time.Hour)

	form := url.Values{}
	form.Set("message", "hello world")
	// Intentionally omit _csrf.

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/c/test-id/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for missing CSRF, got %d", resp.StatusCode)
	}
	if len(fake.sent) != 0 {
		t.Error("expected no send calls when CSRF is missing")
	}
}

func TestSendEmptyMessage(t *testing.T) {
	fake := &fakeSender{}
	srv, bundle := newTestServerWithSender(t, fake)

	form := url.Values{}
	form.Set("message", "   ") // whitespace only

	resp := sendRequest(t, srv.URL, bundle.authCfg.Sessions, bundle.csrfStore, "test-id", form)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for empty message, got %d", resp.StatusCode)
	}
	if len(fake.sent) != 0 {
		t.Error("expected no send calls for empty message")
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "empty") {
		t.Errorf("expected error about empty message, got: %s", body)
	}
}

func TestSendSenderError(t *testing.T) {
	fake := &fakeSender{err: errors.New("store unavailable")}
	srv, bundle := newTestServerWithSender(t, fake)

	form := url.Values{}
	form.Set("message", "hello")

	resp := sendRequest(t, srv.URL, bundle.authCfg.Sessions, bundle.csrfStore, "test-id", form)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 when sender errors, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "send-error") {
		t.Errorf("expected inline error HTML, got: %s", body)
	}
}

func TestSendHTMLFragmentResponse(t *testing.T) {
	fake := &fakeSender{}
	srv, bundle := newTestServerWithSender(t, fake)

	form := url.Values{}
	form.Set("message", "test message content")

	resp := sendRequest(t, srv.URL, bundle.authCfg.Sessions, bundle.csrfStore, "abc123", form)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content-type, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "message-card") {
		t.Errorf("response should contain message-card CSS class")
	}
	if !strings.Contains(bodyStr, "test message content") {
		t.Errorf("response should contain the message text")
	}
}

func TestSendNoTagIsAllowed(t *testing.T) {
	fake := &fakeSender{}
	srv, bundle := newTestServerWithSender(t, fake)

	form := url.Values{}
	form.Set("message", "no tag message")
	// No "tag" field — allowed.

	resp := sendRequest(t, srv.URL, bundle.authCfg.Sessions, bundle.csrfStore, "cf1", form)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if len(fake.sent) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(fake.sent))
	}
	if len(fake.sent[0].tags) != 0 {
		t.Errorf("expected no tags, got %v", fake.sent[0].tags)
	}
}

func TestSendInvalidTag(t *testing.T) {
	fake := &fakeSender{}
	srv, bundle := newTestServerWithSender(t, fake)

	form := url.Values{}
	form.Set("message", "hello")
	form.Set("tag", "not-a-valid-tag")

	resp := sendRequest(t, srv.URL, bundle.authCfg.Sessions, bundle.csrfStore, "cf1", form)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for invalid tag, got %d", resp.StatusCode)
	}
	if len(fake.sent) != 0 {
		t.Error("expected no send calls for invalid tag")
	}
}

func TestSendRequiresSession(t *testing.T) {
	srv, _ := newTestServer(t)

	form := url.Values{}
	form.Set("message", "hello")
	form.Set("_csrf", "some-token")

	resp, err := http.PostForm(srv.URL+"/c/test/send", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without session, got %d", resp.StatusCode)
	}
}

func TestCampfireDetailIncludesComposeBox(t *testing.T) {
	srv, authCfg := newTestServer(t)

	client := authenticatedClient(t, srv.URL, authCfg.Sessions)
	resp, err := client.Get(srv.URL + "/c/myfire123")
	if err != nil {
		t.Fatalf("GET /c/myfire123: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, `name="_csrf"`) {
		t.Error("compose form must include hidden _csrf field")
	}
	if !strings.Contains(bodyStr, `name="message"`) {
		t.Error("compose form must include message textarea")
	}
	if !strings.Contains(bodyStr, `name="tag"`) {
		t.Error("compose form must include tag selector")
	}
	if !strings.Contains(bodyStr, `/c/myfire123/send`) {
		t.Error("compose form must POST to /c/{id}/send")
	}
}
