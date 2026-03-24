package store

import (
	"path/filepath"
	"testing"
	"time"
)

// openTestStore opens a fresh in-memory SQLite store for testing.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// addTestMembership adds a minimal campfire membership for testing.
func addTestMembership(t *testing.T, s *Store, campfireID string) {
	t.Helper()
	err := s.AddMembership(Membership{
		CampfireID:   campfireID,
		TransportDir: t.TempDir(),
		JoinProtocol: "open",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	})
	if err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
}

// TestMigration6_EpochSecretsTable verifies that migration 6 creates the
// campfire_epoch_secrets table and that UpsertEpochSecret / GetEpochSecret work.
func TestMigration6_EpochSecretsTable(t *testing.T) {
	s := openTestStore(t)
	addTestMembership(t, s, "cf-encrypt-1")

	rootSecret := make([]byte, 32)
	for i := range rootSecret {
		rootSecret[i] = byte(i)
	}
	cek := make([]byte, 32)
	for i := range cek {
		cek[i] = byte(i + 100)
	}

	secret := EpochSecret{
		CampfireID: "cf-encrypt-1",
		Epoch:      0,
		RootSecret: rootSecret,
		CEK:        cek,
		CreatedAt:  time.Now().UnixNano(),
	}

	if err := s.UpsertEpochSecret(secret); err != nil {
		t.Fatalf("UpsertEpochSecret: %v", err)
	}

	got, err := s.GetEpochSecret("cf-encrypt-1", 0)
	if err != nil {
		t.Fatalf("GetEpochSecret: %v", err)
	}
	if got == nil {
		t.Fatal("GetEpochSecret returned nil, want non-nil")
	}
	if got.Epoch != 0 {
		t.Errorf("epoch = %d, want 0", got.Epoch)
	}
	if string(got.RootSecret) != string(rootSecret) {
		t.Error("root secret mismatch")
	}
	if string(got.CEK) != string(cek) {
		t.Error("CEK mismatch")
	}
}

// TestGetEpochSecret_NotFound verifies nil is returned when no epoch secret exists.
func TestGetEpochSecret_NotFound(t *testing.T) {
	s := openTestStore(t)
	addTestMembership(t, s, "cf-not-found")

	got, err := s.GetEpochSecret("cf-not-found", 99)
	if err != nil {
		t.Fatalf("GetEpochSecret: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing epoch secret, got %+v", got)
	}
}

// TestGetLatestEpochSecret_MultipleEpochs verifies that GetLatestEpochSecret
// returns the highest epoch when multiple epochs are stored.
func TestGetLatestEpochSecret_MultipleEpochs(t *testing.T) {
	s := openTestStore(t)
	addTestMembership(t, s, "cf-multi")

	for epoch := uint64(0); epoch <= 3; epoch++ {
		root := make([]byte, 32)
		root[0] = byte(epoch)
		cek := make([]byte, 32)
		cek[0] = byte(epoch + 10)
		err := s.UpsertEpochSecret(EpochSecret{
			CampfireID: "cf-multi",
			Epoch:      epoch,
			RootSecret: root,
			CEK:        cek,
			CreatedAt:  time.Now().UnixNano(),
		})
		if err != nil {
			t.Fatalf("UpsertEpochSecret(epoch=%d): %v", epoch, err)
		}
	}

	latest, err := s.GetLatestEpochSecret("cf-multi")
	if err != nil {
		t.Fatalf("GetLatestEpochSecret: %v", err)
	}
	if latest == nil {
		t.Fatal("GetLatestEpochSecret returned nil")
	}
	if latest.Epoch != 3 {
		t.Errorf("latest epoch = %d, want 3", latest.Epoch)
	}
	if latest.RootSecret[0] != 3 {
		t.Errorf("latest root secret[0] = %d, want 3", latest.RootSecret[0])
	}
}

// TestUpsertEpochSecret_Idempotent verifies that upserting an existing epoch
// updates the value rather than erroring.
func TestUpsertEpochSecret_Idempotent(t *testing.T) {
	s := openTestStore(t)
	addTestMembership(t, s, "cf-upsert")

	root1 := make([]byte, 32)
	root1[0] = 1
	cek1 := make([]byte, 32)
	cek1[0] = 10

	err := s.UpsertEpochSecret(EpochSecret{
		CampfireID: "cf-upsert",
		Epoch:      0,
		RootSecret: root1,
		CEK:        cek1,
		CreatedAt:  1000,
	})
	if err != nil {
		t.Fatalf("UpsertEpochSecret first call: %v", err)
	}

	// Upsert same epoch with different values
	root2 := make([]byte, 32)
	root2[0] = 2
	cek2 := make([]byte, 32)
	cek2[0] = 20
	err = s.UpsertEpochSecret(EpochSecret{
		CampfireID: "cf-upsert",
		Epoch:      0,
		RootSecret: root2,
		CEK:        cek2,
		CreatedAt:  2000,
	})
	if err != nil {
		t.Fatalf("UpsertEpochSecret second call (upsert): %v", err)
	}

	got, err := s.GetEpochSecret("cf-upsert", 0)
	if err != nil {
		t.Fatalf("GetEpochSecret: %v", err)
	}
	if got.RootSecret[0] != 2 {
		t.Errorf("after upsert, root[0] = %d, want 2", got.RootSecret[0])
	}
}

