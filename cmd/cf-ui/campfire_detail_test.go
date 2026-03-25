// cmd/cf-ui/campfire_detail_test.go — tests for GET /c/{id} and GET /c/{id}/messages
package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// fakeMessageStore is a test double for MessageStore.
type fakeMessageStore struct {
	records []store.MessageRecord
	err     error
}

func (f *fakeMessageStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	// Apply campfireID filter.
	var out []store.MessageRecord
	for _, r := range f.records {
		if r.CampfireID != campfireID {
			continue
		}
		// Apply tag filter (OR logic) if specified.
		if len(filter) > 0 && len(filter[0].Tags) > 0 {
			if !recordHasAnyTag(r, filter[0].Tags) {
				continue
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func recordHasAnyTag(r store.MessageRecord, tags []string) bool {
	for _, want := range tags {
		for _, have := range r.Tags {
			if strings.EqualFold(have, want) {
				return true
			}
		}
	}
	return false
}

// makeRecord creates a minimal store.MessageRecord for testing.
func makeRecord(id, campfireID, sender string, tags []string, payload string) store.MessageRecord {
	return store.MessageRecord{
		ID:         id,
		CampfireID: campfireID,
		Sender:     sender,
		Tags:       tags,
		Payload:    []byte(payload),
		Timestamp:  time.Now().UnixNano(),
		ReceivedAt: time.Now().UnixNano(),
	}
}

// newDetailTestServer builds a test server with an injected MessageStore and returns
// the server + session store so tests can make authenticated requests.
func newDetailTestServer(t *testing.T, ms MessageStore) (*httptest.Server, SessionStore) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := NewMemSessionStore()
	authCfg := newAuthConfig(logger, func(string) string { return "" }, "http://localhost", sessions, noopAuthProvider{})
	_ = authCfg

	csrf, err := newCSRFStore()
	if err != nil {
		t.Fatalf("newCSRFStore: %v", err)
	}

	hub := NewSSEHub(sessions, logger)
	detail := NewCampfireDetailHandlers(logger, ms).WithCSRF(csrf)

	sessionMW := SessionMiddleware(sessions)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("GET /c/{id}", sessionMW(http.HandlerFunc(detail.HandleDetail)))
	mux.Handle("GET /c/{id}/messages", sessionMW(http.HandlerFunc(detail.HandleMessages)))

	// SSE stream — required to avoid "unknown route" for browsers that open /events.
	mux.Handle("GET /events", sessionMW(handleEventsHandler(hub)))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, sessions
}

// authenticatedDetailRequest makes an authenticated GET request to the given URL.
func authenticatedDetailRequest(t *testing.T, srvURL, path string, sessions SessionStore) *http.Response {
	t.Helper()
	tok := "detail-test-" + t.Name()
	sessions.Store(tok, Identity{Email: "test@example.com", DisplayName: "Test User"}, time.Hour)

	req, err := http.NewRequest(http.MethodGet, srvURL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tok})

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// TestDetailPageRenders verifies GET /c/{id} returns 200 with message-feed markup.
func TestDetailPageRenders(t *testing.T) {
	srv, sessions := newDetailTestServer(t, nil) // nil store = empty feed

	resp := authenticatedDetailRequest(t, srv.URL, "/c/abc123", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, `id="message-feed"`) {
		t.Error("detail page must include #message-feed element")
	}
	if !strings.Contains(bodyStr, `data-campfire-id="abc123"`) {
		t.Error("detail page must include campfire ID as data attribute")
	}
	if !strings.Contains(bodyStr, `detail-shell`) {
		t.Error("detail page must use two-panel detail-shell layout")
	}
	if !strings.Contains(bodyStr, `context-panel`) {
		t.Error("detail page must include context-panel placeholder")
	}
}

// TestDetailPageEmptyCampfire verifies the empty-feed state renders correctly.
func TestDetailPageEmptyCampfire(t *testing.T) {
	ms := &fakeMessageStore{} // no records
	srv, sessions := newDetailTestServer(t, ms)

	resp := authenticatedDetailRequest(t, srv.URL, "/c/empty-cf", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "No messages") {
		t.Errorf("empty campfire should show 'No messages' state")
	}
}

// TestDetailPageWithMessages verifies messages are rendered in the feed.
func TestDetailPageWithMessages(t *testing.T) {
	ms := &fakeMessageStore{
		records: []store.MessageRecord{
			makeRecord("msg1", "cf-abc", "aabbccdd1234", []string{"status"}, "Hello world"),
			makeRecord("msg2", "cf-abc", "aabbccdd1234", []string{"finding"}, "Important finding"),
		},
	}
	srv, sessions := newDetailTestServer(t, ms)

	resp := authenticatedDetailRequest(t, srv.URL, "/c/cf-abc", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Hello world") {
		t.Error("detail page should render message body text")
	}
	if !strings.Contains(bodyStr, `data-tag="status"`) {
		t.Error("detail page should render tag chips")
	}
	if !strings.Contains(bodyStr, `class="message-card"`) {
		t.Error("detail page should use .message-card CSS class")
	}
}

// TestMessageFragmentEndpoint verifies GET /c/{id}/messages returns HTML fragment.
func TestMessageFragmentEndpoint(t *testing.T) {
	ms := &fakeMessageStore{
		records: []store.MessageRecord{
			makeRecord("m1", "cf-xyz", "sender01", []string{"blocker"}, "Blocked!"),
		},
	}
	srv, sessions := newDetailTestServer(t, ms)

	resp := authenticatedDetailRequest(t, srv.URL, "/c/cf-xyz/messages", sessions)
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
		t.Error("messages fragment should contain .message-card elements")
	}
	if !strings.Contains(bodyStr, "Blocked!") {
		t.Error("messages fragment should contain message body text")
	}
}

// TestTagFilteringFiltersMessages verifies tag filtering via ?tags= query parameter.
func TestTagFilteringFiltersMessages(t *testing.T) {
	ms := &fakeMessageStore{
		records: []store.MessageRecord{
			makeRecord("a", "cf-flt", "sender01", []string{"status"}, "Status message"),
			makeRecord("b", "cf-flt", "sender01", []string{"finding"}, "Finding message"),
			makeRecord("c", "cf-flt", "sender01", []string{"blocker"}, "Blocker message"),
		},
	}
	srv, sessions := newDetailTestServer(t, ms)

	// Filter for "status" only.
	resp := authenticatedDetailRequest(t, srv.URL, "/c/cf-flt/messages?tags=status", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Status message") {
		t.Error("filtered result should include status-tagged message")
	}
	if strings.Contains(bodyStr, "Finding message") {
		t.Error("filtered result should NOT include finding-tagged message")
	}
	if strings.Contains(bodyStr, "Blocker message") {
		t.Error("filtered result should NOT include blocker-tagged message")
	}
}

// TestTagFilteringORLogic verifies multiple tags are OR'd together.
func TestTagFilteringORLogic(t *testing.T) {
	ms := &fakeMessageStore{
		records: []store.MessageRecord{
			makeRecord("a", "cf-or", "sender01", []string{"status"}, "Status message"),
			makeRecord("b", "cf-or", "sender01", []string{"finding"}, "Finding message"),
			makeRecord("c", "cf-or", "sender01", []string{"blocker"}, "Blocker message"),
		},
	}
	srv, sessions := newDetailTestServer(t, ms)

	// Filter for "status" OR "finding".
	resp := authenticatedDetailRequest(t, srv.URL, "/c/cf-or/messages?tags=status,finding", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Status message") {
		t.Error("OR filter should include status-tagged message")
	}
	if !strings.Contains(bodyStr, "Finding message") {
		t.Error("OR filter should include finding-tagged message")
	}
	if strings.Contains(bodyStr, "Blocker message") {
		t.Error("OR filter should NOT include blocker-tagged message")
	}
}

// TestMessageFragmentEmptyWithFilter verifies empty state when filter matches nothing.
func TestMessageFragmentEmptyWithFilter(t *testing.T) {
	ms := &fakeMessageStore{
		records: []store.MessageRecord{
			makeRecord("x", "cf-empty-flt", "s1", []string{"status"}, "Status only"),
		},
	}
	srv, sessions := newDetailTestServer(t, ms)

	// Filter for "finding" — no messages have this tag.
	resp := authenticatedDetailRequest(t, srv.URL, "/c/cf-empty-flt/messages?tags=finding", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if strings.Contains(bodyStr, "Status only") {
		t.Error("filtered result should NOT include unmatched messages")
	}
}

// TestDetailPageRequiresSession verifies unauthenticated requests get 401.
func TestDetailPageRequiresSession(t *testing.T) {
	srv, _ := newDetailTestServer(t, nil)

	resp, err := http.Get(srv.URL + "/c/abc123")
	if err != nil {
		t.Fatalf("GET /c/abc123: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without session, got %d", resp.StatusCode)
	}
}

// TestMessageFragmentRequiresSession verifies unauthenticated requests get 401.
func TestMessageFragmentRequiresSession(t *testing.T) {
	srv, _ := newDetailTestServer(t, nil)

	resp, err := http.Get(srv.URL + "/c/abc123/messages")
	if err != nil {
		t.Fatalf("GET /c/abc123/messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without session, got %d", resp.StatusCode)
	}
}

// TestDetailPageContainsSSEScript verifies the SSE reconnect script is present.
func TestDetailPageContainsSSEScript(t *testing.T) {
	srv, sessions := newDetailTestServer(t, nil)

	resp := authenticatedDetailRequest(t, srv.URL, "/c/ssecf", sessions)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "EventSource") {
		t.Error("detail page must include SSE connection script")
	}
}

// TestDetailPageTagFilterChips verifies tag filter chips render when tags exist.
func TestDetailPageTagFilterChips(t *testing.T) {
	ms := &fakeMessageStore{
		records: []store.MessageRecord{
			makeRecord("t1", "cf-chips", "s1", []string{"status", "finding"}, "Tagged msg"),
		},
	}
	srv, sessions := newDetailTestServer(t, ms)

	resp := authenticatedDetailRequest(t, srv.URL, "/c/cf-chips", sessions)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "tag-filter-chip") {
		t.Error("detail page should render tag filter chips when messages have tags")
	}
	if !strings.Contains(bodyStr, `/c/cf-chips/messages?tags=`) {
		t.Error("tag filter chips should link to /c/{id}/messages?tags=...")
	}
}

// TestMessageViewModelSenderPrefix verifies sender hex is truncated to 12 chars.
func TestMessageViewModelSenderPrefix(t *testing.T) {
	r := makeRecord("id1", "cf1", "aabbccdd11223344", nil, "body")
	vm := toViewModel(r)
	if len(vm.Sender) > 12 {
		t.Errorf("sender should be at most 12 chars, got %d: %q", len(vm.Sender), vm.Sender)
	}
	if vm.Sender != "aabbccdd1122" {
		t.Errorf("sender prefix mismatch: got %q, want %q", vm.Sender, "aabbccdd1122")
	}
}

// TestMessageViewModelThreaded verifies thread detection from antecedents.
func TestMessageViewModelThreaded(t *testing.T) {
	r := makeRecord("id1", "cf1", "aabb", nil, "reply")
	r.Antecedents = []string{"prior-msg-id"}
	vm := toViewModel(r)
	if !vm.Threaded {
		t.Error("message with antecedents should be marked as threaded")
	}
	if vm.ThreadCount != 1 {
		t.Errorf("thread count: got %d, want 1", vm.ThreadCount)
	}
}

// TestMessageViewModelTagsSorted verifies tags are alphabetically sorted.
func TestMessageViewModelTagsSorted(t *testing.T) {
	r := makeRecord("id1", "cf1", "aabb", []string{"finding", "blocker", "status"}, "body")
	vm := toViewModel(r)
	want := []string{"blocker", "finding", "status"}
	if fmt.Sprint(vm.Tags) != fmt.Sprint(want) {
		t.Errorf("tags not sorted: got %v, want %v", vm.Tags, want)
	}
}

// TestParseTagsEmpty verifies parseTags handles empty input.
func TestParseTagsEmpty(t *testing.T) {
	if got := parseTags(""); got != nil {
		t.Errorf("parseTags(\"\") = %v, want nil", got)
	}
}

// TestParseTagsMultiple verifies parseTags splits comma-separated tags.
func TestParseTagsMultiple(t *testing.T) {
	got := parseTags("status,finding,blocker")
	want := []string{"status", "finding", "blocker"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("parseTags: got %v, want %v", got, want)
	}
}

// TestToggleTagAdds verifies toggleTag adds a new tag.
func TestToggleTagAdds(t *testing.T) {
	got := toggleTag("status", []string{"finding"})
	if !strings.Contains(got, "status") {
		t.Errorf("toggleTag should add tag: got %q", got)
	}
	if !strings.Contains(got, "finding") {
		t.Errorf("toggleTag should keep existing tag: got %q", got)
	}
}

// TestToggleTagRemoves verifies toggleTag removes an active tag.
func TestToggleTagRemoves(t *testing.T) {
	got := toggleTag("status", []string{"finding", "status"})
	if strings.Contains(got, "status") {
		t.Errorf("toggleTag should remove active tag: got %q", got)
	}
	if !strings.Contains(got, "finding") {
		t.Errorf("toggleTag should keep other tags: got %q", got)
	}
}

// --- BOLA/IDOR access control tests ---

// fakeMembershipChecker is a MembershipChecker that allows only pre-registered pairs.
type fakeMembershipChecker struct {
	// members maps "campfireID:email" → bool
	members map[string]bool
}

func newFakeMembership(pairs ...string) *fakeMembershipChecker {
	m := &fakeMembershipChecker{members: make(map[string]bool)}
	for _, p := range pairs {
		m.members[p] = true
	}
	return m
}

func (f *fakeMembershipChecker) IsMember(campfireID, email string) bool {
	return f.members[campfireID+":"+email]
}

// newDetailTestServerWithMembership builds a test server with an injected
// MembershipChecker to test BOLA/IDOR enforcement.
func newDetailTestServerWithMembership(t *testing.T, ms MessageStore, membership MembershipChecker) (*httptest.Server, SessionStore) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := NewMemSessionStore()
	authCfg := newAuthConfig(logger, func(string) string { return "" }, "http://localhost", sessions, noopAuthProvider{})
	_ = authCfg

	csrf, err := newCSRFStore()
	if err != nil {
		t.Fatalf("newCSRFStore: %v", err)
	}

	hub := NewSSEHub(sessions, logger)
	detail := NewCampfireDetailHandlers(logger, ms).WithCSRF(csrf).WithMembership(membership)

	sessionMW := SessionMiddleware(sessions)

	mux := http.NewServeMux()
	mux.Handle("GET /c/{id}", sessionMW(http.HandlerFunc(detail.HandleDetail)))
	mux.Handle("GET /c/{id}/messages", sessionMW(http.HandlerFunc(detail.HandleMessages)))
	mux.Handle("GET /events", sessionMW(handleEventsHandler(hub)))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, sessions
}

