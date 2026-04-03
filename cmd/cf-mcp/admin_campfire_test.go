// admin_campfire_test.go — Tests for the admin campfire convention.
//
// Tests cover:
//   - TestAdminCreateKey_AllowedSender: valid pubkey → CreateKey called → key returned
//   - TestAdminCreateKey_UnknownSender: unknown pubkey → error returned, no Forge call
//   - TestAdminCreateKey_EmptyAllowlist: empty allowlist → all senders rejected
//   - TestAdminCreateKey_ForgeError: Forge returns error → convention error returned
package main

import (
	"context"
	"errors"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/forge"
)

// ---------------------------------------------------------------------------
// Fake forge client for testing
// ---------------------------------------------------------------------------

// fakeForgeKeyCreator is a test double for forgeKeyCreator.
type fakeForgeKeyCreator struct {
	// callCount tracks how many times CreateKey was called.
	callCount int
	// returnKey is returned on success.
	returnKey forge.Key
	// returnErr, if non-nil, is returned as the error.
	returnErr error
}

func (f *fakeForgeKeyCreator) CreateKey(_ context.Context, _, _ string) (forge.Key, error) {
	f.callCount++
	if f.returnErr != nil {
		return forge.Key{}, f.returnErr
	}
	return f.returnKey, nil
}

// ---------------------------------------------------------------------------
// Helper: build a convention.Request for testing
// ---------------------------------------------------------------------------

func makeAdminRequest(sender, accountID string) *convention.Request {
	args := map[string]any{
		"account_id": accountID,
	}
	return &convention.Request{
		MessageID:  "msg-test-001",
		CampfireID: "campfire-admin-test",
		Sender:     sender,
		Args:       args,
		Tags:       []string{"admin:create-key"},
	}
}

// extractPayloadMap asserts that resp.Payload is map[string]string and returns it.
func extractPayloadMap(t *testing.T, resp *convention.Response) map[string]string {
	t.Helper()
	if resp.Payload == nil {
		t.Fatal("response payload is nil")
	}
	m, ok := resp.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected payload type map[string]string, got %T", resp.Payload)
	}
	return m
}

// ---------------------------------------------------------------------------
// TestAdminCreateKey_AllowedSender
// ---------------------------------------------------------------------------

