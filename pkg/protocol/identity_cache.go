package protocol

import (
	"sync"
	"time"
)

// IdentityCache caches (campfire_id, agent_pubkey) → verified mappings with a
// configurable TTL. It provides O(1) amortized identity lookups for agents
// encountered repeatedly within a session.
//
// Verification semantics: a (campfireID, agentPubkey) pair is "verified" when the
// agent's pubkey has been confirmed as member 0 of their self-campfire. Tainted
// SenderCampfireID values from wire messages must be verified before being
// treated as authoritative identity addresses.
//
// Thread safety: all methods are safe for concurrent use.
type IdentityCache struct {
	mu      sync.RWMutex
	entries map[cacheKey]cacheEntry
	ttl     time.Duration
}

type cacheKey struct {
	campfireID  string // hex of self-campfire ID
	agentPubkey string // hex of agent pubkey
}

type cacheEntry struct {
	verified  bool
	expiresAt time.Time
}

// DefaultIdentityCacheTTL is the default time-to-live for cache entries.
const DefaultIdentityCacheTTL = time.Hour

// NewIdentityCache creates a new IdentityCache with the given TTL.
// Use DefaultIdentityCacheTTL for the standard 1-hour TTL.
func NewIdentityCache(ttl time.Duration) *IdentityCache {
	if ttl <= 0 {
		ttl = DefaultIdentityCacheTTL
	}
	return &IdentityCache{
		entries: make(map[cacheKey]cacheEntry),
		ttl:     ttl,
	}
}

// Get looks up a (campfireID, agentPubkey) pair.
// Returns (verified, true) if the entry exists and has not expired.
// Returns (false, false) if the entry is absent or expired.
func (c *IdentityCache) Get(campfireID, agentPubkey string) (verified bool, found bool) {
	c.mu.RLock()
	e, ok := c.entries[cacheKey{campfireID, agentPubkey}]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return false, false
	}
	return e.verified, true
}

// Set stores a (campfireID, agentPubkey) → verified entry with the cache TTL.
func (c *IdentityCache) Set(campfireID, agentPubkey string, verified bool) {
	c.mu.Lock()
	c.entries[cacheKey{campfireID, agentPubkey}] = cacheEntry{
		verified:  verified,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}

// Prune removes expired entries from the cache. Call periodically to prevent
// unbounded growth when agents rotate keys frequently. Safe to call concurrently.
func (c *IdentityCache) Prune() {
	now := time.Now()
	c.mu.Lock()
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

// Len returns the number of entries currently in the cache (including expired).
// Primarily for testing and diagnostics.
func (c *IdentityCache) Len() int {
	c.mu.RLock()
	n := len(c.entries)
	c.mu.RUnlock()
	return n
}
