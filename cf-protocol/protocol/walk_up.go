package protocol

// walk_up.go — shared filesystem sentinel walk-up helpers.
//
// These helpers walk up the directory tree looking for .campfire/ sentinels
// without importing pkg/naming (which imports pkg/protocol, creating a cycle).

import (
	"os"
	"path/filepath"
	"strings"
)

// walkUpForCenter walks up parent directories from startDir looking for
// a .campfire/center sentinel file. Returns the center campfire ID or empty.
//
// This is an inlined equivalent of the relevant part of naming.ResolveContext,
// used here to avoid the import cycle (pkg/naming imports pkg/protocol).
func walkUpForCenter(startDir string) string {
	resolved, err := filepath.EvalSymlinks(startDir)
	if err != nil {
		resolved = startDir
	}

	dir := resolved
	for {
		centerPath := filepath.Join(dir, ".campfire", centerSentinelFile)
		data, err := os.ReadFile(centerPath)
		if err == nil {
			id := strings.TrimSpace(string(data))
			if id != "" {
				return id
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
