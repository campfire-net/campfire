// cmd/cf-ui/index_test.go — tests for the campfire list (index) handler.
//
// Test strategy: mock CampfireLister interface — not the real SQLiteStore.
// The store is tested separately in pkg/store/. Here we test the HTTP handler's
// rendering behavior: template output, SSE markup, empty state, unread badges.
package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubLister implements CampfireLister for tests.
type stubLister struct {
	entries []CampfireEntry
	err     error
}

func (s *stubLister) ListCampfires() ([]CampfireEntry, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.entries, nil
}

// buildIndexHandler creates a handleIndexWithStore handler for testing.
func buildIndexHandler(t *testing.T, lister CampfireLister) (http.Handler, *AuthConfig) {
	t.Helper()
	logger := newDiscardLogger()
	authCfg := newAuthConfig(logger, func(string) string { return "" }, "http://localhost", NewMemSessionStore(), noopAuthProvider{})
	handler := SessionMiddleware(authCfg.Sessions)(handleIndexWithStore(logger, lister))
	return handler, authCfg
}

// getIndexBody authenticates a request and returns the response body.
func getIndexBody(t *testing.T, handler http.Handler, authCfg *AuthConfig) (int, string) {
	t.Helper()
	sessionToken := "index-test-session-" + t.Name()
	authCfg.Sessions.Store(sessionToken, Identity{Email: "tester@example.com"}, time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestIndexEmptyState verifies the "no campfires yet" state when lister is nil.
func TestIndexEmptyState(t *testing.T) {
	handler, authCfg := buildIndexHandler(t, nil)
	status, body := getIndexBody(t, handler, authCfg)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if !strings.Contains(body, "Your Campfires") {
		t.Error("index page must include 'Your Campfires' heading")
	}
	if !strings.Contains(body, "No campfires") {
		t.Error("empty state must include 'No campfires' message")
	}
}

// TestIndexEmptyStateFromLister verifies empty state when lister returns no entries.
func TestIndexEmptyStateFromLister(t *testing.T) {
	lister := &stubLister{entries: nil}
	handler, authCfg := buildIndexHandler(t, lister)
	status, body := getIndexBody(t, handler, authCfg)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if !strings.Contains(body, "No campfires") {
		t.Error("empty state must include 'No campfires' message")
	}
}

// TestIndexListRendering verifies campfire entries are rendered with the correct markup.
func TestIndexListRendering(t *testing.T) {
	now := time.Now().UnixNano()
	lister := &stubLister{entries: []CampfireEntry{
		{
			ID:                "abc123def456",
			DisplayName:       "My Test Campfire",
			MemberCount:       3,
			LastActivityAt:    now,
			UnreadCount:       5,
			HasRecentActivity: true,
		},
		{
			ID:                "xyz789",
			DisplayName:       "Quiet Channel",
			MemberCount:       1,
			LastActivityAt:    0,
			UnreadCount:       0,
			HasRecentActivity: false,
		},
	}}

	handler, authCfg := buildIndexHandler(t, lister)
	status, body := getIndexBody(t, handler, authCfg)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	// Check campfire entries are present.
	if !strings.Contains(body, "My Test Campfire") {
		t.Error("body should contain first campfire display name")
	}
	if !strings.Contains(body, "Quiet Channel") {
		t.Error("body should contain second campfire display name")
	}

	// Check IDs are present as links.
	if !strings.Contains(body, "/c/abc123def456") {
		t.Error("body should contain link to first campfire")
	}
	if !strings.Contains(body, "/c/xyz789") {
		t.Error("body should contain link to second campfire")
	}
}

// TestIndexUnreadBadgeMarkup verifies unread count badges are rendered with the correct IDs.
func TestIndexUnreadBadgeMarkup(t *testing.T) {
	lister := &stubLister{entries: []CampfireEntry{
		{
			ID:          "campfire-aaa",
			DisplayName: "Active Fire",
			UnreadCount: 7,
		},
		{
			ID:          "campfire-bbb",
			DisplayName: "Read Fire",
			UnreadCount: 0,
		},
	}}

	handler, authCfg := buildIndexHandler(t, lister)
	_, body := getIndexBody(t, handler, authCfg)

	// Badge with count for campfire with unreads.
	if !strings.Contains(body, `id="unread-badge-campfire-aaa"`) {
		t.Error("body should contain unread badge for campfire-aaa")
	}

	// Badge element should also exist for zero-unread campfire (hidden via style).
	if !strings.Contains(body, `id="unread-badge-campfire-bbb"`) {
		t.Error("body should contain unread badge element for campfire-bbb (even when zero)")
	}
}

// TestIndexMemberCountBadge verifies the member count tag-chip is rendered.
func TestIndexMemberCountBadge(t *testing.T) {
	lister := &stubLister{entries: []CampfireEntry{
		{
			ID:          "fire-1",
			DisplayName: "Crowded",
			MemberCount: 42,
		},
	}}

	handler, authCfg := buildIndexHandler(t, lister)
	_, body := getIndexBody(t, handler, authCfg)

	if !strings.Contains(body, "42") {
		t.Error("body should contain member count")
	}
	if !strings.Contains(body, "tag-chip") {
		t.Error("body should use tag-chip class for member count")
	}
}

// TestIndexGlowActiveClass verifies that campfires with recent activity get the glow-active class.
func TestIndexGlowActiveClass(t *testing.T) {
	lister := &stubLister{entries: []CampfireEntry{
		{
			ID:                "fire-active",
			DisplayName:       "Active Fire",
			HasRecentActivity: true,
		},
		{
			ID:                "fire-idle",
			DisplayName:       "Idle Fire",
			HasRecentActivity: false,
		},
	}}

	handler, authCfg := buildIndexHandler(t, lister)
	_, body := getIndexBody(t, handler, authCfg)

	if !strings.Contains(body, "glow-active") {
		t.Error("body should include glow-active class for campfire with recent activity")
	}
}

// TestIndexSSEEventSourceMarkup verifies the SSE EventSource script is present.
func TestIndexSSEEventSourceMarkup(t *testing.T) {
	handler, authCfg := buildIndexHandler(t, nil)
	_, body := getIndexBody(t, handler, authCfg)

	if !strings.Contains(body, "EventSource") {
		t.Error("index page must include EventSource for SSE live updates")
	}
	if !strings.Contains(body, "'/events'") {
		t.Error("index page SSE must connect to /events")
	}
}

// TestIndexListerError verifies graceful degradation when lister returns an error.
func TestIndexListerError(t *testing.T) {
	lister := &stubLister{err: errors.New("store unavailable")}
	handler, authCfg := buildIndexHandler(t, lister)
	status, body := getIndexBody(t, handler, authCfg)

	// Should show empty state, not 500.
	if status != http.StatusOK {
		t.Fatalf("expected 200 (degraded empty state), got %d", status)
	}
	if !strings.Contains(body, "No campfires") {
		t.Error("error state should show empty campfire state")
	}
}

// TestIndexMessageCardClass verifies campfire entries use the message-card design class.
func TestIndexMessageCardClass(t *testing.T) {
	lister := &stubLister{entries: []CampfireEntry{
		{ID: "fire-1", DisplayName: "Test Fire"},
	}}

	handler, authCfg := buildIndexHandler(t, lister)
	_, body := getIndexBody(t, handler, authCfg)

	if !strings.Contains(body, "message-card") {
		t.Error("campfire entries must use message-card CSS class")
	}
}

// TestIndexEmberDotClass verifies campfire entries use the ember-dot design class.
func TestIndexEmberDotClass(t *testing.T) {
	lister := &stubLister{entries: []CampfireEntry{
		{ID: "fire-1", DisplayName: "Test Fire"},
	}}

	handler, authCfg := buildIndexHandler(t, lister)
	_, body := getIndexBody(t, handler, authCfg)

	if !strings.Contains(body, "ember-dot") {
		t.Error("campfire entries must use ember-dot CSS class for activity indicator")
	}
}

// TestIndexRequiresSession verifies that the index route requires authentication.
func TestIndexRequiresSession(t *testing.T) {
	handler, _ := buildIndexHandler(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No session cookie.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without session, got %d", w.Code)
	}
}

// TestIndexSidebarPresent verifies the sidebar campfire list is present.
func TestIndexSidebarPresent(t *testing.T) {
	lister := &stubLister{entries: []CampfireEntry{
		{ID: "fire-s1", DisplayName: "Sidebar Fire", MemberCount: 2},
	}}

	handler, authCfg := buildIndexHandler(t, lister)
	_, body := getIndexBody(t, handler, authCfg)

	if !strings.Contains(body, "sidebar") {
		t.Error("index page must include sidebar with campfire list")
	}
}