// TestMigration7_EncryptedColumn verifies that migration 7 adds the encrypted
// column and that AddMembership/GetMembership handle it correctly.
func TestMigration7_EncryptedColumn(t *testing.T) {
	s := openTestStore(t)

	// Add unencrypted membership
	err := s.AddMembership(Membership{
		CampfireID:   "cf-plain",
		TransportDir: t.TempDir(),
		JoinProtocol: "open",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
		Encrypted:    false,
	})
	if err != nil {
		t.Fatalf("AddMembership(plain): %v", err)
	}

	// Add encrypted membership
	err = s.AddMembership(Membership{
		CampfireID:   "cf-encrypted",
		TransportDir: t.TempDir(),
		JoinProtocol: "open",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
		Encrypted:    true,
	})
	if err != nil {
		t.Fatalf("AddMembership(encrypted): %v", err)
	}

	plain, err := s.GetMembership("cf-plain")
	if err != nil || plain == nil {
		t.Fatalf("GetMembership(plain): %v, got %v", err, plain)
	}
	if plain.Encrypted {
		t.Error("plain campfire should have Encrypted=false")
	}

	enc, err := s.GetMembership("cf-encrypted")
	if err != nil || enc == nil {
		t.Fatalf("GetMembership(encrypted): %v, got %v", err, enc)
	}
	if !enc.Encrypted {
		t.Error("encrypted campfire should have Encrypted=true")
	}
}

// TestSetMembershipEncrypted_DowngradePrevention verifies that SetMembershipEncrypted
// persists the encrypted flag for downgrade prevention (spec §2.1).
func TestSetMembershipEncrypted_DowngradePrevention(t *testing.T) {
	s := openTestStore(t)
	addTestMembership(t, s, "cf-downgrade")

	// Initially not encrypted
	m, err := s.GetMembership("cf-downgrade")
	if err != nil || m == nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m.Encrypted {
		t.Error("newly added membership should not be encrypted by default")
	}

	// Set encrypted flag
	if err := s.SetMembershipEncrypted("cf-downgrade", true); err != nil {
		t.Fatalf("SetMembershipEncrypted(true): %v", err)
	}

	m, err = s.GetMembership("cf-downgrade")
	if err != nil || m == nil {
		t.Fatalf("GetMembership after set: %v", err)
	}
	if !m.Encrypted {
		t.Error("encrypted flag should be true after SetMembershipEncrypted(true)")
	}

	// Verify the flag persists in ListMemberships too
	memberships, err := s.ListMemberships()
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	found := false
	for _, ms := range memberships {
		if ms.CampfireID == "cf-downgrade" {
			found = true
			if !ms.Encrypted {
				t.Error("encrypted flag not persisted in ListMemberships")
			}
		}
	}
	if !found {
		t.Error("membership not found in ListMemberships")
	}
}

// TestSetMembershipEncrypted_NotFound verifies an error is returned for unknown campfire.
func TestSetMembershipEncrypted_NotFound(t *testing.T) {
	s := openTestStore(t)
	err := s.SetMembershipEncrypted("nonexistent", true)
	if err == nil {
		t.Error("SetMembershipEncrypted for nonexistent campfire should return error")
	}
}

// TestUpdateCampfireID_MigratesEpochSecrets verifies that UpdateCampfireID
// migrates epoch secrets to the new campfire ID (spec §3.6 CRITICAL requirement).
// Missing this migration causes silent decryption failure after a campfire rekey.
func TestUpdateCampfireID_MigratesEpochSecrets(t *testing.T) {
	s := openTestStore(t)

	oldID := "cf-old-id"
	newID := "cf-new-id"
	addTestMembership(t, s, oldID)

	// Store epoch secrets under oldID
	for epoch := uint64(0); epoch <= 2; epoch++ {
		root := make([]byte, 32)
		root[0] = byte(epoch)
		cek := make([]byte, 32)
		cek[0] = byte(epoch + 50)
		err := s.UpsertEpochSecret(EpochSecret{
			CampfireID: oldID,
			Epoch:      epoch,
			RootSecret: root,
			CEK:        cek,
			CreatedAt:  time.Now().UnixNano(),
		})
		if err != nil {
			t.Fatalf("UpsertEpochSecret(epoch=%d): %v", epoch, err)
		}
	}

	// Perform campfire ID rotation (simulates handler_rekey.go flow)
	if err := s.UpdateCampfireID(oldID, newID); err != nil {
		t.Fatalf("UpdateCampfireID: %v", err)
	}

	// Epoch secrets should now be accessible under newID
	for epoch := uint64(0); epoch <= 2; epoch++ {
		got, err := s.GetEpochSecret(newID, epoch)
		if err != nil {
			t.Fatalf("GetEpochSecret(newID, %d): %v", epoch, err)
		}
		if got == nil {
			t.Errorf("epoch secret %d not found under new campfire ID after rekey", epoch)
			continue
		}
		if got.RootSecret[0] != byte(epoch) {
			t.Errorf("epoch %d: root[0] = %d, want %d", epoch, got.RootSecret[0], epoch)
		}
	}

	// Old ID should have no secrets
	for epoch := uint64(0); epoch <= 2; epoch++ {
		got, err := s.GetEpochSecret(oldID, epoch)
		if err != nil {
			t.Fatalf("GetEpochSecret(oldID, %d) after rekey: %v", epoch, err)
		}
		if got != nil {
			t.Errorf("epoch secret %d should not exist under old campfire ID after rekey", epoch)
		}
	}
}
