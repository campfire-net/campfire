package protocol

import (
	"sync"
	"testing"
	"time"
)

// TestIdentityCache_SetGet verifies basic set/get with a valid TTL.
func TestIdentityCache_SetGet(t *testing.T) {
	c := NewIdentityCache(time.Hour)
	c.Set("cf1", "pub1", true)

	v, ok := c.Get("cf1", "pub1")
	if !ok {
		t.Fatal("Get: expected found=true, got false")
	}
	if !v {
		t.Error("Get: expected verified=true, got false")
	}
}

// TestIdentityCache_NotFound verifies missing entries return (false, false).
func TestIdentityCache_NotFound(t *testing.T) {
	c := NewIdentityCache(time.Hour)

	v, ok := c.Get("missing", "key")
	if ok {
		t.Error("Get: expected found=false for missing entry, got true")
	}
	if v {
		t.Error("Get: expected verified=false for missing entry, got true")
	}
}

// TestIdentityCache_Expired verifies expired entries return (false, false).
func TestIdentityCache_Expired(t *testing.T) {
	c := NewIdentityCache(time.Millisecond)
	c.Set("cf1", "pub1", true)

	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	v, ok := c.Get("cf1", "pub1")
	if ok {
		t.Error("Get: expected found=false for expired entry, got true")
	}
	if v {
		t.Error("Get: expected verified=false for expired entry, got true")
	}
}

// TestIdentityCache_SetFalse verifies that verified=false is stored and returned correctly.
func TestIdentityCache_SetFalse(t *testing.T) {
	c := NewIdentityCache(time.Hour)
	c.Set("cf1", "pub1", false)

	v, ok := c.Get("cf1", "pub1")
	if !ok {
		t.Fatal("Get: expected found=true, got false")
	}
	if v {
		t.Error("Get: expected verified=false, got true")
	}
}

// TestIdentityCache_Overwrite verifies that Set overwrites prior entries.
func TestIdentityCache_Overwrite(t *testing.T) {
	c := NewIdentityCache(time.Hour)
	c.Set("cf1", "pub1", false)
	c.Set("cf1", "pub1", true)

	v, ok := c.Get("cf1", "pub1")
	if !ok {
		t.Fatal("Get: expected found=true, got false")
	}
	if !v {
		t.Error("Get: expected verified=true after overwrite, got false")
	}
}

// TestIdentityCache_Prune verifies that Prune removes expired entries.
func TestIdentityCache_Prune(t *testing.T) {
	c := NewIdentityCache(time.Millisecond)
	c.Set("cf1", "pub1", true)
	c.Set("cf2", "pub2", true)

	if c.Len() != 2 {
		t.Fatalf("Len: want 2, got %d", c.Len())
	}

	time.Sleep(5 * time.Millisecond)
	c.Prune()

	if c.Len() != 0 {
		t.Errorf("Len after Prune: want 0, got %d", c.Len())
	}
}

// TestIdentityCache_PruneKeepsValid verifies that Prune only removes expired entries.
func TestIdentityCache_PruneKeepsValid(t *testing.T) {
	c := NewIdentityCache(time.Hour)
	c.Set("cf1", "pub1", true) // valid (1h TTL)

	// Force-insert an already-expired entry.
	c.mu.Lock()
	c.entries[cacheKey{"cf2", "pub2"}] = cacheEntry{verified: true, expiresAt: time.Now().Add(-time.Second)}
	c.mu.Unlock()

	if c.Len() != 2 {
		t.Fatalf("Len before Prune: want 2, got %d", c.Len())
	}

	c.Prune()

	if c.Len() != 1 {
		t.Errorf("Len after Prune: want 1 (valid entry), got %d", c.Len())
	}

	v, ok := c.Get("cf1", "pub1")
	if !ok || !v {
		t.Error("Get: valid entry should survive Prune")
	}
}

// TestIdentityCache_ConcurrentAccess verifies thread safety under concurrent reads and writes.
func TestIdentityCache_ConcurrentAccess(t *testing.T) {
	c := NewIdentityCache(time.Hour)

	var wg sync.WaitGroup
	const goroutines = 20
	const ops = 100

	// Concurrent writers.
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				c.Set("cf1", "pub1", true)
			}
		}(i)
	}

	// Concurrent readers.
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				c.Get("cf1", "pub1") //nolint:errcheck
			}
		}(i)
	}

	wg.Wait()

	// Final state should be deterministic.
	v, ok := c.Get("cf1", "pub1")
	if !ok || !v {
		t.Error("after concurrent access, entry should be found and verified")
	}
}

// TestIdentityCache_DefaultTTL verifies that TTL <= 0 uses DefaultIdentityCacheTTL.
func TestIdentityCache_DefaultTTL(t *testing.T) {
	c := NewIdentityCache(0)
	if c.ttl != DefaultIdentityCacheTTL {
		t.Errorf("ttl: want %v, got %v", DefaultIdentityCacheTTL, c.ttl)
	}
}

// TestIdentityCache_MultipleKeys verifies independent keys don't interfere.
func TestIdentityCache_MultipleKeys(t *testing.T) {
	c := NewIdentityCache(time.Hour)
	c.Set("cf1", "pubA", true)
	c.Set("cf1", "pubB", false)
	c.Set("cf2", "pubA", false)

	cases := []struct {
		cf      string
		pub     string
		wantV   bool
		wantOK  bool
	}{
		{"cf1", "pubA", true, true},
		{"cf1", "pubB", false, true},
		{"cf2", "pubA", false, true},
		{"cf2", "pubB", false, false},
	}
	for _, tc := range cases {
		v, ok := c.Get(tc.cf, tc.pub)
		if ok != tc.wantOK {
			t.Errorf("Get(%s, %s): found=%v, want %v", tc.cf, tc.pub, ok, tc.wantOK)
		}
		if v != tc.wantV {
			t.Errorf("Get(%s, %s): verified=%v, want %v", tc.cf, tc.pub, v, tc.wantV)
		}
	}
}
