package trust

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempPinStore(t *testing.T) (*PinStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)
	ps, err := NewPinStore(path, priv.Seed())
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}
	return ps, path
}

func TestPinStore_NewPin(t *testing.T) {
	ps, _ := tempPinStore(t)

	action, err := ps.CheckPin("campfire-1", "trust", "verify", []byte("payload"), "abc123", SignerCampfireKey)
	if err != nil {
		t.Fatalf("CheckPin: %v", err)
	}
	if action != PinNew {
		t.Errorf("action = %s, want %s", action, PinNew)
	}
}

func TestPinStore_SameContentNoChange(t *testing.T) {
	ps, _ := tempPinStore(t)

	payload := []byte("declaration-payload")
	signerKey := "abcdef0123456789"

	ps.SetPin("campfire-1", "trust", "verify", &Pin{
		ContentHash: sha256Hex(payload),
		SignerKey:   signerKey,
		SignerType:  SignerCampfireKey,
		TrustStatus: TrustAdopted,
		PinnedAt:   time.Now(),
	})

	action, err := ps.CheckPin("campfire-1", "trust", "verify", payload, signerKey, SignerCampfireKey)
	if err != nil {
		t.Fatalf("CheckPin: %v", err)
	}
	if action != PinAccept {
		t.Errorf("action = %s, want %s", action, PinAccept)
	}
}

func TestPinStore_HigherAuthorityReplaces(t *testing.T) {
	ps, _ := tempPinStore(t)

	// Existing pin from campfire key (authority 2).
	ps.SetPin("campfire-1", "trust", "verify", &Pin{
		ContentHash: sha256Hex([]byte("old-payload")),
		SignerKey:   "old-signer",
		SignerType:  SignerCampfireKey,
		TrustStatus: TrustAdopted,
		PinnedAt:   time.Now(),
	})

	// New declaration from convention registry (authority 3) — should accept.
	action, err := ps.CheckPin("campfire-1", "trust", "verify", []byte("new-payload"), "new-signer", SignerConventionRegistry)
	if err != nil {
		t.Fatalf("CheckPin: %v", err)
	}
	if action != PinAccept {
		t.Errorf("action = %s, want %s", action, PinAccept)
	}
}

func TestPinStore_LowerAuthorityRejected(t *testing.T) {
	ps, _ := tempPinStore(t)

	// Existing pin from campfire key (authority 2).
	ps.SetPin("campfire-1", "trust", "verify", &Pin{
		ContentHash: sha256Hex([]byte("original-payload")),
		SignerKey:   "campfire-signer",
		SignerType:  SignerCampfireKey,
		TrustStatus: TrustAdopted,
		PinnedAt:   time.Now(),
	})

	// Member key (authority 1) tries to replace — should reject.
	action, err := ps.CheckPin("campfire-1", "trust", "verify", []byte("rogue-payload"), "member-signer", SignerMemberKey)
	if err != nil {
		t.Fatalf("CheckPin: %v", err)
	}
	if action != PinReject {
		t.Errorf("action = %s, want %s", action, PinReject)
	}
}

func TestPinStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)
	privSeed := priv.Seed()

	ps, err := NewPinStore(path, privSeed)
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}

	now := time.Now().Truncate(time.Millisecond) // truncate for JSON round-trip
	ps.SetPin("campfire-1", "trust", "verify", &Pin{
		ContentHash: "abc123",
		SignerKey:   "signer-key-hex",
		SignerType:  SignerConventionRegistry,
		TrustStatus: TrustAdopted,
		PinnedAt:   now,
	})

	if err := ps.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load into a new PinStore.
	ps2, err := NewPinStore(path, privSeed)
	if err != nil {
		t.Fatalf("NewPinStore (reload): %v", err)
	}

	pins := ps2.ListPins()
	pin, ok := pins["campfire-1:trust:verify"]
	if !ok {
		t.Fatal("pin not found after reload")
	}
	if pin.ContentHash != "abc123" {
		t.Errorf("ContentHash = %s, want abc123", pin.ContentHash)
	}
	if pin.SignerKey != "signer-key-hex" {
		t.Errorf("SignerKey = %s, want signer-key-hex", pin.SignerKey)
	}
	if pin.SignerType != SignerConventionRegistry {
		t.Errorf("SignerType = %s, want %s", pin.SignerType, SignerConventionRegistry)
	}
}

func TestPinStore_TamperedHMAC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)
	privSeed := priv.Seed()

	ps, err := NewPinStore(path, privSeed)
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}

	ps.SetPin("campfire-1", "trust", "verify", &Pin{
		ContentHash: "abc123",
		SignerKey:   "signer-key-hex",
		SignerType:  SignerCampfireKey,
		TrustStatus: TrustAdopted,
		PinnedAt:   time.Now(),
	})

	if err := ps.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Tamper with the file — modify the HMAC.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var pf map[string]json.RawMessage
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pf["hmac"] = json.RawMessage(`"0000000000000000000000000000000000000000000000000000000000000000"`)
	tampered, _ := json.Marshal(pf)
	if err := os.WriteFile(path, tampered, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Load should fail.
	_, err = NewPinStore(path, privSeed)
	if err == nil {
		t.Fatal("expected HMAC verification failure, got nil error")
	}
}

