package cmd

// Tests in this file invoke rootCmd.Execute() with rootCmd.SetArgs and reset
// the package-level jsonOutput flag in setup helpers. Because rootCmd is a
// package-level cobra var, these tests MUST NOT run with t.Parallel(): two
// goroutines mutating rootCmd.SetArgs concurrently produce non-deterministic
// argv corruption. Keep this in mind when adding new tests in this package.
// See campfireagent-2fe veracity finding (severity:low) for context.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/trust"
)

// runTrustPin executes `cf trust pin` with the given args and captures stdout.
func runTrustPin(t *testing.T, args ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = false
	rootCmd.SetArgs(append([]string{"trust", "pin"}, args...))
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	return buf.String(), runErr
}

// runTrustUnpin executes `cf trust unpin` with the given args and captures stdout.
func runTrustUnpin(t *testing.T, args ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = false
	rootCmd.SetArgs(append([]string{"trust", "unpin"}, args...))
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	return buf.String(), runErr
}

// runTrustList executes `cf trust list` with the given args and captures stdout.
func runTrustList(t *testing.T, args ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = false
	rootCmd.SetArgs(append([]string{"trust", "list"}, args...))
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	return buf.String(), runErr
}

// runTrustPrune executes `cf trust prune` with the given args and captures stdout.
func runTrustPrune(t *testing.T, args ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = false
	// Reset dry-run flag between tests.
	trustPruneCmd.Flags().Set("dry-run", "false") //nolint:errcheck

	rootCmd.SetArgs(append([]string{"trust", "prune"}, args...))
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	return buf.String(), runErr
}

// makeFakeCampfireID generates a synthetic 64-char hex campfire ID for testing.
func makeFakeCampfireID(t *testing.T, seed string) string {
	t.Helper()
	// Pad seed to 32 bytes and use as ed25519 seed for deterministic but unique IDs.
	b := []byte(seed)
	for len(b) < 32 {
		b = append(b, 0)
	}
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key for fake campfire ID: %v", err)
	}
	return hex.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// makeFakePubKey generates a random 32-byte ed25519 public key as hex.
func makeFakePubKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating pubkey: %v", err)
	}
	return hex.EncodeToString(pub)
}

// TestTrustPin_Success pins a key for a campfire and verifies the key pin store.
func TestTrustPin_Success(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	campfireID := makeFakeCampfireID(t, "test-campfire-1")
	pubKey := makeFakePubKey(t)

	out, err := runTrustPin(t, campfireID, pubKey)
	if err != nil {
		t.Fatalf("cf trust pin: %v\nout=%s", err, out)
	}

	if !strings.Contains(out, "pinned:") {
		t.Errorf("expected 'pinned:' in output, got: %s", out)
	}

	// Verify the key pin was persisted to disk.
	agentID, loadErr := loadIdentity()
	if loadErr != nil {
		t.Fatalf("loading identity: %v", loadErr)
	}
	pinPath := filepath.Join(cfHomeDir, "key-pins.json")
	kps, kpsErr := trust.NewKeyPinStore(pinPath, agentID.PrivateKey)
	if kpsErr != nil {
		t.Fatalf("opening key pin store: %v", kpsErr)
	}

	pin := kps.GetKeyPin(campfireID)
	if pin == nil {
		t.Fatalf("key pin not found after cf trust pin")
	}
	if pin.PubKey != pubKey {
		t.Errorf("PubKey = %q, want %q", pin.PubKey, pubKey)
	}
	if pin.CampfireID != campfireID {
		t.Errorf("CampfireID = %q, want %q", pin.CampfireID, campfireID)
	}
	if pin.PinnedAt.IsZero() {
		t.Error("PinnedAt should not be zero")
	}
}

