package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestProfileCache_SetAndGet sets a display name and retrieves it.
func TestProfileCache_SetAndGet(t *testing.T) {
	dir := t.TempDir()
	c := NewProfileCache(dir)

	pub, _ := genKey(t)
	if err := c.Set(pub, "Alice"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	name, ok := c.Get(pub)
	if !ok {
		t.Fatal("Get returned ok=false, want ok=true")
	}
	if name != "Alice" {
		t.Fatalf("Get returned %q, want %q", name, "Alice")
	}
}

// TestProfileCache_NotFound verifies that Get for an unknown pubkey returns ("", false).
func TestProfileCache_NotFound(t *testing.T) {
	dir := t.TempDir()
	c := NewProfileCache(dir)

	pub, _ := genKey(t)
	name, ok := c.Get(pub)
	if ok {
		t.Fatal("Get returned ok=true for unknown pubkey, want ok=false")
	}
	if name != "" {
		t.Fatalf("Get returned %q for unknown pubkey, want empty string", name)
	}
}

// TestProfileCache_Persists verifies that Set persists and a new ProfileCache
// loaded from the same cfHome returns the stored value.
func TestProfileCache_Persists(t *testing.T) {
	dir := t.TempDir()
	pub, _ := genKey(t)

	c1 := NewProfileCache(dir)
	if err := c1.Set(pub, "Bob"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Load a fresh cache from the same directory.
	c2 := NewProfileCache(dir)
	name, ok := c2.Get(pub)
	if !ok {
		t.Fatal("Get on fresh cache returned ok=false after persist, want ok=true")
	}
	if name != "Bob" {
		t.Fatalf("Get returned %q, want %q", name, "Bob")
	}

	// Verify atomic write: no temp files should remain after Set completes.
	tmpFiles, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("Glob for temp files: %v", err)
	}
	if len(tmpFiles) > 0 {
		t.Errorf("temp files remain after Set: %v", tmpFiles)
	}

	// Verify the written file is valid JSON that round-trips correctly.
	rawBytes, err := os.ReadFile(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatalf("reading profiles.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(rawBytes, &parsed); err != nil {
		t.Errorf("profiles.json is not valid JSON: %v\ncontent: %s", err, rawBytes)
	}
}

// TestProfileCache_CorruptFile verifies that a corrupt profiles.json causes
// NewProfileCache to start fresh without panicking.
func TestProfileCache_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	// Write garbage to profiles.json.
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte("not valid json {{{{"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Should not panic; should start fresh.
	c := NewProfileCache(dir)
	pub, _ := genKey(t)
	_, ok := c.Get(pub)
	if ok {
		t.Fatal("Get returned ok=true after corrupt file, want ok=false (fresh cache)")
	}
}

// TestProfileCache_NilPubkeyPanics verifies that Set(nil, ...) and Get(nil) both panic.
func TestProfileCache_NilPubkeyPanics(t *testing.T) {
	dir := t.TempDir()
	c := NewProfileCache(dir)

	assertPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("%s: expected panic, got none", name)
			}
		}()
		fn()
	}

	assertPanic("Set(nil, ...)", func() { c.Set(nil, "Alice") })
	assertPanic("Get(nil)", func() { c.Get(nil) })
}

// TestProfileCache_All verifies that All() returns a snapshot of all entries.
func TestProfileCache_All(t *testing.T) {
	dir := t.TempDir()
	c := NewProfileCache(dir)

	pub1, _ := genKey(t)
	pub2, _ := genKey(t)

	if err := c.Set(pub1, "Alice"); err != nil {
		t.Fatalf("Set pub1: %v", err)
	}
	if err := c.Set(pub2, "Bob"); err != nil {
		t.Fatalf("Set pub2: %v", err)
	}

	all := c.All()
	if len(all) != 2 {
		t.Fatalf("All() returned %d entries, want 2", len(all))
	}

	// Verify entries are present and correct.
	found := map[string]bool{"Alice": false, "Bob": false}
	for _, entry := range all {
		found[entry.DisplayName] = true
	}
	for name, ok := range found {
		if !ok {
			t.Errorf("All() missing entry with DisplayName %q", name)
		}
	}

	// Verify snapshot independence — mutating the returned map doesn't affect the cache.
	for k := range all {
		delete(all, k)
	}
	if got := c.All(); len(got) != 2 {
		t.Errorf("All() after mutating snapshot returned %d entries, want 2", len(got))
	}
}

// TestProfileCache_ConcurrentAccess exercises concurrent Set + Get without data races.
// Run with -race to detect data races.
func TestProfileCache_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	c := NewProfileCache(dir)

	const workers = 10
	const ops = 20

	keys := make([][]byte, 5)
	for i := range keys {
		pub, _ := genKey(t)
		keys[i] = pub
	}

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	// Writers.
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				pub := keys[i%len(keys)]
				_ = c.Set(pub, "worker-name")
			}
		}(w)
	}

	// Readers.
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				pub := keys[i%len(keys)]
				c.Get(pub) //nolint:errcheck
			}
		}()
	}

	wg.Wait()
	// No assertions — the race detector is the oracle.
}
