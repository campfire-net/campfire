package naming

import (
	"os"
	"path/filepath"
	"strings"
)

// FSWalkSentinel is the sentinel value used in join-policy.json consult_campfire
// to indicate that roots should be discovered via filesystem walk-up rather than
// a real campfire consultation.
const FSWalkSentinel = "fs-walk"

// FSWalkRoots walks the filesystem from startDir upward looking for .campfire/root
// files and returns an ordered list of root campfire IDs found, with joinRoot
// appended as fallback. The deepest (most specific) root is first.
func FSWalkRoots(startDir string, joinRoot string) []string {
	var roots []string
	seen := map[string]bool{}

	dir := startDir
	for {
		rootFile := filepath.Join(dir, ".campfire", "root")
		data, err := os.ReadFile(rootFile)
		if err == nil {
			id := strings.TrimSpace(string(data))
			if id != "" && !seen[id] {
				seen[id] = true
				roots = append(roots, id)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if joinRoot != "" && !seen[joinRoot] {
		roots = append(roots, joinRoot)
	}

	return roots
}
