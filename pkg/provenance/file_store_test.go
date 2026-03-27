package provenance

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileStore_PersistenceAcrossRestart is the regression test for
// campfire-agent-de2: attestations must survive process restarts.
//
// Before the fix, loadProvenanceStore() created a fresh in-memory store each
// CLI invocation, so verified attestations were lost when the process exited.
// This test simulates a restart by closing the first FileStore, creating a new
// one pointed at the same path, and verifying the attestation is still there.
func TestFileStore_PersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "attestations.json")

	verifierKey := "verifier-key-abc"
	targetKey := "target-key-xyz"

	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys[verifierKey] = 0

	// --- First "process invocation" ---
	fs1, err := NewFileStore(path, cfg)
	if err != nil {
		t.Fatalf("NewFileStore (first open) failed: %v", err)
	}

	freshTime := time.Now().Add(-1 * time.Hour)
	a := &Attestation{
		ID:            "att-persist-001",
		TargetKey:     targetKey,
		VerifierKey:   verifierKey,
		Nonce:         "nonce-abc",
		ContactMethod: "cf://test",
		ProofType:     ProofEmailLink,
		VerifiedAt:    freshTime,
		CoSigned:      true,
	}
	if err := fs1.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}

	// Verify state in first invocation.
	if l := fs1.Level(targetKey); l < LevelContactable {
		t.Fatalf("expected at least LevelContactable in first invocation, got %v", l)
	}

	// The file must exist on disk now.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Fatal("attestations.json was not created on disk")
	}

	// --- Simulate process restart: discard fs1, open a new FileStore ---
	// fs1 goes out of scope here (no Close method — GC handles it).
	fs2, err := NewFileStore(path, cfg)
	if err != nil {
		t.Fatalf("NewFileStore (second open / restart) failed: %v", err)
	}

	// The attestation must still be retrievable.
	atts := fs2.Attestations(targetKey)
	if len(atts) == 0 {
		t.Fatal("attestation not found after simulated process restart — persistence is broken")
	}
	if atts[0].ID != "att-persist-001" {
		t.Errorf("wrong attestation ID: got %q, want %q", atts[0].ID, "att-persist-001")
	}

	// Level must be correct after reload.
	if l := fs2.Level(targetKey); l < LevelContactable {
		t.Errorf("expected at least LevelContactable after reload, got %v", l)
	}
}

// TestFileStore_RevokedPersistedAcrossRestart verifies that revocations also survive restarts.
func TestFileStore_RevokedPersistedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "attestations.json")

	verifierKey := "verifier-key-rev"
	targetKey := "target-key-rev"

	cfg := DefaultConfig()
	cfg.TrustedVerifierKeys[verifierKey] = 0

	// First invocation: add and then revoke an attestation.
	fs1, err := NewFileStore(path, cfg)
	if err != nil {
		t.Fatalf("NewFileStore failed: %v", err)
	}

	a := &Attestation{
		ID:          "att-revoke-001",
		TargetKey:   targetKey,
		VerifierKey: verifierKey,
		Nonce:       "nonce-rev",
		VerifiedAt:  time.Now().Add(-30 * time.Minute),
		CoSigned:    true,
	}
	if err := fs1.AddAttestation(a); err != nil {
		t.Fatalf("AddAttestation failed: %v", err)
	}
	if err := fs1.Revoke("att-revoke-001"); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	// After revoke, level should be anonymous.
	if l := fs1.Level(targetKey); l >= LevelContactable {
		t.Fatalf("expected LevelAnonymous after revoke in first invocation, got %v", l)
	}

	// Second invocation: revocation must be persisted.
	fs2, err := NewFileStore(path, cfg)
	if err != nil {
		t.Fatalf("NewFileStore (restart) failed: %v", err)
	}

	if l := fs2.Level(targetKey); l >= LevelContactable {
		t.Errorf("expected LevelAnonymous after restart (revoked attestation), got %v", l)
	}

	// Revoking a nonexistent ID should return ErrAttestationNotFound.
	if err := fs2.Revoke("att-nonexistent"); !errors.Is(err, ErrAttestationNotFound) {
		t.Errorf("expected ErrAttestationNotFound for unknown attestation ID, got %v", err)
	}
}

// TestFileStore_SelfClaimedPersistedAcrossRestart verifies SetSelfClaimed survives restarts.
func TestFileStore_SelfClaimedPersistedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "attestations.json")

	targetKey := "target-key-claimed"

	// First invocation: mark as self-claimed.
	fs1, err := NewFileStore(path, DefaultConfig())
	if err != nil {
		t.Fatalf("NewFileStore failed: %v", err)
	}
	if err := fs1.SetSelfClaimed(targetKey); err != nil {
		t.Fatalf("SetSelfClaimed failed: %v", err)
	}
	if l := fs1.Level(targetKey); l != LevelClaimed {
		t.Fatalf("expected LevelClaimed in first invocation, got %v", l)
	}

	// Second invocation: must still be level 1.
	fs2, err := NewFileStore(path, DefaultConfig())
	if err != nil {
		t.Fatalf("NewFileStore (restart) failed: %v", err)
	}
	if l := fs2.Level(targetKey); l != LevelClaimed {
		t.Errorf("expected LevelClaimed after restart, got %v", l)
	}
}

// TestFileStore_EmptyFile creates a FileStore for a non-existent path —
// should succeed with an empty store (no pre-existing state).
func TestFileStore_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	fs, err := NewFileStore(path, DefaultConfig())
	if err != nil {
		t.Fatalf("NewFileStore on non-existent file should succeed, got: %v", err)
	}

	if l := fs.Level("any-key"); l != LevelAnonymous {
		t.Errorf("expected LevelAnonymous for empty store, got %v", l)
	}
}

// TestFileStore_ImplementsInterface verifies FileStore satisfies AttestationStore.
// This is a compile-time check that will fail if the interface drifts.
func TestFileStore_ImplementsInterface(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "attestations.json")

	fs, err := NewFileStore(path, DefaultConfig())
	if err != nil {
		t.Fatalf("NewFileStore failed: %v", err)
	}

	var _ AttestationStore = fs // compile-time interface check
}
