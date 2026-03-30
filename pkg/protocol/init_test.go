package protocol_test

// Tests for protocol.Init() — campfire-agent-z76.
//
// All tests use real temp dirs, real SQLite stores, and real Ed25519 keys.
// No mocks.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// TestInit runs all Init sub-tests.
func TestInit(t *testing.T) {
	t.Run("GeneratesIdentityAndStore", testInitGeneratesIdentityAndStore)
	t.Run("Idempotency", testInitIdempotency)
	t.Run("RoundTrip", testInitRoundTrip)
	t.Run("StorePersistence", testInitStorePersistence)
	t.Run("IdentityFileExists", testInitIdentityFileExists)
}

// testInitGeneratesIdentityAndStore verifies that Init with no existing identity
// creates a new keypair, persists it, opens a store, and returns a non-nil *Client.
func testInitGeneratesIdentityAndStore(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()

	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if client == nil {
		t.Fatal("Init returned nil client")
	}
	t.Cleanup(func() { client.Close() })

	// identity.json must now exist
	idPath := filepath.Join(configDir, "identity.json")
	if _, err := os.Stat(idPath); err != nil {
		t.Errorf("identity.json not created: %v", err)
	}

	// store.db must now exist
	storePath := filepath.Join(configDir, "store.db")
	if _, err := os.Stat(storePath); err != nil {
		t.Errorf("store.db not created: %v", err)
	}
}

// testInitIdempotency verifies that calling Init twice with the same configDir
// returns a *Client with the same public key both times.
func testInitIdempotency(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()

	c1, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	pub1 := c1.Identity().PublicKey
	c1.Close()

	c2, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	pub2 := c2.Identity().PublicKey
	t.Cleanup(func() { c2.Close() })

	if !bytes.Equal(pub1, pub2) {
		t.Errorf("public keys differ: first=%x second=%x", pub1, pub2)
	}
}

// testInitRoundTrip verifies that the *Client returned by Init can send a
// message to a filesystem campfire and read it back.
func testInitRoundTrip(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()
	transportDir := t.TempDir()

	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	agentID := client.Identity()
	campfireID := setupFilesystemCampfire(t, agentID, client.Store(), transportDir, campfire.RoleFull)

	want := "round-trip payload"
	_, err = client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("Read returned no messages")
	}
	got := string(result.Messages[0].Payload)
	if got != want {
		t.Errorf("payload mismatch: got %q, want %q", got, want)
	}
}

// testInitStorePersistence verifies that messages written in one Init session
// survive a Close and are present after a second Init.
func testInitStorePersistence(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()
	transportDir := t.TempDir()

	// First session: send a message.
	c1, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	agentID := c1.Identity()
	campfireID := setupFilesystemCampfire(t, agentID, c1.Store(), transportDir, campfire.RoleFull)

	want := "persistence payload"
	_, err = c1.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := c1.Close(); err != nil {
		t.Fatalf("Close first client: %v", err)
	}

	// Second session: open the same configDir and read the message.
	c2, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	t.Cleanup(func() { c2.Close() })

	// The campfire membership was stored in c1's store. c2 re-opens the same DB,
	// so membership and message records are still present — no re-setup needed.
	result, err := c2.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		SkipSync:   true, // message already in store, no transport to sync
	})
	if err != nil {
		t.Fatalf("Read after second Init: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("no messages found after re-init — store did not persist")
	}
	got := string(result.Messages[0].Payload)
	if got != want {
		t.Errorf("payload mismatch after re-init: got %q, want %q", got, want)
	}
}

// testInitIdentityFileExists verifies that after Init, the identity file is
// on disk at the expected path and can be loaded by identity.Load.
func testInitIdentityFileExists(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()

	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	idPath := filepath.Join(configDir, "identity.json")
	loaded, err := identity.Load(idPath)
	if err != nil {
		t.Fatalf("identity.Load(%q): %v", idPath, err)
	}
	if loaded == nil {
		t.Fatal("identity.Load returned nil")
	}

	// Loaded identity must match what Init used.
	if !bytes.Equal(loaded.PublicKey, client.Identity().PublicKey) {
		t.Errorf("loaded public key %x doesn't match Init public key %x",
			loaded.PublicKey, client.Identity().PublicKey)
	}
}
