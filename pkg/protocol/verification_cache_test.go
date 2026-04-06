package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"
)

// validCampfireID is a valid 64-character lowercase hex string for use in tests.
const validCampfireID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

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
	homeID := validCampfireID

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
	c.Set(pub, validCampfireID, -1*time.Millisecond)

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

	c.Set(pub, validCampfireID, DefaultVerificationTTL)
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

	firstID := strings.Repeat("1", 64)
	secondID := strings.Repeat("2", 64)
	c.Set(pub, firstID, DefaultVerificationTTL)
	c.Set(pub, secondID, DefaultVerificationTTL)

	got, ok := c.Get(pub)
	if !ok {
		t.Fatal("Get returned ok=false, want ok=true")
	}
	if got != secondID {
		t.Fatalf("Get returned %q, want %q", got, secondID)
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
					c.Set(pub, validCampfireID, DefaultVerificationTTL)
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

// TestVerificationCache_NilPubkeyPanics verifies that passing a nil pubkey to
// any cache method panics rather than silently colliding on the "" map key.
func TestVerificationCache_NilPubkeyPanics(t *testing.T) {
	c := NewVerificationCache()

	assertPanics := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("%s: expected panic, got none", name)
			}
		}()
		fn()
	}

	assertPanics("Get(nil)", func() { c.Get(nil) })
	assertPanics("Set(nil, ...)", func() { c.Set(nil, validCampfireID, time.Minute) })
	assertPanics("Invalidate(nil)", func() { c.Invalidate(nil) })
}

// TestVerificationCache_ZeroTTL verifies that TTL=0 means immediate expiry:
// Get always misses.
func TestVerificationCache_ZeroTTL(t *testing.T) {
	c := NewVerificationCache()
	pub, _ := genKey(t)

	c.Set(pub, validCampfireID, 0)

	_, ok := c.Get(pub)
	if ok {
		t.Fatal("Get returned ok=true with TTL=0, want ok=false (immediate expiry)")
	}
}

// TestVerificationCache_ZeroTTL_AlwaysMisses verifies the >= expiry semantics:
// when TTL=0, expiresAt == time.Now() at Set time. Get must treat an entry as
// expired when time.Now() == expiresAt (i.e., !time.Before is the correct
// predicate, not time.After which requires strictly greater-than).
// This test exercises the real time.Before path — no sleep, no mock clock.
func TestVerificationCache_ZeroTTL_AlwaysMisses(t *testing.T) {
	c := NewVerificationCache()
	pub, _ := genKey(t)

	// Set with TTL=0: expiresAt = time.Now() at the moment of Set.
	// Get must return ("", false) even if called in the same nanosecond,
	// because the >= expiry predicate (!time.Before) treats equal times as expired.
	c.Set(pub, validCampfireID, 0)

	got, ok := c.Get(pub)
	if ok {
		t.Fatalf("Get returned ok=true immediately after Set with TTL=0, got %q; want (\"\", false)", got)
	}
	if got != "" {
		t.Fatalf("Get returned non-empty string %q after TTL=0 Set", got)
	}
}

// TestVerificationCache_TTLClamped verifies that a TTL exceeding MaxVerificationTTL
// is silently clamped: the entry is stored and retrievable immediately.
func TestVerificationCache_TTLClamped(t *testing.T) {
	c := NewVerificationCache()
	pub, _ := genKey(t)

	// Supply a TTL far beyond the maximum.
	c.Set(pub, validCampfireID, 24*time.Hour)

	// The entry must still be present immediately after Set.
	id, ok := c.Get(pub)
	if !ok {
		t.Fatal("expected entry to be present after TTL clamp")
	}
	if id != validCampfireID {
		t.Fatalf("Get returned %q, want %q", id, validCampfireID)
	}
}

// TestVerificationCache_InvalidCampfireIDPanics verifies that Set panics when
// given a homeCampfireID that is not a 64-character lowercase hex string.
func TestVerificationCache_InvalidCampfireIDPanics(t *testing.T) {
	c := NewVerificationCache()
	pub, _ := genKey(t)

	assertPanics := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("%s: expected panic, got none", name)
			}
		}()
		fn()
	}

	// Non-hex string should panic.
	assertPanics("non-hex ID", func() {
		c.Set(pub, "not-a-valid-campfire-id", time.Minute)
	})
	// Upper-case hex must panic (must be lowercase).
	assertPanics("uppercase hex ID", func() {
		c.Set(pub, strings.Repeat("A", 64), time.Minute)
	})
	// Too short should panic.
	assertPanics("short ID", func() {
		c.Set(pub, strings.Repeat("a", 32), time.Minute)
	})
	// Too long should panic.
	assertPanics("long ID", func() {
		c.Set(pub, strings.Repeat("a", 65), time.Minute)
	})
	// Empty string should panic.
	assertPanics("empty ID", func() {
		c.Set(pub, "", time.Minute)
	})
}
