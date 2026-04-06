package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"sync"
	"time"
)

// DefaultVerificationTTL is the default TTL for super-identity verification entries.
// Callers should use this when invoking Set unless a custom TTL is required.
const DefaultVerificationTTL = 5 * time.Minute

// VerificationCache maps sender Ed25519 pubkeys to verified home campfire IDs.
// Entries expire after their individual TTL. Thread-safe.
//
// A verified entry means the sender's pubkey has been confirmed as belonging to
// the stated home campfire via the cf home be echo ceremony. The cache lets
// callers skip re-verifying the echo chain on every received message.
//
// Key encoding: Ed25519 pubkeys (32 bytes) are hex-encoded for use as map keys.
// Lazy expiry: expired entries are evicted on Get, not by a background goroutine.
type VerificationCache interface {
	// Set stores a verified mapping: senderPubkey → homeCampfireID.
	// Any prior entry for senderPubkey is replaced.
	Set(senderPubkey ed25519.PublicKey, homeCampfireID string, ttl time.Duration)

	// Get returns the verified home campfire ID for senderPubkey, or ("", false)
	// if not found or expired.
	Get(senderPubkey ed25519.PublicKey) (homeCampfireID string, ok bool)

	// Invalidate removes the entry for senderPubkey (e.g., on revocation).
	Invalidate(senderPubkey ed25519.PublicKey)
}

// verificationEntry holds a verified home campfire ID and its expiry.
type verificationEntry struct {
	homeCampfireID string
	expiresAt      time.Time
}

// memVerificationCache is the in-memory VerificationCache implementation.
type memVerificationCache struct {
	mu      sync.RWMutex
	entries map[string]verificationEntry // hex pubkey → entry
}

// NewVerificationCache returns a new in-memory VerificationCache.
func NewVerificationCache() VerificationCache {
	return &memVerificationCache{
		entries: make(map[string]verificationEntry),
	}
}

func pubkeyHex(pub ed25519.PublicKey) string {
	if pub == nil {
		panic("verification_cache: nil pubkey")
	}
	return hex.EncodeToString(pub)
}

// Set stores a verified mapping: senderPubkey → homeCampfireID with the given TTL.
// TTL=0 means the entry expires immediately (Get will always miss).
func (c *memVerificationCache) Set(senderPubkey ed25519.PublicKey, homeCampfireID string, ttl time.Duration) {
	key := pubkeyHex(senderPubkey)
	c.mu.Lock()
	c.entries[key] = verificationEntry{
		homeCampfireID: homeCampfireID,
		expiresAt:      time.Now().Add(ttl),
	}
	c.mu.Unlock()
}

// Get returns the verified home campfire ID for senderPubkey.
// Returns ("", false) if the entry is absent or expired.
func (c *memVerificationCache) Get(senderPubkey ed25519.PublicKey) (string, bool) {
	key := pubkeyHex(senderPubkey)
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiresAt) {
		// Lazy eviction: drop the expired entry under write lock.
		c.mu.Lock()
		// Re-check: another goroutine may have replaced the entry.
		if e2, still := c.entries[key]; still && time.Now().After(e2.expiresAt) {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return "", false
	}
	return e.homeCampfireID, true
}

// Invalidate removes the entry for senderPubkey.
func (c *memVerificationCache) Invalidate(senderPubkey ed25519.PublicKey) {
	key := pubkeyHex(senderPubkey)
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}
