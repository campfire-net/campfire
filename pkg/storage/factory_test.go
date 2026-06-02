package storage_test

import (
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/storage"
)

// TestFactorySelectsLocalWhenNoConnString verifies that, absent an Azure
// connection string, the factory returns a LocalStorage backed by a real
// SQLite store at the requested path. Ground-source: a real SQLite store is
// opened, not a mock.
func TestFactorySelectsLocalWhenNoConnString(t *testing.T) {
	dir := t.TempDir()
	cfg := storage.Config{
		ConnectionString: "", // no cloud → local
		LocalPath:        filepath.Join(dir, "store.db"),
	}
	st, err := storage.Open(cfg)
	if err != nil {
		t.Fatalf("Open(local): %v", err)
	}
	defer st.Close()

	if st.Backend() != storage.BackendLocal {
		t.Fatalf("Backend() = %q, want %q", st.Backend(), storage.BackendLocal)
	}

	// MembershipExists answers cleanly on an empty store: not a member.
	exists, err := st.MembershipExists("nonexistent-campfire")
	if err != nil {
		t.Fatalf("MembershipExists: %v", err)
	}
	if exists {
		t.Fatalf("MembershipExists on empty store = true, want false")
	}
}

// TestOpenLocalRequiresPath verifies the local branch refuses an empty path
// rather than opening a SQLite store at an ambiguous location.
func TestOpenLocalRequiresPath(t *testing.T) {
	_, err := storage.Open(storage.Config{ConnectionString: "", LocalPath: ""})
	if err == nil {
		t.Fatalf("Open with empty ConnectionString and empty LocalPath: want error, got nil")
	}
}
