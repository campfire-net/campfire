package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"
)

// genKey generates a fresh Ed25519 key pair for testing.
func genKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// TestVerificationCache_SetAndGet verifies that a stored entry is returned
// within its TTL.
func TestVerificationCache_SetAndGet(t *testing.T) {
	c := NewVerificationCache()
	pub, _ := genKey(t)
	homeID := "abc123campfire"

	c.Set(pub, homeID, DefaultVerificationTTL)

	got, ok := c.Get(pub)
	if !ok {
		t.Fatal("Get returned ok=false, want ok=true")
	}
	if got != homeID {
		t.Fatalf("Get returned %q, want %q", got, homeID)
	}
}

// TestVerificationCache_Expired verifies that a TTL-elapsed entry returns ("", false).
func TestVerificationCache_Expired(t *testing.T) {
	c := NewVerificationCache()
	pub, _ := genKey(t)

	// Set with a TTL that has already elapsed.
	c.Set(pub, "home-campfire", -1*time.Millisecond)

	got, ok := c.Get(pub)
	if ok {
		t.Fatalf("Get returned ok=true after expiry, got %q", got)
	}
	if got != "" {
		t.Fatalf("Get returned non-empty string after expiry: %q", got)
	}
}

// TestVerificationCache_Invalidate verifies that Invalidate removes a live entry.
func TestVerificationCache_Invalidate(t *testing.T) {
	c := NewVerificationCache()
	pub, _ := genKey(t)

	c.Set(pub, "some-home", DefaultVerificationTTL)
	c.Invalidate(pub)

	_, ok := c.Get(pub)
	if ok {
		t.Fatal("Get returned ok=true after Invalidate, want ok=false")
	}
}

// TestVerificationCache_Replace verifies that a second Set for the same key
// replaces the first value.
func TestVerificationCache_Replace(t *testing.T) {
	c := NewVerificationCache()
	pub, _ := genKey(t)

	c.Set(pub, "first-home", DefaultVerificationTTL)
	c.Set(pub, "second-home", DefaultVerificationTTL)

	got, ok := c.Get(pub)
	if !ok {
		t.Fatal("Get returned ok=false, want ok=true")
	}
	if got != "second-home" {
		t.Fatalf("Get returned %q, want %q", got, "second-home")
	}
}

// TestVerificationCache_ConcurrentAccess exercises the cache under concurrent
// Set, Get, and Invalidate calls. Run with -race to detect data races.
func TestVerificationCache_ConcurrentAccess(t *testing.T) {
	c := NewVerificationCache()

	const workers = 20
	const ops = 100

	// Pre-generate keys so goroutines share some keys (contention) and have
	// some unique ones.
	keys := make([]ed25519.PublicKey, 5)
	for i := range keys {
		pub, _ := genKey(t)
		keys[i] = pub
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				pub := keys[i%len(keys)]
				switch i % 3 {
				case 0:
					c.Set(pub, "home-"+string(rune('A'+id%26)), DefaultVerificationTTL)
				case 1:
					c.Get(pub) //nolint:errcheck
				case 2:
					c.Invalidate(pub)
				}
			}
		}(w)
	}
	wg.Wait()
	// No assertions — the race detector is the oracle.
}

// TestVerificationCache_ZeroTTL verifies that TTL=0 means immediate expiry:
// Get always misses.
func TestVerificationCache_ZeroTTL(t *testing.T) {
	c := NewVerificationCache()
	pub, _ := genKey(t)

	c.Set(pub, "home-campfire", 0)

	_, ok := c.Get(pub)
	if ok {
		t.Fatal("Get returned ok=true with TTL=0, want ok=false (immediate expiry)")
	}
}