func TestPinStore_ClearScoped(t *testing.T) {
	ps, _ := tempPinStore(t)

	now := time.Now()
	ps.SetPin("campfire-1", "trust", "verify", &Pin{ContentHash: "a", PinnedAt: now})
	ps.SetPin("campfire-1", "naming", "resolve", &Pin{ContentHash: "b", PinnedAt: now})
	ps.SetPin("campfire-2", "trust", "verify", &Pin{ContentHash: "c", PinnedAt: now})
	ps.SetPin("campfire-2", "naming", "resolve", &Pin{ContentHash: "d", PinnedAt: now})

	// Clear by campfire.
	ps.ClearPins(PinScope{CampfireID: "campfire-1"})
	pins := ps.ListPins()
	if len(pins) != 2 {
		t.Errorf("after clear campfire-1: pin count = %d, want 2", len(pins))
	}
	if _, ok := pins["campfire-1:trust:verify"]; ok {
		t.Error("campfire-1:trust:verify should be cleared")
	}
	if _, ok := pins["campfire-2:trust:verify"]; !ok {
		t.Error("campfire-2:trust:verify should remain")
	}

	// Reset and test clear by convention.
	ps.SetPin("campfire-1", "trust", "verify", &Pin{ContentHash: "a", PinnedAt: now})
	ps.SetPin("campfire-1", "naming", "resolve", &Pin{ContentHash: "b", PinnedAt: now})

	ps.ClearPins(PinScope{Convention: "trust"})
	pins = ps.ListPins()
	for key := range pins {
		if keyMatchesConvention(key, "trust") {
			t.Errorf("pin %s should be cleared (convention=trust)", key)
		}
	}

	// Test clear all.
	ps.SetPin("campfire-1", "trust", "verify", &Pin{ContentHash: "a", PinnedAt: now})
	ps.ClearPins(PinScope{All: true})
	pins = ps.ListPins()
	if len(pins) != 0 {
		t.Errorf("after clear all: pin count = %d, want 0", len(pins))
	}
}

func TestPinStore_FilePermissions(t *testing.T) {
	ps, path := tempPinStore(t)

	ps.SetPin("campfire-1", "trust", "verify", &Pin{
		ContentHash: "abc",
		PinnedAt:   time.Now(),
	})

	if err := ps.Save(); err != nil {
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

// TestPinStore_AtomicWrite verifies that Save() writes to a temp file and
// renames, so no temp file is left behind on success.
func TestPinStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)

	ps, err := NewPinStore(path, priv.Seed())
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}

	ps.SetPin("campfire-1", "trust", "verify", &Pin{
		ContentHash: "hash-abc",
		SignerKey:   "key-abc",
		SignerType:  SignerCampfireKey,
		TrustStatus: TrustAdopted,
		PinnedAt:   time.Now(),
	})

	if err := ps.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The target file must exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("target file missing after Save: %v", err)
	}

	// No temp files (.pins-*.tmp) should remain in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "pins.json" {
			t.Errorf("unexpected file left in dir after Save: %s", e.Name())
		}
	}
}

// TestPinStore_HMACMatchesStoredBytes verifies that the HMAC covers the exact
// bytes stored in the file (no TOCTOU between HMAC computation and write).
// A file hand-crafted with mismatched HMAC must be rejected; a valid file must
// round-trip correctly.
func TestPinStore_HMACMatchesStoredBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	_, priv, _ := ed25519.GenerateKey(nil)
	privSeed := priv.Seed()

	ps, err := NewPinStore(path, privSeed)
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}

	now := time.Now().Truncate(time.Millisecond)
	ps.SetPin("campfire-1", "trust", "verify", &Pin{
		ContentHash: "deadbeef",
		SignerKey:   "pubkey-hex",
		SignerType:  SignerConventionRegistry,
		TrustStatus: TrustAdopted,
		PinnedAt:   now,
	})

	if err := ps.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload and verify round-trip.
	ps2, err := NewPinStore(path, privSeed)
	if err != nil {
		t.Fatalf("NewPinStore reload: %v", err)
	}
	pins := ps2.ListPins()
	if len(pins) != 1 {
		t.Fatalf("expected 1 pin, got %d", len(pins))
	}
	pin, ok := pins["campfire-1:trust:verify"]
	if !ok {
		t.Fatal("pin not found after reload")
	}
	if pin.ContentHash != "deadbeef" {
		t.Errorf("ContentHash = %s, want deadbeef", pin.ContentHash)
	}

	// Tamper with the raw "pins" bytes in the stored file — HMAC must reject it.
	data, _ := os.ReadFile(path)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal for tamper: %v", err)
	}
	// Replace the pins value with slightly different content.
	raw["pins"] = json.RawMessage(`{}`)
	tampered, _ := json.Marshal(raw)
	_ = os.WriteFile(path, tampered, 0600)

	_, err = NewPinStore(path, privSeed)
	if err == nil {
		t.Fatal("expected HMAC failure after tampering with stored pins bytes, got nil")
	}
}
