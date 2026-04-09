package http

import (
	"fmt"
	"path/filepath"
	"strings"
)

// campfireIDLen is the number of hex characters in a valid campfire ID.
// A campfire ID is the hex-encoded Ed25519 public key: 32 bytes = 64 hex chars.
const campfireIDLen = 64

// ValidateCampfireID checks that id is exactly 64 lowercase hex characters.
// Returns a descriptive error for empty, too-short, too-long, uppercase, or
// non-hex inputs. This should be called before any cryptographic operation that
// uses the campfire_id to avoid processing malformed identifiers.
func ValidateCampfireID(id string) error {
	if id == "" {
		return fmt.Errorf("campfire_id is empty")
	}
	if len(id) != campfireIDLen {
		return fmt.Errorf("campfire_id has wrong length: got %d, want %d", len(id), campfireIDLen)
	}
	for i, c := range id {
		switch {
		case c >= '0' && c <= '9':
			// ok
		case c >= 'a' && c <= 'f':
			// ok
		case c >= 'A' && c <= 'F':
			return fmt.Errorf("campfire_id contains uppercase hex at position %d: use lowercase", i)
		default:
			return fmt.Errorf("campfire_id contains non-hex character %q at position %d", c, i)
		}
	}
	return nil
}

// sanitizeTransportDir validates a TransportDir value from a membership record
// and returns the cleaned absolute path. It rejects paths that are not absolute
// or that contain ".." components, defending against path traversal attacks.
func sanitizeTransportDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("transport dir is empty")
	}
	// Clean the path (resolves any . and .. elements).
	clean := filepath.Clean(dir)
	// After cleaning, the path must still be absolute.
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("transport dir %q is not an absolute path", dir)
	}
	// Reject if the original path contained ".." segments (pre-clean check).
	// filepath.Clean resolves them, but we want to reject stored values that
	// include traversal markers — they indicate a tampered record.
	if strings.Contains(dir, "..") {
		return "", fmt.Errorf("transport dir %q contains path traversal", dir)
	}
	return clean, nil
}
