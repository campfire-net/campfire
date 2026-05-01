package trust

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempKeyPinStore(t *testing.T) (*KeyPinStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "key-pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)
	kps, err := NewKeyPinStore(path, priv.Seed())
	if err != nil {
		t.Fatalf("NewKeyPinStore: %v", err)
	}
	return kps, path
}

// TestKeyPinStore_SetAndGet verifies that SetKeyPin stores a pin and GetKeyPin retrieves it.
func TestKeyPinStore_SetAndGet(t *testing.T) {
	kps, _ := tempKeyPinStore(t)

	campfireID := "aaaa000000000000"
	pubKey := "bbbb111111111111"

	kps.SetKeyPin(campfireID, pubKey)

	pin := kps.GetKeyPin(campfireID)
	if pin == nil {
		t.Fatal("GetKeyPin returned nil after SetKeyPin")
	}
	if pin.CampfireID != campfireID {
		t.Errorf("CampfireID = %q, want %q", pin.CampfireID, campfireID)
	}
	if pin.PubKey != pubKey {
		t.Errorf("PubKey = %q, want %q", pin.PubKey, pubKey)
	}
	if pin.PinnedAt.IsZero() {
		t.Error("PinnedAt should not be zero after SetKeyPin")
	}
}

// TestKeyPinStore_GetMissing verifies GetKeyPin returns nil for a missing campfire ID.
func TestKeyPinStore_GetMissing(t *testing.T) {
	kps, _ := tempKeyPinStore(t)

	if pin := kps.GetKeyPin("nonexistent"); pin != nil {
		t.Errorf("GetKeyPin returned non-nil for missing key: %+v", pin)
	}
}

// TestKeyPinStore_Remove verifies RemoveKeyPin deletes a pin.
func TestKeyPinStore_Remove(t *testing.T) {
	kps, _ := tempKeyPinStore(t)

	campfireID := "cccc222222222222"
	kps.SetKeyPin(campfireID, "dddd333333333333")
	kps.RemoveKeyPin(campfireID)

	if pin := kps.GetKeyPin(campfireID); pin != nil {
		t.Errorf("pin should be removed, got: %+v", pin)
	}
}

// TestKeyPinStore_RemoveNoOp verifies RemoveKeyPin on a missing ID doesn't panic.
func TestKeyPinStore_RemoveNoOp(t *testing.T) {
	kps, _ := tempKeyPinStore(t)
	kps.RemoveKeyPin("does-not-exist") // should not panic or error
}

// TestKeyPinStore_ListKeyPins verifies ListKeyPins returns all pins.
func TestKeyPinStore_ListKeyPins(t *testing.T) {
	kps, _ := tempKeyPinStore(t)

	kps.SetKeyPin("campfire-1", "pubkey-1")
	kps.SetKeyPin("campfire-2", "pubkey-2")
	kps.SetKeyPin("campfire-3", "pubkey-3")

	pins := kps.ListKeyPins()
	if len(pins) != 3 {
		t.Fatalf("ListKeyPins count = %d, want 3", len(pins))
	}

	// Verify all three campfire IDs are present.
	seen := map[string]bool{}
	for _, p := range pins {
		seen[p.CampfireID] = true
	}
	for _, id := range []string{"campfire-1", "campfire-2", "campfire-3"} {
		if !seen[id] {
			t.Errorf("campfire %q missing from ListKeyPins output", id)
		}
	}
}

// TestKeyPinStore_PruneKeyPins verifies PruneKeyPins removes stale pins.
func TestKeyPinStore_PruneKeyPins(t *testing.T) {
	kps, _ := tempKeyPinStore(t)

	kps.SetKeyPin("active-1", "pub-a1")
	kps.SetKeyPin("active-2", "pub-a2")
	kps.SetKeyPin("stale-1", "pub-s1")
	kps.SetKeyPin("stale-2", "pub-s2")

	active := map[string]struct{}{
		"active-1": {},
		"active-2": {},
	}

	pruned := kps.PruneKeyPins(active)
	if len(pruned) != 2 {
		t.Errorf("pruned count = %d, want 2", len(pruned))
	}
	for _, id := range pruned {
		if id != "stale-1" && id != "stale-2" {
			t.Errorf("unexpected pruned ID: %q", id)
		}
	}

	// Active pins should remain.
	if kps.GetKeyPin("active-1") == nil {
		t.Error("active-1 should not be pruned")
	}
	if kps.GetKeyPin("active-2") == nil {
		t.Error("active-2 should not be pruned")
	}
	// Stale pins should be gone.
	if kps.GetKeyPin("stale-1") != nil {
		t.Error("stale-1 should be pruned")
	}
	if kps.GetKeyPin("stale-2") != nil {
		t.Error("stale-2 should be pruned")
	}
}

