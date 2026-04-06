package protocol

import (
	"encoding/json"
	"os"
	"sync"
)

// SessionProfileCache is an in-memory map from sender pubkey hex to self-declared display name.
// It is populated each session from identity:profile messages encountered during reads.
// Display names are UNVERIFIED — self-declared by the sender.
// The cache is EPHEMERAL — it is never persisted to disk.
//
// Thread safety: all methods are safe for concurrent use.
type SessionProfileCache struct {
	mu      sync.RWMutex
	entries map[string]string // pubkey hex → display_name
}

// NewSessionProfileCache creates a new empty SessionProfileCache.
func NewSessionProfileCache() *SessionProfileCache {
	return &SessionProfileCache{
		entries: make(map[string]string),
	}
}

// Set stores a pubkey → display_name mapping.
// displayName is UNVERIFIED — it is whatever the sender claimed.
func (c *SessionProfileCache) Set(pubkeyHex, displayName string) {
	if pubkeyHex == "" || displayName == "" {
		return
	}
	c.mu.Lock()
	c.entries[pubkeyHex] = displayName
	c.mu.Unlock()
}

// Lookup returns the display name for pubkeyHex, or "" if not cached.
func (c *SessionProfileCache) Lookup(pubkeyHex string) string {
	c.mu.RLock()
	name := c.entries[pubkeyHex]
	c.mu.RUnlock()
	return name
}

// LoadFromMessages scans a slice of messages for identity:profile tags and
// populates the cache from their payloads. The sender of each qualifying message
// is used as the pubkey key; the payload must decode as {"display_name": "..."}.
// Invalid payloads are silently skipped — best-effort population.
func (c *SessionProfileCache) LoadFromMessages(messages []Message) {
	for _, m := range messages {
		for _, tag := range m.Tags {
			if tag == "identity:profile" {
				var payload struct {
					DisplayName string `json:"display_name"`
				}
				if err := json.Unmarshal(m.Payload, &payload); err != nil {
					continue
				}
				if payload.DisplayName != "" && m.Sender != "" {
					c.Set(m.Sender, payload.DisplayName)
				}
				break
			}
		}
	}
}

// ProfileFile is the schema for ~/.cf/profile.json.
type ProfileFile struct {
	DisplayName string `json:"display_name"`
}

// LoadProfile reads ~/.cf/profile.json from cfHome.
// Returns a zero-value ProfileFile if the file does not exist or cannot be parsed.
func LoadProfile(cfHome string) ProfileFile {
	path := profilePath(cfHome)
	data, err := os.ReadFile(path)
	if err != nil {
		return ProfileFile{}
	}
	var p ProfileFile
	if err := json.Unmarshal(data, &p); err != nil {
		return ProfileFile{}
	}
	return p
}

// SaveProfile writes a ProfileFile to ~/.cf/profile.json in cfHome.
func SaveProfile(cfHome string, p ProfileFile) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(profilePath(cfHome), data, 0600)
}

func profilePath(cfHome string) string {
	return cfHome + "/profile.json"
}
