package naming

import (
	"os"
	"path/filepath"
	"strings"
)

// ContextResolution holds the naming context discovered by walking up the
// filesystem from a starting directory.
type ContextResolution struct {
	// NamingRoots is an ordered list of naming root campfire IDs found in
	// .campfire/root sentinels, deepest (most specific) first.
	NamingRoots []string

	// CenterCampfireID is the center campfire ID found in the nearest
	// .campfire/center sentinel, or empty if none was found.
	CenterCampfireID string

	// ContextKeyPath is the absolute path to the nearest .campfire/context-key.pub
	// file found in the directory tree, or empty if none was found.
	ContextKeyPath string
}

// ResolveContext walks up the filesystem from startDir, collecting naming
// context in a single pass. It returns the discovered NamingRoots (from
// .campfire/root sentinels), CenterCampfireID (from the nearest .campfire/center
// sentinel), and ContextKeyPath (path to the nearest .campfire/context-key.pub).
//
// If no sentinels are found, a zero-value ContextResolution is returned with no
// error. Symlinked directories are resolved to their real paths to prevent
// infinite loops.
func ResolveContext(startDir string) (*ContextResolution, error) {
	// Resolve symlinks to prevent infinite loops.
	resolved, err := filepath.EvalSymlinks(startDir)
	if err != nil {
		// If symlink resolution fails, fall back to the original path.
		resolved = startDir
	}

	res := &ContextResolution{}
	seen := map[string]bool{}

	dir := resolved
	for {
		campfireDir := filepath.Join(dir, ".campfire")

		// Check for .campfire/root
		rootData, err := os.ReadFile(filepath.Join(campfireDir, "root"))
		if err == nil {
			id := strings.TrimSpace(string(rootData))
			if id != "" && !seen[id] {
				seen[id] = true
				res.NamingRoots = append(res.NamingRoots, id)
			}
		}

		// Check for .campfire/center (first one found wins)
		if res.CenterCampfireID == "" {
			centerData, err := os.ReadFile(filepath.Join(campfireDir, "center"))
			if err == nil {
				id := strings.TrimSpace(string(centerData))
				if id != "" {
					res.CenterCampfireID = id
				}
			}
		}

		// Check for .campfire/context-key.pub (first one found wins)
		if res.ContextKeyPath == "" {
			keyPath := filepath.Join(campfireDir, "context-key.pub")
			if _, err := os.Stat(keyPath); err == nil {
				res.ContextKeyPath = keyPath
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return res, nil
}
