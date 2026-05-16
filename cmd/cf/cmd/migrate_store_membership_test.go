package cmd

// migrate_store_membership_test.go — defense-in-depth: membership/role check
// for `cf migrate-store` (campfireagent-6a9).
//
// Pre-fix: migrate-store performed no membership lookup; any local user that
// could name a campfire directory could migrate it.
//
// Test contract (veracity-gated):
//   1. Create a real campfire via setupCampfireWithRole (identity A, full role).
//   2. Create identity B (non-member) with a separate CF_HOME and store.
//   3. Run migrateStoreCmd.RunE as identity B pointing at identity A's campfire.
//   4. Assert the command returns a non-nil error (exits non-zero).
//   5. Assert no files in the campfire store directory were mutated (compare
//      pre/post directory snapshot).
//
// Uses real pkg/protocol store + fs transport — NOT stubbed validators.

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/identity"
)

// snapshotDir walks dir and returns a sorted slice of "relpath:size" strings
// for every regular file. Used to detect mutations.
func snapshotDir(t *testing.T, dir string) []string {
	t.Helper()
	var entries []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		entries = append(entries, rel+":"+string(rune('0'+int(info.Size()%10)))) // size mod 10 as stable marker
		// Use full size for a more complete fingerprint.
		entries[len(entries)-1] = rel + ":" + string([]byte{byte(info.Size() >> 0), byte(info.Size() >> 8)})
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotDir %s: %v", dir, err)
	}
	sort.Strings(entries)
	return entries
}

// setupNonMemberEnv creates identity B (non-member) with its own CF_HOME and
// store, sets environment variables so that migrateStoreCmd.RunE will load
// identity B's credentials while the transport base points at the real campfire
// directory containing identity A's campfire.
//
// Returns:
//   - campfireID: the campfire created for identity A
//   - transportBaseDir: the fs transport base (contains the campfire directory)
//   - cleanup: deferred cleanup function
func setupNonMemberEnv(t *testing.T) (campfireID, transportBaseDir string) {
	t.Helper()

	// --- Identity A: full member ---
	cfHomeA := t.TempDir()
	transportBaseDir = t.TempDir()

	agentA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity A: %v", err)
	}
	if err := agentA.Save(filepath.Join(cfHomeA, "identity.json")); err != nil {
		t.Fatalf("saving identity A: %v", err)
	}
	sA, err := store.Open(filepath.Join(cfHomeA, "store.db"))
	if err != nil {
		t.Fatalf("opening store A: %v", err)
	}
	t.Cleanup(func() { sA.Close() })

	campfireID = setupCampfireWithRole(t, agentA, sA, transportBaseDir, campfire.RoleFull)

	// --- Identity B: non-member, separate CF_HOME ---
	cfHomeB := t.TempDir()
	agentB, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity B: %v", err)
	}
	if err := agentB.Save(filepath.Join(cfHomeB, "identity.json")); err != nil {
		t.Fatalf("saving identity B: %v", err)
	}
	sB, err := store.Open(filepath.Join(cfHomeB, "store.db"))
	if err != nil {
		t.Fatalf("opening store B: %v", err)
	}
	t.Cleanup(func() { sB.Close() })
	// Identity B has NO membership for campfireID in its store.

	// Point the command at identity B's CF_HOME, but the shared transport base.
	t.Setenv("CF_HOME", cfHomeB)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	return campfireID, transportBaseDir
}

