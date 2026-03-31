package protocol_test

// context_key_test.go — tests for context key delegation in protocol.Init().
//
// All tests use real in-process filesystem campfires and real Ed25519 crypto.
// No mocks.
//
// Test setup pattern:
//   1. Create a center campfire (using setupFilesystemCampfire via a bootstrap client).
//   2. Write the .campfire/center sentinel in the project dir (cfHome).
//   3. Call protocol.Init(cfHome) — this triggers delegation.
//   4. Assert postconditions.

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/protocol"
)

// setupCenterCampfire creates a center campfire via a bootstrap client, writes
// the .campfire/center sentinel in projDir, and returns the campfire ID.
// The bootstrap client's store (stored in bootstrapCfHome) has membership for
// the center campfire so Init() can read the campfire state.
//
// The returned cfHome is the same as the projDir: Init() is called on it, and
// ResolveContext finds projDir/.campfire/center in the first walk-up iteration.
func setupCenterCampfire(t *testing.T) (cfHome string, centerCampfireID string) {
	t.Helper()

	// projDir acts as cfHome — the directory Init() is called with.
	projDir := t.TempDir()
	transportDir := t.TempDir()

	// Use projDir as cfHome so Init() reuses the same store that knows about the center campfire.
	client, err := protocol.Init(projDir)
	if err != nil {
		t.Fatalf("bootstrap Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	// Create the center campfire.
	res, err := client.Create(protocol.CreateRequest{
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create center campfire: %v", err)
	}
	centerCampfireID = res.CampfireID

	// Write the .campfire/center sentinel so Init() can find it.
	campfireDir := filepath.Join(projDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0755); err != nil {
		t.Fatalf("mkdir .campfire: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "center"), []byte(centerCampfireID), 0644); err != nil {
		t.Fatalf("writing center sentinel: %v", err)
	}

	return projDir, centerCampfireID
}

// TestContextKeyDelegation runs all context key delegation sub-tests.
func TestContextKeyDelegation(t *testing.T) {
	t.Run("ContextKeyCreated", testContextKeyCreated)
	t.Run("DelegationCertValid", testDelegationCertValid)
	t.Run("DelegationCertInCampfire", testDelegationCertInCampfire)
	t.Run("ContextKeyIdempotent", testContextKeyIdempotent)
	t.Run("NoContextKeyWithoutCenter", testNoContextKeyWithoutCenter)
}

// testContextKeyCreated verifies that after Init() with a center campfire sentinel,
// context-key.pub exists and is a valid 32-byte Ed25519 public key.
func testContextKeyCreated(t *testing.T) {
	t.Helper()
	cfHome, _ := setupCenterCampfire(t)

	// Init a second client — the first client already wrote the center sentinel.
	// Close the first client (from setupCenterCampfire) and re-init.
	client2, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client2.Close() })

	keyPath := filepath.Join(cfHome, ".campfire", "context-key.pub")
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("context-key.pub not found: %v", err)
	}
	if len(data) != ed25519.PublicKeySize {
		t.Errorf("context-key.pub has %d bytes, want %d (Ed25519 public key size)", len(data), ed25519.PublicKeySize)
	}
	// Verify it's a usable Ed25519 key (non-zero).
	var allZero bool = true
	for _, b := range data {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("context-key.pub is all-zero bytes — not a valid key")
	}
}

// testDelegationCertValid verifies that the delegation cert signature is valid:
// it was signed by the center campfire's key over "delegate:" + hex(contextPubKey).
func testDelegationCertValid(t *testing.T) {
	t.Helper()
	cfHome, centerCampfireID := setupCenterCampfire(t)

	client2, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client2.Close() })

	campfireDir := filepath.Join(cfHome, ".campfire")

	// Read context public key.
	pubData, err := os.ReadFile(filepath.Join(campfireDir, "context-key.pub"))
	if err != nil {
		t.Fatalf("reading context-key.pub: %v", err)
	}
	contextPubKey := ed25519.PublicKey(pubData)
	contextPubHex := hex.EncodeToString(contextPubKey)

	// Read delegation cert (hex-encoded signature).
	certHex, err := os.ReadFile(filepath.Join(campfireDir, "delegation.cert"))
	if err != nil {
		t.Fatalf("reading delegation.cert: %v", err)
	}
	sig, err := hex.DecodeString(string(certHex))
	if err != nil {
		t.Fatalf("decoding delegation.cert hex: %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("delegation.cert has %d bytes signature, want %d", len(sig), ed25519.SignatureSize)
	}

	// Retrieve the center campfire's public key from the membership.
	// The center's public key is the campfireID decoded from hex.
	centerPubKey, err := hex.DecodeString(centerCampfireID)
	if err != nil {
		t.Fatalf("decoding center campfire ID as public key: %v", err)
	}

	// Verify: center_key.Sign("delegate:" + contextPubHex) == sig
	signInput := []byte("delegate:" + contextPubHex)
	if !ed25519.Verify(ed25519.PublicKey(centerPubKey), signInput, sig) {
		t.Error("delegation cert signature verification failed — cert was not signed by the center campfire key")
	}
}

