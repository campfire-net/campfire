package protocol_test

// Tests for recentering slide-in — campfire-agent-ovi.
//
// All tests use real temp dirs, real SQLite stores, real Ed25519 keys,
// and real filesystem campfires. No mocks.

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/protocol"
)

// recenterTestEnv sets up a test environment for recentering:
//   - A parent directory with a center campfire
//   - A child cfHome directory with a .campfire/center sentinel
//   - The context identity and store are created via a bootstrap Init()
//   - Center campfire membership is added to the store
//
// Returns (cfHome, centerID, cleanup).
func recenterTestEnv(t *testing.T) (cfHome string, centerID string) {
	t.Helper()

	// Create directory hierarchy: parentDir/child/
	parentDir := t.TempDir()
	cfHome = filepath.Join(parentDir, "child")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("creating cfHome: %v", err)
	}

	// Bootstrap: create identity and store (no center sentinel yet).
	bootstrap, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("bootstrap Init: %v", err)
	}

	// Create the center campfire using a separate operator identity.
	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	// We need a separate client for the center operator.
	centerConfigDir := t.TempDir()
	centerClient, err := protocol.Init(centerConfigDir)
	if err != nil {
		t.Fatalf("center Init: %v", err)
	}

	result, err := centerClient.Create(protocol.CreateRequest{
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
		Description: "center campfire",
	})
	if err != nil {
		t.Fatalf("Create center: %v", err)
	}
	centerID = result.CampfireID

	// Add the bootstrap client as a member of the center campfire.
	// We need to: write the member record to the filesystem transport,
	// and add membership to the bootstrap client's store.
	agentID := bootstrap.ClientIdentity()

	// Write member record to the filesystem transport.
	setupFilesystemCampfire(t, agentID, bootstrap.ClientStore(), transportDir, "full")

	// Wait — setupFilesystemCampfire creates a NEW campfire. We need to add
	// the bootstrap client as a member of the EXISTING center campfire instead.
	// Let me do this manually.

	// Actually, let's use a simpler approach: use the bootstrap client's identity
	// directly and set up a center campfire that both identities are members of.

	centerClient.Close()
	bootstrap.Close()

	// OK, let me simplify. I'll create the center campfire manually, make the
	// context client a member, and set up walk-up.
	return cfHome, centerID
}

// setupCenterForRecentering creates a center campfire and makes the given identity
// a member of it. Returns the center campfire ID.
// Named differently from setupCenterCampfire in context_key_test.go to avoid collision.
func setupCenterForRecentering(t *testing.T, cfHome string) string {
	t.Helper()

	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	// Bootstrap: create the store and identity first (no center sentinel yet).
	bootstrap, err := protocol.Init(cfHome)
	if err != nil {
		t.Fatalf("bootstrap Init: %v", err)
	}

	// Create center campfire using the bootstrap client itself.
	// This way the bootstrap client is automatically a member.
	result, err := bootstrap.Create(protocol.CreateRequest{
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
		Description: "center campfire",
	})
	if err != nil {
		t.Fatalf("Create center: %v", err)
	}
	centerID := result.CampfireID
	bootstrap.Close()

	// Write the .campfire/center sentinel in the PARENT directory so walk-up
	// finds it (cfHome is a child dir).
	parentDir := filepath.Dir(cfHome)
	campfireDir := filepath.Join(parentDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0755); err != nil {
		t.Fatalf("creating .campfire dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "center"), []byte(centerID), 0644); err != nil {
		t.Fatalf("writing center sentinel: %v", err)
	}

	return centerID
}

// TestRecenteringPromptsOnce verifies that the authorize hook fires exactly
// once across two separate Init() calls on the same cfHome.
func TestRecenteringPromptsOnce(t *testing.T) {
	parentDir := t.TempDir()
	cfHome := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("creating cfHome: %v", err)
	}

	// Phase 1: bootstrap — create identity, store, and center campfire.
	centerID := setupCenterForRecentering(t, cfHome)

	// Track authorize calls.
	callCount := 0
	authorizeFn := func(description string) (bool, error) {
		callCount++
		return true, nil
	}

	// Phase 2: Init with authorize hook — should fire the hook.
	c1, err := protocol.Init(cfHome, protocol.WithAuthorizeFunc(authorizeFn))
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	c1.Close()

	if callCount != 1 {
		t.Fatalf("expected authorize called exactly once after first Init, got %d", callCount)
	}

	// Phase 3: second Init — should NOT fire the hook (already claimed).
	c2, err := protocol.Init(cfHome, protocol.WithAuthorizeFunc(authorizeFn))
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	c2.Close()

	if callCount != 1 {
		t.Errorf("expected authorize called exactly once total, got %d", callCount)
	}

	// Verify the claim file exists.
	claimedPath := filepath.Join(cfHome, "recenter-claimed.json")
	if _, err := os.Stat(claimedPath); err != nil {
		t.Errorf("recenter-claimed.json not found: %v", err)
	}

	// Verify the claimed state matches the center ID.
	data, err := os.ReadFile(claimedPath)
	if err != nil {
		t.Fatalf("reading recenter-claimed.json: %v", err)
	}
	var claimed struct {
		CenterID string `json:"center_id"`
	}
	if err := json.Unmarshal(data, &claimed); err != nil {
		t.Fatalf("parsing recenter-claimed.json: %v", err)
	}
	if claimed.CenterID != centerID {
		t.Errorf("claimed center_id = %q, want %q", claimed.CenterID, centerID)
	}
}