// TestTrustPin_Idempotent verifies that re-pinning the same campfire replaces
// the existing pin rather than erroring.
func TestTrustPin_Idempotent(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	campfireID := makeFakeCampfireID(t, "test-campfire-idem")
	pubKey1 := makeFakePubKey(t)
	pubKey2 := makeFakePubKey(t)

	// Pin once.
	_, err := runTrustPin(t, campfireID, pubKey1)
	if err != nil {
		t.Fatalf("first pin: %v", err)
	}

	// Pin again with a different key — should succeed (replace).
	out, err := runTrustPin(t, campfireID, pubKey2)
	if err != nil {
		t.Fatalf("second pin: %v\nout=%s", err, out)
	}

	// Verify the second key is stored.
	agentID, _ := loadIdentity()
	kps, _ := trust.NewKeyPinStore(filepath.Join(cfHomeDir, "key-pins.json"), agentID.PrivateKey)
	pin := kps.GetKeyPin(campfireID)
	if pin == nil {
		t.Fatal("key pin not found after second pin")
	}
	if pin.PubKey != pubKey2 {
		t.Errorf("PubKey = %q, want %q (second key)", pin.PubKey, pubKey2)
	}
}

// TestTrustPin_MissingArgs checks cobra rejects invocations with wrong arg count.
func TestTrustPin_MissingArgs(t *testing.T) {
	setupTrustTestEnv(t)

	campfireID := makeFakeCampfireID(t, "test-campfire-args")

	// Only one arg — should fail.
	_, err := runTrustPin(t, campfireID)
	if err == nil {
		t.Fatal("expected error for missing pubkey arg, got nil")
	}
}

// TestTrustPin_MalformedPubKey checks that a non-hex pubkey returns an error.
func TestTrustPin_MalformedPubKey(t *testing.T) {
	setupTrustTestEnv(t)

	campfireID := makeFakeCampfireID(t, "test-campfire-hex")
	_, err := runTrustPin(t, campfireID, "not-a-hex-string")
	if err == nil {
		t.Fatal("expected error for malformed pubkey, got nil")
	}
	if !strings.Contains(err.Error(), "invalid pubkey") {
		t.Errorf("expected 'invalid pubkey' error, got: %v", err)
	}
}

// TestTrustPin_WrongSizePubKey checks that a valid hex string of wrong length errors.
func TestTrustPin_WrongSizePubKey(t *testing.T) {
	setupTrustTestEnv(t)

	campfireID := makeFakeCampfireID(t, "test-campfire-size")
	// 31 bytes = 62 hex chars — not ed25519 size (32 bytes / 64 chars)
	_, err := runTrustPin(t, campfireID, strings.Repeat("aa", 31))
	if err == nil {
		t.Fatal("expected error for wrong-size pubkey, got nil")
	}
}

