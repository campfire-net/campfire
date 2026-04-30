// profile_cache.go — TRANSITIONAL (Stage 3 placeholder, campfireagent-9f4).
//
// Belongs to cf-identity/cf-profile L3. Temporary location until Stage 3 lands.
// See profile.go for full migration rationale.
package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ProfileEntry holds cached identity profile information for a campfire participant.
type ProfileEntry struct {
	DisplayName string    `json:"display_name"`
	LearnedAt   time.Time `json:"learned_at"`
}

// ProfileCache maps sender Ed25519 pubkeys to learned display names.
// Persisted to cfHome/profiles.json. Thread-safe.
type ProfileCache struct {
	mu      sync.RWMutex
	entries map[string]ProfileEntry // hex pubkey → entry
	path    string                  // path to profiles.json
}

// NewProfileCache loads profiles from cfHome/profiles.json (creates if absent).
// Never returns an error — missing or corrupt file starts fresh.
func NewProfileCache(cfHome string) *ProfileCache {
	c := &ProfileCache{
		entries: make(map[string]ProfileEntry),
		path:    filepath.Join(cfHome, "profiles.json"),
	}
	c.load()
	return c
}

// load reads profiles.json from disk into the cache.
// If the file is missing, nothing happens. If it is corrupt, a warning is
// printed to stderr and the cache starts empty.
func (c *ProfileCache) load() {
	data, err := os.ReadFile(c.path)
	if err != nil {
		// Missing file is expected on first run — not an error.
		return
	}
	var entries map[string]ProfileEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		fmt.Fprintf(os.Stderr, "profile_cache: corrupt profiles.json, starting fresh: %v\n", err)
		return
	}
	c.entries = entries
}

// Set stores display_name for pubkey. Persists immediately to disk.
func (c *ProfileCache) Set(pubkey ed25519.PublicKey, displayName string) error {
	key := profilePubkeyHex(pubkey)
	entry := ProfileEntry{
		DisplayName: displayName,
		LearnedAt:   time.Now(),
	}
	c.mu.Lock()
	c.entries[key] = entry
	snapshot := make(map[string]ProfileEntry, len(c.entries))
	for k, v := range c.entries {
		snapshot[k] = v
	}
	c.mu.Unlock()
	return c.persist(snapshot)
}

// Get returns the display name for pubkey, or ("", false) if not found.
func (c *ProfileCache) Get(pubkey ed25519.PublicKey) (displayName string, ok bool) {
	key := profilePubkeyHex(pubkey)
	c.mu.RLock()
	entry, found := c.entries[key]
	c.mu.RUnlock()
	if !found {
		return "", false
	}
	return entry.DisplayName, true
}

// All returns a snapshot of all cached profiles.
func (c *ProfileCache) All() map[string]ProfileEntry {
	c.mu.RLock()
	snapshot := make(map[string]ProfileEntry, len(c.entries))
	for k, v := range c.entries {
		snapshot[k] = v
	}
	c.mu.RUnlock()
	return snapshot
}

// persist writes the entries map to disk atomically via a temp file + rename.
func (c *ProfileCache) persist(entries map[string]ProfileEntry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.path)
	tmp, err := os.CreateTemp(dir, "profiles.*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, c.path)
}

// profilePubkeyHex hex-encodes a pubkey for use as a map key.
// Panics if pubkey is nil (same contract as verification_cache).
func profilePubkeyHex(pub ed25519.PublicKey) string {
	if pub == nil {
		panic("profile_cache: nil pubkey")
	}
	return hex.EncodeToString(pub)
}