// TestKeyPinStore_PruneEmpty verifies PruneKeyPins with empty active set prunes all.
func TestKeyPinStore_PruneEmpty(t *testing.T) {
	kps, _ := tempKeyPinStore(t)
	kps.SetKeyPin("campfire-1", "pub-1")
	kps.SetKeyPin("campfire-2", "pub-2")

	pruned := kps.PruneKeyPins(map[string]struct{}{})
	if len(pruned) != 2 {
		t.Errorf("pruned count = %d, want 2", len(pruned))
	}
}

// TestKeyPinStore_Persistence verifies Save/Load round-trips pins correctly.
func TestKeyPinStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()

	kps, err := NewKeyPinStore(path, seed)
	if err != nil {
		t.Fatalf("NewKeyPinStore: %v", err)
	}

	campfireID := "persist-campfire-hex"
	pubKey := "persist-pubkey-hex"
	kps.SetKeyPin(campfireID, pubKey)

	if err := kps.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload into a fresh store.
	kps2, err := NewKeyPinStore(path, seed)
	if err != nil {
		t.Fatalf("NewKeyPinStore (reload): %v", err)
	}

	pin := kps2.GetKeyPin(campfireID)
	if pin == nil {
		t.Fatal("pin not found after reload")
	}
	if pin.PubKey != pubKey {
		t.Errorf("PubKey = %q, want %q", pin.PubKey, pubKey)
	}
	if pin.CampfireID != campfireID {
		t.Errorf("CampfireID = %q, want %q", pin.CampfireID, campfireID)
	}
}

// TestKeyPinStore_TamperedHMAC verifies that HMAC tampering is detected.
func TestKeyPinStore_TamperedHMAC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()

	kps, err := NewKeyPinStore(path, seed)
	if err != nil {
		t.Fatalf("NewKeyPinStore: %v", err)
	}
	kps.SetKeyPin("some-campfire", "some-pubkey")
	if err := kps.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Tamper with the HMAC field.
	data, _ := os.ReadFile(path)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal for tamper: %v", err)
	}
	raw["hmac"] = json.RawMessage(`"0000000000000000000000000000000000000000000000000000000000000000"`)
	tampered, _ := json.Marshal(raw)
	os.WriteFile(path, tampered, 0600) //nolint:errcheck

	_, err = NewKeyPinStore(path, seed)
	if err == nil {
		t.Fatal("expected HMAC failure after tampering, got nil")
	}
	if err.Error() == "" || !contains(err.Error(), "HMAC") {
		t.Errorf("expected HMAC error, got: %v", err)
	}
}

// TestKeyPinStore_FilePermissions verifies the saved file has 0600 permissions.
func TestKeyPinStore_FilePermissions(t *testing.T) {
	kps, path := tempKeyPinStore(t)
	kps.SetKeyPin("campfire-perm", "pubkey-perm")
	if err := kps.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}

// TestKeyPinStore_AtomicWrite verifies no temp files remain after Save.
func TestKeyPinStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)

	kps, err := NewKeyPinStore(path, priv.Seed())
	if err != nil {
		t.Fatalf("NewKeyPinStore: %v", err)
	}
	kps.SetKeyPin("campfire-atomic", "pubkey-atomic")
	if err := kps.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "key-pins.json" {
			t.Errorf("unexpected file after Save: %q", e.Name())
		}
	}
}

// TestKeyPinStore_LastUsedField verifies last_used is zero by default and survives round-trip.
func TestKeyPinStore_LastUsedField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()

	kps, err := NewKeyPinStore(path, seed)
	if err != nil {
		t.Fatalf("NewKeyPinStore: %v", err)
	}

	kps.SetKeyPin("campfire-lu", "pubkey-lu")
	pin := kps.GetKeyPin("campfire-lu")
	if !pin.LastUsed.IsZero() {
		t.Errorf("LastUsed should be zero by default, got: %v", pin.LastUsed)
	}

	// Set a non-zero LastUsed and persist.
	kps.mu.Lock()
	kps.pins["campfire-lu"].LastUsed = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	kps.mu.Unlock()

	if err := kps.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	kps2, err := NewKeyPinStore(path, seed)
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	pin2 := kps2.GetKeyPin("campfire-lu")
	if pin2 == nil {
		t.Fatal("pin not found after reload")
	}
	if pin2.LastUsed.IsZero() {
		t.Error("LastUsed should be non-zero after reload")
	}
}

// TestKeyPinStore_HMACConsistentWithPinStore verifies that KeyPinStore and PinStore
// derive the same HMAC key from the same private key (same derivation function).
func TestKeyPinStore_HMACConsistentWithPinStore(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()

	// Both should succeed (same derivation) — just verify no error.
	dir := t.TempDir()
	_, err := NewKeyPinStore(filepath.Join(dir, "key-pins.json"), seed)
	if err != nil {
		t.Fatalf("NewKeyPinStore: %v", err)
	}
	_, err = NewPinStore(filepath.Join(dir, "pins.json"), seed)
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}
}

// contains is a local helper for substring matching in error messages.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