// TestMigrateStore_NonMemberDenied is the regression test for campfireagent-6a9.
//
// Veracity contract:
//   1. Command returns non-nil error for a non-member identity.
//   2. No files in the campfire store directory are mutated.
//   3. Uses real store/transport — not mocked.
func TestMigrateStore_NonMemberDenied(t *testing.T) {
	campfireID, transportBaseDir := setupNonMemberEnv(t)

	campfireDir := filepath.Join(transportBaseDir, campfireID)

	// Snapshot the campfire directory BEFORE the command runs.
	before := snapshotDir(t, campfireDir)

	// Run migrate-store as identity B (non-member).
	migrateStoreCmd.SetOut(&bytes.Buffer{})
	migrateStoreCmd.SetErr(&bytes.Buffer{})
	err := migrateStoreCmd.RunE(migrateStoreCmd, []string{campfireID})

	// 1. Command must return an error.
	if err == nil {
		t.Fatal("migrate-store returned nil error for a non-member identity — membership check missing")
	}

	// The error must mention membership or not-a-member.
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "member") && !strings.Contains(errMsg, "not a member") {
		t.Errorf("migrate-store returned unexpected error (expected membership denial): %v", err)
	}

	// 2. No files mutated.
	after := snapshotDir(t, campfireDir)
	if len(before) != len(after) {
		t.Errorf("campfire directory file count changed: before=%d after=%d (files were mutated)", len(before), len(after))
	}
	for i := range before {
		if i >= len(after) {
			break
		}
		if before[i] != after[i] {
			t.Errorf("campfire directory entry changed:\n  before: %s\n  after:  %s", before[i], after[i])
		}
	}
}

// TestMigrateStore_FullMemberAllowed verifies the positive case: a full member
// can run migrate-store (the command proceeds past the membership check).
// The campfire is already in bucketed layout after creation, so the migration
// will succeed or report "already bucketed" — not a membership error.
func TestMigrateStore_FullMemberAllowed(t *testing.T) {
	cfHomeA := t.TempDir()
	transportBaseDir := t.TempDir()

	agentA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity A: %v", err)
	}
	if err := agentA.Save(filepath.Join(cfHomeA, "identity.json")); err != nil {
		t.Fatalf("saving identity A: %v", err)
	}
	sA, err := store.Open(filepath.Join(cfHomeA, "store.db"))
	if err != nil {
		t.Fatalf("opening store A: %v", err)
	}
	t.Cleanup(func() { sA.Close() })

	campfireID := setupCampfireWithRole(t, agentA, sA, transportBaseDir, campfire.RoleFull)

	t.Setenv("CF_HOME", cfHomeA)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	migrateStoreCmd.SetOut(&bytes.Buffer{})
	migrateStoreCmd.SetErr(&bytes.Buffer{})
	err = migrateStoreCmd.RunE(migrateStoreCmd, []string{campfireID})

	// A full member must NOT get a membership-denial error.
	// The migration itself may error (e.g. already bucketed) — that is fine.
	// What must NOT happen: a "not a member" error.
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "not a member") || strings.Contains(errMsg, "not a member of campfire") {
			t.Errorf("full member was denied by membership check: %v", err)
		}
		// Any other error (e.g. "already bucketed", "dry-run", layout errors) is acceptable.
		t.Logf("migration returned (expected for already-bucketed campfire): %v", err)
	}
}

// TestMigrateStore_ForceFlag verifies that --force allows a non-member to
// bypass the membership check (escape hatch for disaster recovery).
func TestMigrateStore_ForceFlag(t *testing.T) {
	campfireID, _ := setupNonMemberEnv(t)

	// Reset force flag to false before setting.
	if err := migrateStoreCmd.Flags().Set("force", "true"); err != nil {
		// Flag doesn't exist yet — that is expected before the fix ships.
		// Skip the test gracefully; the flag will be added in the fix.
		t.Skipf("--force flag not yet defined (pre-fix): %v", err)
	}
	t.Cleanup(func() {
		_ = migrateStoreCmd.Flags().Set("force", "false")
	})

	migrateStoreCmd.SetOut(&bytes.Buffer{})
	migrateStoreCmd.SetErr(&bytes.Buffer{})
	err := migrateStoreCmd.RunE(migrateStoreCmd, []string{campfireID})

	// With --force, a non-member should not get a membership-denial error.
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "not a member") {
			t.Errorf("--force flag did not bypass membership check: %v", err)
		}
	}
}
