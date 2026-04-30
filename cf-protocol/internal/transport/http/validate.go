package http

import (
	"fmt"
	"path/filepath"
	"strings"
)

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