// authenticatedRequestAs makes an authenticated GET request with the given email identity.
func authenticatedRequestAs(t *testing.T, srvURL, path, email string, sessions SessionStore) *http.Response {
	t.Helper()
	tok := "bola-test-" + t.Name() + email
	sessions.Store(tok, Identity{Email: email, DisplayName: email}, time.Hour)

	req, err := http.NewRequest(http.MethodGet, srvURL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tok})

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// TestDetailBOLANonMemberGets403 verifies that a non-member operator receives 403
// when attempting to access a campfire detail page (BOLA/IDOR protection).
func TestDetailBOLANonMemberGets403(t *testing.T) {
	// only alice@example.com is a member of campfire-secret
	membership := newFakeMembership("campfire-secret:alice@example.com")
	srv, sessions := newDetailTestServerWithMembership(t, nil, membership)

	// bob is NOT a member
	resp := authenticatedRequestAs(t, srv.URL, "/c/campfire-secret", "bob@example.com", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-member on /c/{id}, got %d", resp.StatusCode)
	}
}

// TestDetailBOLAMemberGets200 verifies that a member operator can access the campfire detail page.
func TestDetailBOLAMemberGets200(t *testing.T) {
	membership := newFakeMembership("campfire-open:alice@example.com")
	srv, sessions := newDetailTestServerWithMembership(t, nil, membership)

	resp := authenticatedRequestAs(t, srv.URL, "/c/campfire-open", "alice@example.com", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for member on /c/{id}, got %d", resp.StatusCode)
	}
}

