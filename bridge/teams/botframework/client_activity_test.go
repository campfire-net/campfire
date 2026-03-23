package botframework

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// makeTestTokenClient returns a TokenClient whose token endpoint is served by srv.
func makeTestTokenClient(t *testing.T, srv *httptest.Server) *TokenClient {
	t.Helper()
	tc := newTokenClientWithClient("app-id", "app-secret", "tenant-id",
		srv.Client())
	tc.tokenURL = srv.URL + "/token"
	return tc
}

// tokenHandler returns a simple OAuth2 token response.
func tokenHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken: "test-token",
		ExpiresIn:   3600,
		TokenType:   "Bearer",
	})
}

// TestSendActivity verifies the correct endpoint and method are used.
func TestSendActivity(t *testing.T) {
	var gotPath, gotMethod, gotAuth string
	var gotBody Activity

	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenHandler)
	mux.HandleFunc("/v3/conversations/conv-1/activities", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ResourceResponse{ID: "new-act-id"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tc := makeTestTokenClient(t, srv)
	c := newClientWithHTTP(tc, srv.Client())

	act := &Activity{Type: ActivityTypeMessage, Text: "hi"}
	resp, err := c.SendActivity(context.Background(), srv.URL+"/", "conv-1", act)
	if err != nil {
		t.Fatalf("SendActivity: %v", err)
	}
	if resp.ID != "new-act-id" {
		t.Errorf("ID = %q, want new-act-id", resp.ID)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v3/conversations/conv-1/activities" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("auth = %q, want 'Bearer test-token'", gotAuth)
	}
	if gotBody.Text != "hi" {
		t.Errorf("body Text = %q, want hi", gotBody.Text)
	}
}

// TestReplyToActivity verifies the reply-in-thread endpoint includes replyToId in the path.
func TestReplyToActivity(t *testing.T) {
	var gotPath string

	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenHandler)
	mux.HandleFunc("/v3/conversations/conv-1/activities/parent-id", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ResourceResponse{ID: "reply-id"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tc := makeTestTokenClient(t, srv)
	c := newClientWithHTTP(tc, srv.Client())

	act := &Activity{Type: ActivityTypeMessage, Text: "reply"}
	resp, err := c.ReplyToActivity(context.Background(), srv.URL+"/", "conv-1", "parent-id", act)
	if err != nil {
		t.Fatalf("ReplyToActivity: %v", err)
	}
	if resp.ID != "reply-id" {
		t.Errorf("ID = %q, want reply-id", resp.ID)
	}
	if gotPath != "/v3/conversations/conv-1/activities/parent-id" {
		t.Errorf("path = %q", gotPath)
	}
}

// TestUpdateActivity verifies UpdateActivity uses PUT.
func TestUpdateActivity(t *testing.T) {
	var gotMethod string

	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenHandler)
	mux.HandleFunc("/v3/conversations/conv-1/activities/act-id", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ResourceResponse{ID: "act-id"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tc := makeTestTokenClient(t, srv)
	c := newClientWithHTTP(tc, srv.Client())

	act := &Activity{Type: ActivityTypeMessage, Text: "updated"}
	_, err := c.UpdateActivity(context.Background(), srv.URL+"/", "conv-1", "act-id", act)
	if err != nil {
		t.Fatalf("UpdateActivity: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
}

// TestCreateConversation verifies the conversations endpoint and response.
func TestCreateConversation(t *testing.T) {
	var gotPath string

	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenHandler)
	mux.HandleFunc("/v3/conversations", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ConversationResourceResponse{
			ID:         "19:new@thread.tacv2",
			ActivityID: "first-act-id",
			ServiceURL: r.Header.Get("Host"),
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tc := makeTestTokenClient(t, srv)
	c := newClientWithHTTP(tc, srv.Client())

	params := &ConversationParameters{
		Bot:     ChannelAccount{ID: "28:bot-id"},
		Members: []ChannelAccount{{ID: "29:user-id"}},
	}
	resp, err := c.CreateConversation(context.Background(), srv.URL+"/", params)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if resp.ID != "19:new@thread.tacv2" {
		t.Errorf("ID = %q", resp.ID)
	}
	if gotPath != "/v3/conversations" {
		t.Errorf("path = %q", gotPath)
	}
}

// TestSendActivity_401Retry verifies that a 401 response triggers a token refresh and retry.
func TestSendActivity_401Retry(t *testing.T) {
	callCount := 0
	actCallCount := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		tok := fmt.Sprintf("token-%d", callCount)
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: tok,
			ExpiresIn:   3600,
			TokenType:   "Bearer",
		})
	})
	mux.HandleFunc("/v3/conversations/conv-1/activities", func(w http.ResponseWriter, r *http.Request) {
		actCallCount++
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "token-1") {
			// First call: return 401
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Second call with refreshed token: succeed
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ResourceResponse{ID: "ok"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tc := makeTestTokenClient(t, srv)
	c := newClientWithHTTP(tc, srv.Client())

	act := &Activity{Type: ActivityTypeMessage, Text: "hi"}
	resp, err := c.SendActivity(context.Background(), srv.URL+"/", "conv-1", act)
	if err != nil {
		t.Fatalf("SendActivity: %v", err)
	}
	if resp.ID != "ok" {
		t.Errorf("ID = %q, want ok", resp.ID)
	}
	if actCallCount != 2 {
		t.Errorf("activity endpoint called %d times, want 2", actCallCount)
	}
	if callCount < 2 {
		t.Errorf("token endpoint called %d times, want >= 2", callCount)
	}
}

// TestSendActivity_429Retry verifies that a 429 response triggers a wait and retry.
func TestSendActivity_429Retry(t *testing.T) {
	actCallCount := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenHandler)
	mux.HandleFunc("/v3/conversations/conv-1/activities", func(w http.ResponseWriter, r *http.Request) {
		actCallCount++
		if actCallCount == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			// Encode a very short retry-after so the test doesn't hang.
			_ = json.NewEncoder(w).Encode(map[string]any{"retryAfter": 0.001})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ResourceResponse{ID: "after-429"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tc := makeTestTokenClient(t, srv)
	c := newClientWithHTTP(tc, srv.Client())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	act := &Activity{Type: ActivityTypeMessage, Text: "hi"}
	resp, err := c.SendActivity(ctx, srv.URL+"/", "conv-1", act)
	if err != nil {
		t.Fatalf("SendActivity: %v", err)
	}
	if resp.ID != "after-429" {
		t.Errorf("ID = %q, want after-429", resp.ID)
	}
	if actCallCount != 2 {
		t.Errorf("activity endpoint called %d times, want 2", actCallCount)
	}
}

// TestSendActivity_ErrorStatus verifies non-2xx (other than 401/429) returns an error.
func TestSendActivity_ErrorStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenHandler)
	mux.HandleFunc("/v3/conversations/conv-1/activities", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tc := makeTestTokenClient(t, srv)
	c := newClientWithHTTP(tc, srv.Client())

	act := &Activity{Type: ActivityTypeMessage, Text: "hi"}
	_, err := c.SendActivity(context.Background(), srv.URL+"/", "conv-1", act)
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want it to mention 500", err)
	}
}

// TestEnsureTrailingSlash verifies URL normalisation.
func TestEnsureTrailingSlash(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://example.com", "https://example.com/"},
		{"https://example.com/", "https://example.com/"},
		{"https://example.com/base/", "https://example.com/base/"},
	}
	for _, tc := range cases {
		got := ensureTrailingSlash(tc.in)
		if got != tc.want {
			t.Errorf("ensureTrailingSlash(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestParseRetryAfter verifies Retry-After extraction from JSON bodies.
func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		body string
		want time.Duration
	}{
		{`{"retryAfter": 5}`, 5 * time.Second},
		{`{"retryAfter": "3"}`, 3 * time.Second},
		{`{}`, time.Second},           // default
		{`invalid json`, time.Second}, // fallback
	}
	for _, tc := range cases {
		got := parseRetryAfter([]byte(tc.body))
		if got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
