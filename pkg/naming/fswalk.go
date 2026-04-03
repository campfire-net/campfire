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

// fsWalkMaxDepth is the maximum number of directory levels FSWalkRoots will
// traverse upward. This caps the walk against symlink loops or unexpectedly deep
// directory hierarchies.
const fsWalkMaxDepth = 50

// FSWalkRoots walks the filesystem from startDir upward looking for .campfire/root
// files and returns an ordered list of root campfire IDs found, with joinRoot
// appended as fallback. The deepest (most specific) root is first.
//
// The walk is capped at fsWalkMaxDepth levels to guard against symlink loops.
func FSWalkRoots(startDir string, joinRoot string) []string {
	var roots []string
	seen := map[string]bool{}

	dir := startDir
	for depth := 0; depth < fsWalkMaxDepth; depth++ {
		rootFile := filepath.Join(dir, ".campfire", "root")
		data, err := os.ReadFile(rootFile)
		if err == nil {
			id := strings.TrimSpace(string(data))
			if campfireIDRe.MatchString(id) && !seen[id] {
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