// TestAdminCreateKey_AllowedSender verifies that a sender whose pubkey is in
// the allowlist triggers a Forge CreateKey call and the key plaintext is
// returned in the convention response.
func TestAdminCreateKey_AllowedSender(t *testing.T) {
	const allowedKey = "aaaa1234abcd5678aaaa1234abcd5678aaaa1234abcd5678aaaa1234abcd5678"
	const accountID = "acct-operator-001"
	const wantKeyPlaintext = "forge-tk-abc123"

	fc := &fakeForgeKeyCreator{
		returnKey: forge.Key{
			KeyPlaintext: wantKeyPlaintext,
			AccountID:    accountID,
			Role:         "tenant",
		},
	}

	handler := buildAdminHandler([]string{allowedKey}, fc)
	req := makeAdminRequest(allowedKey, accountID)

	resp, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// CreateKey must have been called exactly once.
	if fc.callCount != 1 {
		t.Errorf("expected CreateKey called 1 time, got %d", fc.callCount)
	}

	body := extractPayloadMap(t, resp)

	// Response payload must contain the key plaintext.
	if body["key"] != wantKeyPlaintext {
		t.Errorf("expected key=%q, got %q", wantKeyPlaintext, body["key"])
	}
	// Error field must not be present.
	if errMsg, ok := body["error"]; ok {
		t.Errorf("unexpected error field in response: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// TestAdminCreateKey_UnknownSender
// ---------------------------------------------------------------------------

// TestAdminCreateKey_UnknownSender verifies that a sender NOT in the allowlist
// receives an error response and Forge is never called.
func TestAdminCreateKey_UnknownSender(t *testing.T) {
	const allowedKey = "aaaa1234abcd5678aaaa1234abcd5678aaaa1234abcd5678aaaa1234abcd5678"
	const unknownKey = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	fc := &fakeForgeKeyCreator{
		returnKey: forge.Key{KeyPlaintext: "should-not-appear"},
	}

	handler := buildAdminHandler([]string{allowedKey}, fc)
	req := makeAdminRequest(unknownKey, "acct-any")

	resp, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Forge must NOT have been called.
	if fc.callCount != 0 {
		t.Errorf("expected CreateKey not called, got %d call(s)", fc.callCount)
	}

	body := extractPayloadMap(t, resp)
	if body["error"] == "" {
		t.Errorf("expected error field in response, got none")
	}
	// Key must not appear in an error response.
	if _, ok := body["key"]; ok {
		t.Errorf("key field must not appear in error response")
	}
}

// ---------------------------------------------------------------------------
// TestAdminCreateKey_EmptyAllowlist
// ---------------------------------------------------------------------------

// TestAdminCreateKey_EmptyAllowlist verifies that with an empty allowlist,
// every sender is rejected and Forge is never called.
func TestAdminCreateKey_EmptyAllowlist(t *testing.T) {
	const anySender = "aaaa1234abcd5678aaaa1234abcd5678aaaa1234abcd5678aaaa1234abcd5678"

	fc := &fakeForgeKeyCreator{
		returnKey: forge.Key{KeyPlaintext: "should-not-appear"},
	}

	handler := buildAdminHandler([]string{}, fc)
	req := makeAdminRequest(anySender, "acct-any")

	resp, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Forge must NOT have been called.
	if fc.callCount != 0 {
		t.Errorf("expected CreateKey not called, got %d call(s)", fc.callCount)
	}

	body := extractPayloadMap(t, resp)
	if body["error"] == "" {
		t.Errorf("expected error field in response for empty allowlist")
	}
}

// ---------------------------------------------------------------------------
// TestAdminCreateKey_ForgeError
// ---------------------------------------------------------------------------

// TestAdminCreateKey_ForgeError verifies that when Forge returns an error, the
// handler surfaces it as a convention error response (not a Go error) so the
// caller receives a structured reply rather than a silent failure.
func TestAdminCreateKey_ForgeError(t *testing.T) {
	const allowedKey = "aaaa1234abcd5678aaaa1234abcd5678aaaa1234abcd5678aaaa1234abcd5678"

	fc := &fakeForgeKeyCreator{
		returnErr: errors.New("forge server returned 500"),
	}

	handler := buildAdminHandler([]string{allowedKey}, fc)
	req := makeAdminRequest(allowedKey, "acct-will-fail")

	resp, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned unexpected Go error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// CreateKey was attempted.
	if fc.callCount != 1 {
		t.Errorf("expected CreateKey called 1 time, got %d", fc.callCount)
	}

	// Response must carry the forge error, not the key.
	body := extractPayloadMap(t, resp)
	if body["error"] == "" {
		t.Errorf("expected error field in response when forge fails")
	}
	if _, ok := body["key"]; ok {
		t.Errorf("key must not appear in error response")
	}
}

// ---------------------------------------------------------------------------
// TestLoadAdminCampfireConfig
// ---------------------------------------------------------------------------

// TestLoadAdminCampfireConfig verifies config loading from environment variables.
func TestLoadAdminCampfireConfig(t *testing.T) {
	t.Run("disabled when CF_ADMIN_CAMPFIRE unset", func(t *testing.T) {
		t.Setenv("CF_ADMIN_CAMPFIRE", "")
		cfg := loadAdminCampfireConfig()
		if cfg != nil {
			t.Errorf("expected nil config when CF_ADMIN_CAMPFIRE is empty")
		}
	})

	t.Run("loads campfire ID and parses allowlist", func(t *testing.T) {
		const campfireID = "abc123def456"
		const key1 = "aaaa1234abcd5678"
		const key2 = "bbbb5678abcd1234"
		t.Setenv("CF_ADMIN_CAMPFIRE", campfireID)
		t.Setenv("CF_ADMIN_ALLOWLIST", key1+","+key2)

		cfg := loadAdminCampfireConfig()
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.campfireID != campfireID {
			t.Errorf("campfireID: want %q, got %q", campfireID, cfg.campfireID)
		}
		if len(cfg.allowlist) != 2 {
			t.Fatalf("allowlist: want 2 entries, got %d", len(cfg.allowlist))
		}
		if cfg.allowlist[0] != key1 {
			t.Errorf("allowlist[0]: want %q, got %q", key1, cfg.allowlist[0])
		}
		if cfg.allowlist[1] != key2 {
			t.Errorf("allowlist[1]: want %q, got %q", key2, cfg.allowlist[1])
		}
	})

	t.Run("empty allowlist string yields empty slice", func(t *testing.T) {
		t.Setenv("CF_ADMIN_CAMPFIRE", "someid")
		t.Setenv("CF_ADMIN_ALLOWLIST", "")
		cfg := loadAdminCampfireConfig()
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if len(cfg.allowlist) != 0 {
			t.Errorf("expected empty allowlist, got %v", cfg.allowlist)
		}
	})
}
