package enrichment

import (
	"github.com/campfire-net/campfire/bridge/state"
)

const fallbackColor = "default"

// resolveSender looks up a sender pubkey hex in the identity_registry.
// If not found or DB is nil, it falls back to the first 8 hex chars as the
// display name, empty role, and "default" color.
func resolveSender(pubkeyHex string, db *state.DB) (name, role, color string) {
	if db != nil {
		info, err := db.LookupIdentity(pubkeyHex)
		if err == nil && info != nil {
			return info.DisplayName, info.Role, info.Color
		}
	}

	// Fallback: first 8 chars of hex pubkey.
	short := pubkeyHex
	if len(short) > 8 {
		short = short[:8]
	}
	return short, "", fallbackColor
}