// testDelegationCertInCampfire verifies that the center campfire message history
// contains the delegation message posted by Init().
func testDelegationCertInCampfire(t *testing.T) {
	t.Helper()
	cfHome, centerCampfireID := setupCenterCampfire(t)

	client2, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client2.Close() })

	// Read the context public key so we can check the message payload.
	pubData, err := os.ReadFile(filepath.Join(cfHome, ".campfire", "context-key.pub"))
	if err != nil {
		t.Fatalf("reading context-key.pub: %v", err)
	}
	contextPubHex := hex.EncodeToString(pubData)
	wantPayload := "context-key-delegation:" + contextPubHex
	wantTag := "context-key-delegation"

	// Read messages from the center campfire.
	result, err := client2.Read(protocol.ReadRequest{
		CampfireID: centerCampfireID,
		Tags:       []string{wantTag},
	})
	if err != nil {
		t.Fatalf("Read center campfire: %v", err)
	}

	found := false
	for _, msg := range result.Messages {
		if string(msg.Payload) == wantPayload {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("delegation message not found in center campfire; read %d messages with tag %q; want payload %q",
			len(result.Messages), wantTag, wantPayload)
	}
}

// testContextKeyIdempotent verifies that a second Init() call does not create
// a new key or a new cert (the existing context-key.pub is preserved).
func testContextKeyIdempotent(t *testing.T) {
	t.Helper()
	cfHome, centerCampfireID := setupCenterCampfire(t)

	// First real Init (setupCenterCampfire already did one, but we need to trigger
	// delegation by calling Init again after the center sentinel is in place).
	client2, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	client2.Close()

	// Read the context public key after first delegation.
	pubData1, err := os.ReadFile(filepath.Join(cfHome, ".campfire", "context-key.pub"))
	if err != nil {
		t.Fatalf("reading context-key.pub after first Init: %v", err)
	}

	// Second Init — must not create a new key.
	client3, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	t.Cleanup(func() { client3.Close() })

	pubData2, err := os.ReadFile(filepath.Join(cfHome, ".campfire", "context-key.pub"))
	if err != nil {
		t.Fatalf("reading context-key.pub after second Init: %v", err)
	}

	if string(pubData1) != string(pubData2) {
		t.Error("context-key.pub changed after second Init — key was re-generated (not idempotent)")
	}

	// Count delegation messages in the center campfire — should still be exactly 1.
	client4, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("third Init for read: %v", err)
	}
	defer client4.Close()

	result, err := client4.Read(protocol.ReadRequest{
		CampfireID: centerCampfireID,
		Tags:       []string{"context-key-delegation"},
	})
	if err != nil {
		t.Fatalf("Read center campfire: %v", err)
	}

	// Count messages with the exact delegation payload for our context key.
	contextPubHex := hex.EncodeToString(pubData2)
	wantPayload := "context-key-delegation:" + contextPubHex
	count := 0
	for _, msg := range result.Messages {
		if string(msg.Payload) == wantPayload {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 delegation message for context key, found %d", count)
	}
}

// testNoContextKeyWithoutCenter verifies that Init() with no .campfire/center
// sentinel in the walk-up path does not create context-key.pub and does not error.
func testNoContextKeyWithoutCenter(t *testing.T) {
	t.Helper()
	cfHome := t.TempDir()

	// No .campfire/center sentinel — plain Init.
	client, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("Init without center: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	keyPath := filepath.Join(cfHome, ".campfire", "context-key.pub")
	if _, err := os.Stat(keyPath); err == nil {
		t.Error("context-key.pub was created even though no center campfire was found")
	}
}

// Ensure centerCampfireID is used in tests (suppress "declared but not used" for
// tests that only use cfHome but not centerCampfireID directly).
var _ = func() string { return "" }
