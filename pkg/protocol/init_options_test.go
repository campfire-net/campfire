package protocol_test

// Tests for protocol.Init() variadic options — campfire-agent-m6n.
//
// All four call forms are verified:
//   1. Init(cfHome)                         — zero options, backward-compatible
//   2. Init(cfHome, WithAuthorizeFunc(fn))  — fn called on authorization demand
//   3. Init(cfHome, WithRemote(url))        — remote URL stored
//   4. Init(cfHome, WithWalkUp(false))      — walk-up disabled

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/protocol"
)

// TestInitOptions runs all Init-options sub-tests.
func TestInitOptions(t *testing.T) {
	t.Run("ZeroOptions", testInitZeroOptions)
	t.Run("WithAuthorizeFunc", testInitWithAuthorizeFunc)
	t.Run("WithRemote", testInitWithRemote)
	t.Run("WithWalkUpFalse", testInitWithWalkUpFalse)
}

// testInitZeroOptions verifies Init(cfHome) with no options returns a non-nil
// Client and nil error — identical behavior to the pre-options implementation.
func testInitZeroOptions(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()

	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init(zero options): %v", err)
	}
	if client == nil {
		t.Fatal("Init(zero options) returned nil client")
	}
	t.Cleanup(func() { client.Close() })

	// Identity and store must be created (same code path as before options).
	if client.ClientIdentity() == nil {
		t.Error("ClientIdentity() is nil after zero-options Init")
	}
	if client.ClientStore() == nil {
		t.Error("ClientStore() is nil after zero-options Init")
	}

	// RemoteURL must be empty (no WithRemote given).
	if url := client.RemoteURL(); url != "" {
		t.Errorf("RemoteURL() = %q, want empty for zero-options Init", url)
	}

	// Walk-up must be enabled by default.
	if !client.WalkUpEnabled() {
		t.Error("WalkUpEnabled() = false, want true for zero-options Init")
	}
}

// testInitWithAuthorizeFunc verifies that the registered fn is called (with a
// non-empty description) when the client triggers an authorization demand via
// client.Authorize(). The bool return is respected.
func testInitWithAuthorizeFunc(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()

	var capturedDesc string
	called := false
	approveNext := true

	fn := func(description string) (bool, error) {
		called = true
		capturedDesc = description
		return approveNext, nil
	}

	client, err := protocol.Init(configDir, protocol.WithAuthorizeFunc(fn))
	if err != nil {
		t.Fatalf("Init(WithAuthorizeFunc): %v", err)
	}
	t.Cleanup(func() { client.Close() })

	// Trigger authorization through the real SDK dispatch path.
	const wantDesc = "link this device to your existing center campfire"
	approved, err := client.Authorize(wantDesc)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !called {
		t.Fatal("authorize fn was not called after Authorize()")
	}
	if capturedDesc == "" {
		t.Error("authorize fn received empty description")
	}
	if capturedDesc != wantDesc {
		t.Errorf("authorize fn received description %q, want %q", capturedDesc, wantDesc)
	}
	if !approved {
		t.Errorf("Authorize() returned false, want true (fn returned true)")
	}

	// Verify refusal: reset and configure fn to return false.
	called = false
	approveNext = false
	denied, err := client.Authorize("security-sensitive operation")
	if err != nil {
		t.Fatalf("Authorize (deny): %v", err)
	}
	if !called {
		t.Fatal("authorize fn not called for deny path")
	}
	if denied {
		t.Error("Authorize() returned true when fn returned false")
	}
}

// testInitWithRemote verifies that WithRemote stores the URL on the client.
func testInitWithRemote(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()

	const remoteURL = "https://mcp.example.com"
	client, err := protocol.Init(configDir, protocol.WithRemote(remoteURL))
	if err != nil {
		t.Fatalf("Init(WithRemote): %v", err)
	}
	t.Cleanup(func() { client.Close() })

	got := client.RemoteURL()
	if got != remoteURL {
		t.Errorf("RemoteURL() = %q, want %q", got, remoteURL)
	}
}

// testInitWithWalkUpFalse verifies that WithWalkUp(false) disables walk-up.
func testInitWithWalkUpFalse(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()

	client, err := protocol.Init(configDir, protocol.WithWalkUp(false))
	if err != nil {
		t.Fatalf("Init(WithWalkUp(false)): %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if client.WalkUpEnabled() {
		t.Error("WalkUpEnabled() = true after WithWalkUp(false)")
	}
}