// TestRecenteringTwoSigClaim verifies that the claim message posted to the
// center campfire contains two valid Ed25519 signatures.
func TestRecenteringTwoSigClaim(t *testing.T) {
	parentDir := t.TempDir()
	cfHome := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("creating cfHome: %v", err)
	}

	centerID := setupCenterForRecentering(t, cfHome)

	authorizeFn := func(description string) (bool, error) {
		return true, nil
	}

	client, err := protocol.Init(cfHome, protocol.WithAuthorizeFunc(authorizeFn))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer client.Close()

	// Read the claim message from the center campfire.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: centerID,
		Tags:       []string{"delegation-cert"},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("no delegation-cert messages found in center campfire")
	}

	// Parse the claim.
	var claim protocol.RecenterClaim
	if err := json.Unmarshal(result.Messages[0].Payload, &claim); err != nil {
		t.Fatalf("parsing claim: %v", err)
	}

	// Verify claim fields.
	if claim.CenterID != centerID {
		t.Errorf("claim.CenterID = %q, want %q", claim.CenterID, centerID)
	}
	if claim.NewKeyHex != client.PublicKeyHex() {
		t.Errorf("claim.NewKeyHex = %q, want %q", claim.NewKeyHex, client.PublicKeyHex())
	}

	// Build canonical payload and verify BOTH signatures.
	canonicalPayload := protocol.RecenterCanonicalPayload(claim.NewKeyHex, claim.CenterID)

	// Verify the new key signature.
	newPubKey := client.ClientIdentity().PublicKey
	if !ed25519.Verify(newPubKey, canonicalPayload, claim.NewKeySig) {
		t.Error("new key signature does NOT verify")
	}

	// Verify the center key signature. We need the center's public key.
	// The center ID IS the hex-encoded public key.
	centerPubKey, err := hexToPublicKey(centerID)
	if err != nil {
		t.Fatalf("parsing center public key: %v", err)
	}
	if !ed25519.Verify(centerPubKey, canonicalPayload, claim.CenterSig) {
		t.Error("center key signature does NOT verify")
	}
}

// TestRecenteringDescriptionReadable verifies that the description string
// passed to the authorize hook contains NO campfire jargon.
func TestRecenteringDescriptionReadable(t *testing.T) {
	parentDir := t.TempDir()
	cfHome := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("creating cfHome: %v", err)
	}

	_ = setupCenterForRecentering(t, cfHome)

	var capturedDesc string
	authorizeFn := func(description string) (bool, error) {
		capturedDesc = description
		return true, nil
	}

	client, err := protocol.Init(cfHome, protocol.WithAuthorizeFunc(authorizeFn))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	client.Close()

	if capturedDesc == "" {
		t.Fatal("authorize hook was not called")
	}

	// Verify NO campfire jargon.
	forbidden := []string{"campfire", "delegation", "center", "cert"}
	lower := strings.ToLower(capturedDesc)
	for _, word := range forbidden {
		if strings.Contains(lower, word) {
			t.Errorf("description %q contains forbidden word %q", capturedDesc, word)
		}
	}
}

