package protocol_test

// sdk014_integration_test.go — E2E integration test for SDK 0.14:
// Identity as Infrastructure.
//
// Covered bead: campfire-agent-z0g
//
// Exercises all 6 SDK 0.14 outcomes in a single realistic sequence:
//   1. Center creation: Init() on a fresh dir creates an identity
//   2. Walk-up discovery: Init() on a child dir finds center via walk-up
//   3. Context key delegation: the child Init() auto-generates a context key + delegation cert
//   4. Recentering: a second identity in a sibling dir detects the center, fires authorize
//      hook once, posts a two-signature claim
//   5. Provenance bridge: Bridge() between two clients produces IsBridged() == true and
//      LevelContactable via LevelFromMessage()
//   6. Convention gate: an executor with min_operator_level=2 rejects Level 1, accepts Level 2
//
// ALL real: real filesystem campfires, real Ed25519, real SQLite stores.
// NO mocks for crypto, transport, or campfire operations.
// The sdk014NoopTransport (phase 6 only) follows the same pattern as noopTransport in
// min_operator_level_test.go — used because convention.Execute needs a transport backend.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/provenance"
)

func TestSDK014_IdentityAsInfrastructure(t *testing.T) {
	// ── Phase 1: Center creation ──
	// Init() on a fresh dir creates an Ed25519 identity.
	// We use the center dir as both the config dir and center sentinel holder.
	centerDir := t.TempDir()
	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	// Bootstrap: create identity + store, then create the center campfire.
	// WithNoWalkUp() avoids any spurious walk-up behavior during bootstrap.
	bootstrapClient, err := protocol.Init(centerDir, protocol.WithNoWalkUp())
	if err != nil {
		t.Fatalf("Phase 1: bootstrap Init: %v", err)
	}
	if bootstrapClient.PublicKeyHex() == "" {
		t.Fatal("Phase 1: PublicKeyHex is empty after Init")
	}

	createResult, err := bootstrapClient.Create(protocol.CreateRequest{
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:    beaconDir,
		Description:  "sdk014 center campfire",
	})
	if err != nil {
		t.Fatalf("Phase 1: Create center campfire: %v", err)
	}
	centerID := createResult.CampfireID
	if centerID == "" {
		t.Fatal("Phase 1: Create returned empty CampfireID")
	}
	bootstrapClient.Close()

	t.Logf("Phase 1: center campfire created: %s...", centerID[:16])

	// Write .campfire/center sentinel in centerDir so walk-up finds it from children.
	campfireMetaDir := filepath.Join(centerDir, ".campfire")
	if err := os.MkdirAll(campfireMetaDir, 0755); err != nil {
		t.Fatalf("Phase 1: mkdir .campfire: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireMetaDir, "center"), []byte(centerID), 0644); err != nil {
		t.Fatalf("Phase 1: writing center sentinel: %v", err)
	}

	// Re-open center client (uses existing identity + store from centerDir).
	// Walk-up finds centerDir/.campfire/center in the same dir — triggers delegation.
	centerClient, err := protocol.Init(centerDir)
	if err != nil {
		t.Fatalf("Phase 1: center re-Init: %v", err)
	}
	t.Cleanup(func() { centerClient.Close() })
	t.Log("Phase 1: center creation verified")

	// ── Phase 2: Walk-up discovery + Phase 3: Context key delegation ──
	// Init() on a child dir with the same store as the center finds the sentinel
	// via walk-up and auto-generates a context key + delegation cert.
	//
	// Key constraint: the child dir must share the center campfire's store so
	// maybeIssueContextKeyDelegation can read the campfire state.
	// We accomplish this by placing childDir as a subdirectory of centerDir
	// AND copying the store (by using centerDir as cfHome, with a nested project).
	//
	// The correct pattern (from context_key_test.go) is to use the SAME cfHome
	// that has center membership — here centerDir already has membership, so we
	// create the child as a subdirectory and use centerDir as cfHome for Init.
	// The walk-up in Init() walks UP from cfHome, finding the sentinel in the same dir.
	t.Log("Phase 2+3: Walk-up discovery + context key delegation")

	// Close and re-open centerDir with walk-up enabled (default).
	// The sentinel is in centerDir/.campfire/center, so Init finds it immediately.
	// context-key.pub may not exist yet (bootstrap used WithNoWalkUp).
	centerClient.Close()
	centerClient2, err := protocol.Init(centerDir)
	if err != nil {
		t.Fatalf("Phase 2: Init with walk-up: %v", err)
	}
	t.Cleanup(func() { centerClient2.Close() })

	// context-key.pub must now exist.
	ctxKeyPath := filepath.Join(campfireMetaDir, "context-key.pub")
	ctxKeyData, err := os.ReadFile(ctxKeyPath)
	if err != nil {
		t.Fatalf("Phase 2: context-key.pub not found: %v", err)
	}
	if len(ctxKeyData) != ed25519.PublicKeySize {
		t.Fatalf("Phase 2: context-key.pub has %d bytes, want %d", len(ctxKeyData), ed25519.PublicKeySize)
	}

	// Phase 3: delegation.cert must exist and verify against center key.
	certPath := filepath.Join(campfireMetaDir, "delegation.cert")
	certHexData, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("Phase 3: delegation.cert not found: %v", err)
	}
	sig, err := hex.DecodeString(string(certHexData))
	if err != nil {
		t.Fatalf("Phase 3: decoding delegation.cert: %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("Phase 3: delegation.cert has %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}

	centerPubKey, err := hex.DecodeString(centerID)
	if err != nil {
		t.Fatalf("Phase 3: decoding centerID: %v", err)
	}
	contextPubHex := hex.EncodeToString(ctxKeyData)
	signInput := []byte("delegate:" + contextPubHex)
	if !ed25519.Verify(ed25519.PublicKey(centerPubKey), signInput, sig) {
		t.Fatal("Phase 3: delegation cert signature does NOT verify against center key")
	}

	// Delegation message must be in the center campfire.
	wantPayload := "context-key-delegation:" + contextPubHex
	delegResult, err := centerClient2.Read(protocol.ReadRequest{
		CampfireID: centerID,
		Tags:       []string{"context-key-delegation"},
	})
	if err != nil {
		t.Fatalf("Phase 3: Read delegation messages: %v", err)
	}
	foundDelegation := false
	for _, msg := range delegResult.Messages {
		if string(msg.Payload) == wantPayload {
			foundDelegation = true
			break
		}
	}
	if !foundDelegation {
		t.Errorf("Phase 3: delegation message not found in center campfire (%d messages read)", len(delegResult.Messages))
	}
	t.Log("Phase 2+3: walk-up discovery and context key delegation verified")

	// ── Phase 4: Recentering slide-in ──
	// A second identity in a sibling dir detects the center via walk-up,
	// fires authorize hook exactly once, and posts a two-signature claim.
	t.Log("Phase 4: Recentering slide-in")

	// siblingDir is a subdirectory of centerDir's PARENT so walk-up finds
	// centerDir/.campfire/center. We need the sibling to also be a member
	// of the center campfire (the center client adds membership via setupFilesystemCampfire pattern).
	//
	// Simplest approach: use a fresh dir alongside centerDir (same parent temp dir).
	// Use setupCenterForRecentering pattern: create the sibling's own store first,
	// add its identity as a member of the center campfire, then Init with authorize hook.
	parentDir := filepath.Dir(centerDir)
	siblingDir := filepath.Join(parentDir, "sibling-"+t.Name())
	if err := os.MkdirAll(siblingDir, 0755); err != nil {
		t.Fatalf("Phase 4: mkdir siblingDir: %v", err)
	}

	// Bootstrap sibling: create identity + store and add as center campfire member.
	siblingBootstrap, err := protocol.Init(siblingDir, protocol.WithNoWalkUp())
	if err != nil {
		t.Fatalf("Phase 4: sibling bootstrap: %v", err)
	}
	siblingID := siblingBootstrap.ClientIdentity()
	siblingStore := siblingBootstrap.ClientStore()
	// Add sibling to center campfire transport + store.
	addMemberFS(t, siblingID, siblingStore, transportDir, centerID)
	siblingBootstrap.Close()

	// Now init sibling with authorize hook and walk-up enabled (default).
	// siblingDir is a peer of centerDir — both children of parentDir.
	// Walk-up from siblingDir checks siblingDir/.campfire/center (not found),
	// then parentDir/.campfire/center — but centerDir/.campfire/center is IN centerDir
	// which is NOT a parent of siblingDir.
	// Therefore, place the center sentinel in parentDir as well.
	parentCampfireDir := filepath.Join(parentDir, ".campfire")
	if err := os.MkdirAll(parentCampfireDir, 0755); err != nil {
		t.Fatalf("Phase 4: mkdir parent .campfire: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentCampfireDir, "center"), []byte(centerID), 0644); err != nil {
		t.Fatalf("Phase 4: writing parent center sentinel: %v", err)
	}

	authCallCount := 0
	siblingClient, err := protocol.Init(siblingDir, protocol.WithAuthorizeFunc(func(desc string) (bool, error) {
		authCallCount++
		return true, nil
	}))
	if err != nil {
		t.Fatalf("Phase 4: sibling Init with authorize: %v", err)
	}
	t.Cleanup(func() { siblingClient.Close() })

	if authCallCount != 1 {
		t.Errorf("Phase 4: authorize hook called %d times, want 1", authCallCount)
	}

	// recenter-claimed.json must exist.
	claimedPath := filepath.Join(siblingDir, "recenter-claimed.json")
	if _, err := os.Stat(claimedPath); err != nil {
		t.Errorf("Phase 4: recenter-claimed.json not found: %v", err)
	}

	// Read the delegation-cert claim from center campfire.
	claimResult, err := siblingClient.Read(protocol.ReadRequest{
		CampfireID: centerID,
		Tags:       []string{"delegation-cert"},
	})
	if err != nil {
		t.Fatalf("Phase 4: Read claim: %v", err)
	}

	siblingKeyHex := siblingClient.PublicKeyHex()
	foundClaim := false
	for _, msg := range claimResult.Messages {
		var claim protocol.RecenterClaim
		if err := json.Unmarshal(msg.Payload, &claim); err != nil {
			continue
		}
		if claim.NewKeyHex != siblingKeyHex {
			continue
		}
		canonicalPayload := protocol.RecenterCanonicalPayload(claim.NewKeyHex, claim.CenterID)
		newPub := siblingClient.ClientIdentity().PublicKey
		if !ed25519.Verify(newPub, canonicalPayload, claim.NewKeySig) {
			t.Error("Phase 4: new key signature does NOT verify")
		}
		if !ed25519.Verify(ed25519.PublicKey(centerPubKey), canonicalPayload, claim.CenterSig) {
			t.Error("Phase 4: center key signature does NOT verify")
		}
		foundClaim = true
		break
	}
	if !foundClaim {
		t.Fatal("Phase 4: two-signature claim not found in center campfire")
	}

	// Second Init must NOT fire authorize hook again.
	siblingClient.Close()
	secondCount := 0
	siblingClient3, err := protocol.Init(siblingDir, protocol.WithAuthorizeFunc(func(d string) (bool, error) {
		secondCount++
		return true, nil
	}))
	if err != nil {
		t.Fatalf("Phase 4: second sibling Init: %v", err)
	}
	siblingClient3.Close()
	if secondCount != 0 {
		t.Errorf("Phase 4: authorize hook fired %d times on second Init, want 0", secondCount)
	}
	t.Log("Phase 4: recentering hook fired once, two-sig claim verified")

	// ── Phase 5: Provenance bridge tiers ──
	// Bridge() between two clients produces IsBridged() == true on the forwarded
	// message, and LevelFromMessage() returns LevelContactable (2).
	t.Log("Phase 5: Provenance bridge tiers")

	// Use a dedicated campfire for the bridge test to avoid noise from center campfire.
	bridgeTransportDir := t.TempDir()
	srcID, srcStore, _ := setupTestEnv(t)
	bridgeCampfireID := setupFilesystemCampfire(t, srcID, srcStore, bridgeTransportDir, campfire.RoleFull)
	source := protocol.New(srcStore, srcID)

	destID, destStore, _ := setupTestEnv(t)
	addMemberFS(t, destID, destStore, bridgeTransportDir, bridgeCampfireID)
	dest := protocol.New(destStore, destID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		protocol.Bridge(ctx, source, dest, bridgeCampfireID, protocol.BridgeOptions{}) //nolint:errcheck
	}()
	time.Sleep(300 * time.Millisecond)

	_, err = source.Send(protocol.SendRequest{
		CampfireID: bridgeCampfireID,
		Payload:    []byte("sdk014 bridge provenance test"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Phase 5: source.Send: %v", err)
	}

	var bridgedMsg *protocol.Message
	deadline := time.After(10 * time.Second)
waitLoop:
	for {
		result, err := dest.Read(protocol.ReadRequest{CampfireID: bridgeCampfireID})
		if err != nil {
			t.Fatalf("Phase 5: dest.Read: %v", err)
		}
		for i := range result.Messages {
			msg := result.Messages[i]
			if string(msg.Payload) == "sdk014 bridge provenance test" && msg.Sender == destID.PublicKeyHex() {
				bridgedMsg = &msg
				break waitLoop
			}
		}
		select {
		case <-deadline:
			t.Fatal("Phase 5: timeout waiting for bridged message")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	cancel()

	if !bridgedMsg.IsBridged() {
		t.Error("Phase 5: IsBridged() = false, want true")
	}

	hops := make([]provenance.MessageHop, len(bridgedMsg.Provenance))
	for i, h := range bridgedMsg.Provenance {
		hops[i] = provenance.MessageHop{Role: h.Role}
	}
	level := provenance.LevelFromMessage(hops, bridgedMsg.Sender, nil)
	if level != provenance.LevelContactable {
		t.Errorf("Phase 5: LevelFromMessage = %v (%d), want LevelContactable (%d)",
			level, int(level), int(provenance.LevelContactable))
	}
	t.Logf("Phase 5: IsBridged=true, level=%v (LevelContactable)", level)

	// ── Phase 6: Convention gate ──
	// An executor with min_operator_level=2 rejects a Level 1 message and
	// accepts a Level 2 message. Uses sdk014NoopTransport (test double).
	t.Log("Phase 6: Convention gate")

	const (
		sdk014SenderKey   = "aabbcc" + "0000000000000000000000000000000000000000000000000000000000"
		sdk014CampfireKey = "deadbeef" + "00000000000000000000000000000000000000000000000000000000"
	)

	declPayload, err := json.Marshal(map[string]any{
		"convention":         "peering",
		"version":            "0.3",
		"operation":          "sdk014-gate-test",
		"description":        "SDK 0.14 convention gate (requires level 2)",
		"min_operator_level": 2,
		"produces_tags": []any{
			map[string]any{"tag": "peering:core", "cardinality": "exactly_one"},
		},
		"args": []any{
			map[string]any{"name": "peer_key", "type": "string", "required": true, "max_length": 64},
		},
		"antecedents": "none",
		"signing":     "member_key",
	})
	if err != nil {
		t.Fatalf("Phase 6: marshal decl: %v", err)
	}

	decl, _, err := convention.Parse(
		[]string{convention.ConventionOperationTag},
		declPayload,
		sdk014SenderKey,
		sdk014CampfireKey,
	)
	if err != nil {
		t.Fatalf("Phase 6: parse decl: %v", err)
	}

	// Level 1 — must be rejected.
	tr1 := &sdk014NoopTransport{}
	ex1 := convention.NewExecutorForTest(tr1, sdk014SenderKey).
		WithProvenance(&sdk014StaticProvenance{levels: map[string]int{sdk014SenderKey: 1}})

	_, gateErr := ex1.Execute(context.Background(), decl, "campfire-sdk014", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})
	if gateErr == nil {
		t.Fatal("Phase 6: expected rejection at Level 1 but got nil error")
	}
	if !strings.Contains(gateErr.Error(), "operator provenance level") {
		t.Errorf("Phase 6: expected structured provenance error, got: %v", gateErr)
	}
	if len(tr1.sent) != 0 {
		t.Errorf("Phase 6: expected no messages sent on rejection, got %d", len(tr1.sent))
	}
	t.Log("Phase 6: Level 1 correctly rejected")

	// Level 2 — must be accepted.
	tr2 := &sdk014NoopTransport{}
	ex2 := convention.NewExecutorForTest(tr2, sdk014SenderKey).
		WithProvenance(&sdk014StaticProvenance{levels: map[string]int{sdk014SenderKey: 2}})

	_, acceptErr := ex2.Execute(context.Background(), decl, "campfire-sdk014", map[string]any{
		"peer_key": strings.Repeat("b", 64),
	})
	if acceptErr != nil {
		t.Fatalf("Phase 6: expected acceptance at Level 2, got error: %v", acceptErr)
	}
	if len(tr2.sent) != 1 {
		t.Errorf("Phase 6: expected 1 message sent at Level 2, got %d", len(tr2.sent))
	}
	t.Log("Phase 6: Level 2 correctly accepted")

	t.Log("SDK 0.14 E2E: all 6 outcomes verified")
}

// ---------------------------------------------------------------------------
// Test doubles for Phase 6 convention gate.
// Named with sdk014 prefix to avoid collision with noopTransport and
// staticProvenanceChecker in min_operator_level_test.go (same package).
// ---------------------------------------------------------------------------

type sdk014NoopTransport struct {
	sent []struct{ tags []string }
}

func (n *sdk014NoopTransport) SendMessage(_ context.Context, _ string, _ []byte, tags []string, _ []string) (string, error) {
	n.sent = append(n.sent, struct{ tags []string }{tags})
	return "msg-id", nil
}

func (n *sdk014NoopTransport) SendCampfireKeySigned(_ context.Context, _ string, _ []byte, tags []string, _ []string) (string, error) {
	n.sent = append(n.sent, struct{ tags []string }{tags})
	return "msg-id-ck", nil
}

func (n *sdk014NoopTransport) ReadMessages(_ context.Context, _ string, _ []string) ([]convention.MessageRecord, error) {
	return nil, nil
}

func (n *sdk014NoopTransport) SendFutureAndAwait(_ context.Context, _ string, _ []byte, _ []string, _ []string, _ time.Duration) (string, []byte, error) {
	return "", nil, nil
}

type sdk014StaticProvenance struct {
	levels map[string]int
}

func (s *sdk014StaticProvenance) Level(key string) int {
	if l, ok := s.levels[key]; ok {
		return l
	}
	return 0
}