// TestTrustPin_JSONOutput verifies --json outputs valid JSON with required keys.
func TestTrustPin_JSONOutput(t *testing.T) {
	setupTrustTestEnv(t)

	campfireID := makeFakeCampfireID(t, "test-campfire-json")
	pubKey := makeFakePubKey(t)

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = true
	defer func() { jsonOutput = false }()

	rootCmd.SetArgs([]string{"trust", "pin", campfireID, pubKey, "--json"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	if runErr != nil {
		t.Fatalf("cf trust pin --json: %v\nout=%s", runErr, buf.String())
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("parsing JSON: %v\nout=%s", err, buf.String())
	}
	for _, key := range []string{"campfire_id", "pubkey", "pinned_at"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("JSON output missing key %q: %v", key, parsed)
		}
	}
	if parsed["campfire_id"] != campfireID {
		t.Errorf("campfire_id = %v, want %s", parsed["campfire_id"], campfireID)
	}
	if parsed["pubkey"] != pubKey {
		t.Errorf("pubkey = %v, want %s", parsed["pubkey"], pubKey)
	}
}

// TestTrustUnpin_Success pins and then unpins a campfire, verifying removal.
func TestTrustUnpin_Success(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	campfireID := makeFakeCampfireID(t, "test-campfire-unpin")
	pubKey := makeFakePubKey(t)

	// Pin first.
	if _, err := runTrustPin(t, campfireID, pubKey); err != nil {
		t.Fatalf("pinning: %v", err)
	}

	// Then unpin.
	out, err := runTrustUnpin(t, campfireID)
	if err != nil {
		t.Fatalf("cf trust unpin: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "unpinned:") {
		t.Errorf("expected 'unpinned:' in output, got: %s", out)
	}

	// Verify pin is gone.
	agentID, _ := loadIdentity()
	kps, _ := trust.NewKeyPinStore(filepath.Join(cfHomeDir, "key-pins.json"), agentID.PrivateKey)
	if kps.GetKeyPin(campfireID) != nil {
		t.Error("key pin should be gone after unpin, but it's still there")
	}
}

// TestTrustUnpin_NotFound verifies unpin on a non-existent pin reports clearly.
func TestTrustUnpin_NotFound(t *testing.T) {
	setupTrustTestEnv(t)

	campfireID := makeFakeCampfireID(t, "test-campfire-unpin-noop")

	out, err := runTrustUnpin(t, campfireID)
	if err != nil {
		t.Fatalf("cf trust unpin (no pin): %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "no pin found") {
		t.Errorf("expected 'no pin found' message, got: %s", out)
	}
}

// TestTrustUnpin_MissingArgs checks cobra rejects invocations with missing arg.
func TestTrustUnpin_MissingArgs(t *testing.T) {
	setupTrustTestEnv(t)

	_, err := runTrustUnpin(t)
	if err == nil {
		t.Fatal("expected error for missing campfire-id arg, got nil")
	}
}

// TestTrustList_Empty verifies `cf trust list` on a fresh env reports nothing.
func TestTrustList_Empty(t *testing.T) {
	setupTrustTestEnv(t)

	out, err := runTrustList(t)
	if err != nil {
		t.Fatalf("cf trust list: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "No key pins") {
		t.Errorf("expected 'No key pins' in output, got: %s", out)
	}
}

// TestTrustList_ShowsPinnedKeys verifies that pinned keys appear in list output.
func TestTrustList_ShowsPinnedKeys(t *testing.T) {
	setupTrustTestEnv(t)

	campfireID1 := makeFakeCampfireID(t, "campfire-list-1")
	campfireID2 := makeFakeCampfireID(t, "campfire-list-2")
	pubKey1 := makeFakePubKey(t)
	pubKey2 := makeFakePubKey(t)

	if _, err := runTrustPin(t, campfireID1, pubKey1); err != nil {
		t.Fatalf("pin 1: %v", err)
	}
	if _, err := runTrustPin(t, campfireID2, pubKey2); err != nil {
		t.Fatalf("pin 2: %v", err)
	}

	out, err := runTrustList(t)
	if err != nil {
		t.Fatalf("cf trust list: %v\nout=%s", err, out)
	}

	// Both campfire short IDs should appear.
	if !strings.Contains(out, campfireID1[:12]) {
		t.Errorf("expected campfire1 %q in list output, got: %s", campfireID1[:12], out)
	}
	if !strings.Contains(out, campfireID2[:12]) {
		t.Errorf("expected campfire2 %q in list output, got: %s", campfireID2[:12], out)
	}
	if !strings.Contains(out, "Key pins (2)") {
		t.Errorf("expected 'Key pins (2)' header, got: %s", out)
	}
}

// TestTrustList_JSON verifies --json emits a valid array with required fields.
func TestTrustList_JSON(t *testing.T) {
	setupTrustTestEnv(t)

	campfireID := makeFakeCampfireID(t, "campfire-list-json")
	pubKey := makeFakePubKey(t)

	if _, err := runTrustPin(t, campfireID, pubKey); err != nil {
		t.Fatalf("pin: %v", err)
	}

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = true
	defer func() { jsonOutput = false }()

	trustPruneCmd.Flags().Set("dry-run", "false") //nolint:errcheck
	rootCmd.SetArgs([]string{"trust", "list", "--json"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	if runErr != nil {
		t.Fatalf("cf trust list --json: %v\nout=%s", runErr, buf.String())
	}

	var parsed []map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("parsing JSON array: %v\nout=%s", err, buf.String())
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 entry in list, got %d: %v", len(parsed), parsed)
	}
	entry := parsed[0]
	for _, key := range []string{"campfire_id", "pubkey", "pinned_at"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("JSON list entry missing key %q", key)
		}
	}
	if entry["campfire_id"] != campfireID {
		t.Errorf("campfire_id = %v, want %s", entry["campfire_id"], campfireID)
	}
	if entry["pubkey"] != pubKey {
		t.Errorf("pubkey = %v, want %s", entry["pubkey"], pubKey)
	}
}

// TestTrustList_EmptyJSON verifies `cf trust list --json` returns an empty array (not null).
func TestTrustList_EmptyJSON(t *testing.T) {
	setupTrustTestEnv(t)

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = true
	defer func() { jsonOutput = false }()

	rootCmd.SetArgs([]string{"trust", "list", "--json"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	if runErr != nil {
		t.Fatalf("cf trust list --json (empty): %v\nout=%s", runErr, buf.String())
	}

	var parsed []interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("parsing JSON: %v\nout=%s", err, buf.String())
	}
	if len(parsed) != 0 {
		t.Errorf("expected empty array, got %d entries", len(parsed))
	}
}

// TestTrustPrune_RemovesStale verifies prune removes pins for campfires not in membership.
func TestTrustPrune_RemovesStale(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	// Add a campfire to the local store membership.
	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	activeCampfireID := makeFakeCampfireID(t, "active-campfire")
	if err := s.AddMembership(store.Membership{
		CampfireID:    activeCampfireID,
		TransportDir:  t.TempDir(),
		JoinProtocol:  "open",
		Role:          "full",
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		s.Close()
		t.Fatalf("adding membership: %v", err)
	}
	s.Close()

	// Pin both the active campfire and a stale (not in membership) one.
	staleCampfireID := makeFakeCampfireID(t, "stale-campfire")
	activePubKey := makeFakePubKey(t)
	stalePubKey := makeFakePubKey(t)

	if _, err := runTrustPin(t, activeCampfireID, activePubKey); err != nil {
		t.Fatalf("pin active: %v", err)
	}
	if _, err := runTrustPin(t, staleCampfireID, stalePubKey); err != nil {
		t.Fatalf("pin stale: %v", err)
	}

	// Prune should remove the stale pin.
	out, err := runTrustPrune(t)
	if err != nil {
		t.Fatalf("cf trust prune: %v\nout=%s", err, out)
	}

	if !strings.Contains(out, "Pruned 1 pin") {
		t.Errorf("expected 'Pruned 1 pin' in output, got: %s", out)
	}

	// Verify: active pin remains, stale pin is gone.
	agentID, _ := loadIdentity()
	kps, _ := trust.NewKeyPinStore(filepath.Join(cfHomeDir, "key-pins.json"), agentID.PrivateKey)

	if kps.GetKeyPin(activeCampfireID) == nil {
		t.Error("active campfire pin should remain after prune")
	}
	if kps.GetKeyPin(staleCampfireID) != nil {
		t.Error("stale campfire pin should be removed after prune")
	}
}

// TestTrustPrune_NoStale verifies prune reports "nothing to prune" when all pins are active.
func TestTrustPrune_NoStale(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	// Add a campfire to local membership.
	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	activeCampfireID := makeFakeCampfireID(t, "active-only-campfire")
	if err := s.AddMembership(store.Membership{
		CampfireID:    activeCampfireID,
		TransportDir:  t.TempDir(),
		JoinProtocol:  "open",
		Role:          "full",
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		s.Close()
		t.Fatalf("adding membership: %v", err)
	}
	s.Close()

	// Pin the active campfire.
	if _, err := runTrustPin(t, activeCampfireID, makeFakePubKey(t)); err != nil {
		t.Fatalf("pin: %v", err)
	}

	out, err := runTrustPrune(t)
	if err != nil {
		t.Fatalf("cf trust prune: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "No pins to prune") {
		t.Errorf("expected 'No pins to prune', got: %s", out)
	}
}

// TestTrustPrune_DryRun verifies --dry-run previews without removing pins.
func TestTrustPrune_DryRun(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	staleCampfireID := makeFakeCampfireID(t, "stale-dry-run")
	if _, err := runTrustPin(t, staleCampfireID, makeFakePubKey(t)); err != nil {
		t.Fatalf("pin stale: %v", err)
	}

	// Dry run — should show what would be pruned but not prune.
	out, err := runTrustPrune(t, "--dry-run")
	if err != nil {
		t.Fatalf("cf trust prune --dry-run: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "Would prune 1") {
		t.Errorf("expected 'Would prune 1' in dry-run output, got: %s", out)
	}

	// Pin should still exist after dry run.
	agentID, _ := loadIdentity()
	kps, _ := trust.NewKeyPinStore(filepath.Join(cfHomeDir, "key-pins.json"), agentID.PrivateKey)
	if kps.GetKeyPin(staleCampfireID) == nil {
		t.Error("pin should NOT be removed after --dry-run")
	}
}

// TestTrustPrune_JSON verifies --json output on prune.
func TestTrustPrune_JSON(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	staleCampfireID := makeFakeCampfireID(t, "stale-json")
	if _, err := runTrustPin(t, staleCampfireID, makeFakePubKey(t)); err != nil {
		t.Fatalf("pin stale: %v", err)
	}

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	jsonOutput = true
	defer func() { jsonOutput = false }()
	trustPruneCmd.Flags().Set("dry-run", "false") //nolint:errcheck

	rootCmd.SetArgs([]string{"trust", "prune", "--json"})
	runErr := rootCmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	buf.ReadFrom(r) //nolint:errcheck
	r.Close()

	if runErr != nil {
		t.Fatalf("cf trust prune --json: %v\nout=%s", runErr, buf.String())
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("parsing JSON: %v\nout=%s", err, buf.String())
	}
	for _, key := range []string{"pruned", "count", "remaining"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("JSON output missing key %q", key)
		}
	}
	if int(parsed["count"].(float64)) != 1 {
		t.Errorf("count = %v, want 1", parsed["count"])
	}
	if int(parsed["remaining"].(float64)) != 0 {
		t.Errorf("remaining = %v, want 0", parsed["remaining"])
	}

	_ = cfHomeDir // referenced above
}

// TestTrustPrune_DisbandedCampfire simulates the primary use case: pruning a pin
// whose campfire has been disbanded (removed from local membership).
func TestTrustPrune_DisbandedCampfire(t *testing.T) {
	cfHomeDir := setupTrustTestEnv(t)

	disbandedID := makeFakeCampfireID(t, "disbanded-campfire")
	activeID := makeFakeCampfireID(t, "still-active-campfire")

	// Add both to membership initially.
	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	for _, cid := range []string{disbandedID, activeID} {
		if err := s.AddMembership(store.Membership{
			CampfireID:    cid,
			TransportDir:  t.TempDir(),
			JoinProtocol:  "open",
			Role:          "full",
			JoinedAt:      time.Now().UnixNano(),
			Threshold:     1,
			TransportType: "filesystem",
		}); err != nil {
			s.Close()
			t.Fatalf("adding membership for %s: %v", cid, err)
		}
	}

	// Pin both.
	if _, err := runTrustPin(t, disbandedID, makeFakePubKey(t)); err != nil {
		t.Fatalf("pin disbanded: %v", err)
	}
	if _, err := runTrustPin(t, activeID, makeFakePubKey(t)); err != nil {
		t.Fatalf("pin active: %v", err)
	}

	// Simulate disband: remove the disbanded campfire from membership.
	if err := s.RemoveMembership(disbandedID); err != nil {
		s.Close()
		t.Fatalf("removing membership: %v", err)
	}
	s.Close()

	// Prune: disbanded campfire's pin should be removed.
	out, err := runTrustPrune(t)
	if err != nil {
		t.Fatalf("cf trust prune: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "Pruned 1 pin") {
		t.Errorf("expected 'Pruned 1 pin', got: %s", out)
	}

	agentID, _ := loadIdentity()
	kps, _ := trust.NewKeyPinStore(filepath.Join(cfHomeDir, "key-pins.json"), agentID.PrivateKey)
	if kps.GetKeyPin(disbandedID) != nil {
		t.Error("disbanded campfire pin should be pruned")
	}
	if kps.GetKeyPin(activeID) == nil {
		t.Error("active campfire pin should remain")
	}
}