// TestMessagesBOLANonMemberGets403 verifies that a non-member operator receives 403
// when attempting to access the messages fragment (BOLA/IDOR protection).
func TestMessagesBOLANonMemberGets403(t *testing.T) {
	membership := newFakeMembership("campfire-private:alice@example.com")
	srv, sessions := newDetailTestServerWithMembership(t, nil, membership)

	// charlie is NOT a member
	resp := authenticatedRequestAs(t, srv.URL, "/c/campfire-private/messages", "charlie@example.com", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-member on /c/{id}/messages, got %d", resp.StatusCode)
	}
}

// TestMessagesBOLAMemberGets200 verifies that a member operator can fetch messages.
func TestMessagesBOLAMemberGets200(t *testing.T) {
	membership := newFakeMembership("campfire-shared:alice@example.com")
	srv, sessions := newDetailTestServerWithMembership(t, nil, membership)

	resp := authenticatedRequestAs(t, srv.URL, "/c/campfire-shared/messages", "alice@example.com", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for member on /c/{id}/messages, got %d", resp.StatusCode)
	}
}

// TestDetailNilMembershipCheckerAllowsAll verifies that when no membership checker
// is configured (nil), all authenticated operators can access campfires (open/dev mode).
func TestDetailNilMembershipCheckerAllowsAll(t *testing.T) {
	// No membership checker — open mode.
	srv, sessions := newDetailTestServerWithMembership(t, nil, nil)

	resp := authenticatedRequestAs(t, srv.URL, "/c/any-campfire", "anyone@example.com", sessions)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with nil membership checker, got %d", resp.StatusCode)
	}
}