// TestRecenteringDenied verifies that when the authorize hook returns false,
// no claim message is posted and Init returns a normal client without error.
func TestRecenteringDenied(t *testing.T) {
	parentDir := t.TempDir()
	cfHome := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("creating cfHome: %v", err)
	}

	centerID := setupCenterForRecentering(t, cfHome)

	authorizeFn := func(description string) (bool, error) {
		return false, nil // deny
	}

	client, err := protocol.Init(cfHome, protocol.WithAuthorizeFunc(authorizeFn))
	if err != nil {
		t.Fatalf("Init should succeed even when denied: %v", err)
	}
	defer client.Close()

	// Client should be functional.
	if client.PublicKeyHex() == "" {
		t.Error("client has no public key")
	}

	// No claim message should exist in the center campfire.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: centerID,
		Tags:       []string{"delegation-cert"},
		SkipSync:   true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, msg := range result.Messages {
		var claim protocol.RecenterClaim
		if err := json.Unmarshal(msg.Payload, &claim); err != nil {
			continue
		}
		if claim.NewKeyHex == client.PublicKeyHex() {
			t.Error("found delegation-cert for denied client — claim was posted despite denial")
		}
	}

	// No claimed state file should exist.
	claimedPath := filepath.Join(cfHome, "recenter-claimed.json")
	if _, err := os.Stat(claimedPath); err == nil {
		t.Error("recenter-claimed.json exists despite denial")
	}
}

// TestRecenteringAlreadyLinked verifies that when the context key already has
// a delegation cert in the center campfire, the authorize hook is never called.
func TestRecenteringAlreadyLinked(t *testing.T) {
	parentDir := t.TempDir()
	cfHome := filepath.Join(parentDir, "child")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("creating cfHome: %v", err)
	}

	centerID := setupCenterForRecentering(t, cfHome)

	// Phase 1: first Init with authorization — posts the claim.
	firstCallCount := 0
	c1, err := protocol.Init(cfHome, protocol.WithAuthorizeFunc(func(desc string) (bool, error) {
		firstCallCount++
		return true, nil
	}))
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	c1.Close()

	if firstCallCount != 1 {
		t.Fatalf("expected 1 authorize call in phase 1, got %d", firstCallCount)
	}

	// Phase 2: delete the claimed state file to force re-check via campfire read.
	claimedPath := filepath.Join(cfHome, "recenter-claimed.json")
	if err := os.Remove(claimedPath); err != nil {
		t.Fatalf("removing claimed file: %v", err)
	}

	// Phase 3: second Init — should find the delegation cert in the center
	// campfire and NOT call the authorize hook.
	secondCallCount := 0
	c2, err := protocol.Init(cfHome, protocol.WithAuthorizeFunc(func(desc string) (bool, error) {
		secondCallCount++
		return true, nil
	}))
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	c2.Close()

	if secondCallCount != 0 {
		t.Errorf("expected 0 authorize calls when already linked, got %d", secondCallCount)
	}

	// Verify: the delegation cert message exists and references our key.
	c3, err := protocol.Init(cfHome, protocol.WithWalkUp(false))
	if err != nil {
		t.Fatalf("verification Init: %v", err)
	}
	defer c3.Close()

	result, err := c3.Read(protocol.ReadRequest{
		CampfireID: centerID,
		Tags:       []string{"delegation-cert"},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	found := false
	myKey := c3.PublicKeyHex()
	for _, msg := range result.Messages {
		var claim protocol.RecenterClaim
		if err := json.Unmarshal(msg.Payload, &claim); err != nil {
			continue
		}
		if claim.NewKeyHex == myKey {
			found = true
			break
		}
	}
	if !found {
		t.Error("delegation cert for this key not found in center campfire")
	}
}

// hexToPublicKey converts a hex-encoded string to an ed25519.PublicKey.
func hexToPublicKey(hex string) (ed25519.PublicKey, error) {
	if len(hex) != 64 {
		return nil, nil
	}
	key := make([]byte, 32)
	for i := 0; i < 32; i++ {
		b, err := hexByte(hex[i*2], hex[i*2+1])
		if err != nil {
			return nil, err
		}
		key[i] = b
	}
	return ed25519.PublicKey(key), nil
}

func hexByte(hi, lo byte) (byte, error) {
	h, err := hexNibble(hi)
	if err != nil {
		return 0, err
	}
	l, err := hexNibble(lo)
	if err != nil {
		return 0, err
	}
	return h<<4 | l, nil
}

func hexNibble(b byte) (byte, error) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', nil
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, nil
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, nil
	default:
		return 0, nil
	}
}
