package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// extractToolText pulls the text from the first content item in a tool result.
func extractToolText(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content from result: %v", string(b))
	}
	return result.Content[0].Text
}

// TestHandleTrust_SetAndResolve verifies that campfire_trust stores a label and
// returns it on subsequent lookups.
func TestHandleTrust_SetAndResolve(t *testing.T) {
	srv := newTestServer(t)
	key := "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"

	// Set a label.
	resp := srv.handleTrust(1, map[string]interface{}{
		"public_key": key,
		"label":      "alice",
	})
	text := extractToolText(t, resp)
	if !strings.Contains(text, "alice") {
		t.Errorf("expected label in response, got %q", text)
	}

	// Resolve it back.
	resp = srv.handleTrust(2, map[string]interface{}{
		"public_key": key,
	})
	text = extractToolText(t, resp)
	if !strings.Contains(text, "alice") {
		t.Errorf("expected label alice in resolve response, got %q", text)
	}
}

// TestHandleTrust_UnlabeledKey verifies that resolving an unknown key returns
// "(unlabeled)" rather than an error.
func TestHandleTrust_UnlabeledKey(t *testing.T) {
	srv := newTestServer(t)
	key := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	resp := srv.handleTrust(1, map[string]interface{}{
		"public_key": key,
	})
	text := extractToolText(t, resp)
	if !strings.Contains(text, "unlabeled") {
		t.Errorf("expected '(unlabeled)' for unknown key, got %q", text)
	}
}

// TestHandleTrust_MissingPublicKey verifies that omitting public_key returns a
// parameter error.
func TestHandleTrust_MissingPublicKey(t *testing.T) {
	srv := newTestServer(t)

	resp := srv.handleTrust(1, map[string]interface{}{})
	if resp.Error == nil {
		t.Fatal("expected error for missing public_key, got nil")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected code -32602, got %d", resp.Error.Code)
	}
}

// TestHandleTrust_NoInit verifies that calling campfire_trust without a cfHome
// returns a meaningful error.
func TestHandleTrust_NoInit(t *testing.T) {
	srv := &server{} // no cfHome set

	resp := srv.handleTrust(1, map[string]interface{}{
		"public_key": "aaaa",
	})
	if resp.Error == nil {
		t.Fatal("expected error when cfHome is empty")
	}
}

// TestHandleTrust_Overwrite verifies that setting a new label for an existing
// key replaces the old value.
func TestHandleTrust_Overwrite(t *testing.T) {
	srv := newTestServer(t)
	key := "1111111111111111111111111111111111111111111111111111111111111111"

	srv.handleTrust(1, map[string]interface{}{"public_key": key, "label": "first"})
	srv.handleTrust(2, map[string]interface{}{"public_key": key, "label": "second"})

	resp := srv.handleTrust(3, map[string]interface{}{"public_key": key})
	text := extractToolText(t, resp)
	if !strings.Contains(text, "second") {
		t.Errorf("expected updated label 'second', got %q", text)
	}
	if strings.Contains(text, "first") {
		t.Errorf("old label 'first' should be gone, got %q", text)
	}
}
