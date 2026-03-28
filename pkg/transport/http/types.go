package http

// MembershipEvent represents a membership change notification.
type MembershipEvent struct {
	Event    string `json:"event"`    // "join", "leave", or "evict"
	Member   string `json:"member"`   // hex public key
	Endpoint string `json:"endpoint"` // HTTP endpoint URL (may be empty for leave/evict)
}

// CampfireKeyProvider returns the campfire private key for a given campfire ID.
// Returns an error if the campfire is not found on this node.
type CampfireKeyProvider func(campfireID string) (privKey []byte, pubKey []byte, err error)

// CampfireDeliveryModesProvider returns the effective delivery modes for a campfire.
// Used by the join handler when the membership's TransportDir is not a filesystem
// path (e.g., HTTP-mode campfires where TransportDir is a URL). Returns nil when
// the campfire is not found or the modes are unknown; the caller defaults to ["pull"].
type CampfireDeliveryModesProvider func(campfireID string) []string
