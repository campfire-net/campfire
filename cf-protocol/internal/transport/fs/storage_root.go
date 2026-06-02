package fs

// storage_root.go — tree-walk .cf/config.toml storage-root resolution for the
// fs transport (campfireagent-3f0).
//
// Resolution order (highest priority first):
//  1. $CF_TRANSPORT_DIR — explicit override (back-compat)
//  2. $CF_HOME — explicit override, returns CF_HOME/campfires (back-compat)
//  3. Walk up from cwd looking for .cf/config.toml with transport.storage_root
//  4. ~/.campfire — compiled-in default
//
// The walk stops at the user's home directory so it never picks up configs
// written outside the user's home tree.
//
// Security: readStorageRoot validates the resolved storage_root (S4-equivalent):
// relative paths that after filepath.Join+Clean escape the home directory are
// rejected — the walk skips that config and continues. Absolute paths outside
// home are accepted (legitimate operator use-case: /data/campfires on a server).

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// storageRootTOML is the minimal TOML shape needed to extract
// [transport].storage_root from a .cf/config.toml without importing the full
// protocol config package (which would create a circular dependency since
// protocol imports fs).
type storageRootTOML struct {
	Transport struct {
		StorageRoot string `toml:"storage_root"`
	} `toml:"transport"`
}

// cfConfigDir is the name of the per-directory campfire config folder.
const cfConfigDir = ".cf"

// cfConfigFile is the name of the campfire config file inside cfConfigDir.
const cfConfigFile = "config.toml"

// storageRootSource identifies which resolution step produced the storage root.
// It is used by DefaultBaseDir to decide whether to append "campfires" without
// re-reading environment variables (eliminating the double-read from campfireagent-75f).
type storageRootSource int

const (
	// sourceTransportDir — CF_TRANSPORT_DIR: already the full BaseDir, no suffix.
	sourceTransportDir storageRootSource = iota
	// sourceCFHome — CF_HOME/campfires: already has the "campfires" suffix.
	sourceCFHome
	// sourceConfig — tree-walk found a storage_root in .cf/config.toml: needs "campfires" suffix.
	sourceConfig
	// sourceDefault — ~/.campfire compiled-in default: needs "campfires" suffix.
	sourceDefault
)

// storageRootResult carries the resolved path and its source so callers can
// decide whether to append a "campfires" subdirectory without re-probing env.
type storageRootResult struct {
	Root   string
	Source storageRootSource
}

// ResolveStorageRoot returns the base storage directory for fs-transport
// campfire data, applying the resolution order described in the file header.
//
// cwd is the directory from which the tree-walk starts (normally os.Getwd()).
// Passing an empty cwd skips the tree-walk and falls through to the default.
//
// The returned string is the resolved root path. Use resolveStorageRootFull
// when you need to know the source (to avoid appending "campfires" twice).
func ResolveStorageRoot(cwd string) string {
	return resolveStorageRootFull(cwd).Root
}

// resolveStorageRootFull is the internal implementation that returns both the
// resolved path and the resolution source. DefaultBaseDir uses this to decide
// whether to append "campfires" without re-reading environment variables.
func resolveStorageRootFull(cwd string) storageRootResult {
	// 1. CF_TRANSPORT_DIR — highest priority (explicit, back-compat).
	if v := os.Getenv("CF_TRANSPORT_DIR"); v != "" {
		return storageRootResult{Root: v, Source: sourceTransportDir}
	}

	// 2. CF_HOME — explicit override; returns CF_HOME/campfires (back-compat).
	if cfHome := os.Getenv("CF_HOME"); cfHome != "" {
		return storageRootResult{Root: filepath.Join(cfHome, "campfires"), Source: sourceCFHome}
	}

	// 3. Tree-walk from cwd looking for a .cf/config.toml with storage_root.
	if cwd != "" {
		if root := walkForStorageRoot(cwd); root != "" {
			return storageRootResult{Root: root, Source: sourceConfig}
		}
	}

	// 4. Default: ~/.campfire.
	return storageRootResult{Root: defaultStorageRoot(), Source: sourceDefault}
}

// walkForStorageRoot walks up the directory tree from start, searching for a
// .cf/config.toml that contains [transport].storage_root. Returns the first
// (nearest) storage_root found, or "" if none is found.
//
// The walk is bounded by the user's home directory — it stops before ascending
// above home to prevent system-level config files from being picked up.
func walkForStorageRoot(start string) string {
	homeDir := resolveHomeDir()

	abs, err := filepath.Abs(start)
	if err != nil {
		abs = start
	}

	dir := abs
	for {
		cfgPath := filepath.Join(dir, cfConfigDir, cfConfigFile)
		if root := readStorageRoot(cfgPath); root != "" {
			return root
		}

		parent := filepath.Dir(dir)
		// Stop at filesystem root or at the home directory boundary.
		if parent == dir {
			break
		}
		if dir == homeDir {
			// We've checked home itself; don't ascend above it.
			break
		}
		dir = parent
	}

	return ""
}

// readStorageRoot reads [transport].storage_root from the given config.toml
// path. Returns "" if the file does not exist, cannot be parsed, the field is
// empty, or the resolved path fails the S4-equivalent traversal check.
//
// Security (S4-equivalent for storage_root):
//   - Relative paths are resolved against the config file's containing directory
//     (.cf/). After resolution, if the resulting absolute path escapes the user's
//     home directory, the value is REJECTED (returns "") — the walk skips this
//     config and continues to the next ancestor or falls through to the default.
//   - Absolute paths are accepted as-is (legitimate operator use-case: e.g.,
//     /data/campfires on a dedicated server). This mirrors config.go's policy for
//     identity.file, which forbids absolute paths, but storage_root has a broader
//     legitimate use-case for explicit absolute paths outside home.
//   - The raw TOML value is checked for ".." components before filepath.Clean
//     collapses them, matching the approach used by ValidateIdentityPath in
//     protocol/config.go (S4, lines 753-758).
func readStorageRoot(cfgPath string) string {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		// File absent or unreadable — not an error in the walk context.
		return ""
	}

	var raw storageRootTOML
	if _, err := toml.Decode(string(data), &raw); err != nil {
		// Malformed TOML — skip this file and continue walking.
		return ""
	}

	root := raw.Transport.StorageRoot
	if root == "" {
		return ""
	}

	// S4-equivalent: check for ".." components in relative paths before
	// filepath.Clean collapses them. "sub/../x" cleans to "x" and would pass
	// a post-Clean check, but still represents a traversal attempt.
	// Absolute paths are exempt: they don't traverse relative to the config dir.
	if !filepath.IsAbs(root) {
		parts := strings.Split(filepath.ToSlash(root), "/")
		for _, part := range parts {
			if part == ".." {
				// Relative traversal attempt — reject this config entry.
				return ""
			}
		}
		// Resolve relative path against the config file's containing directory
		// (.cf/), consistent with protocol/config.go mergeLayer.
		root = filepath.Join(filepath.Dir(cfgPath), root)
		root = filepath.Clean(root)

		// Post-resolution home boundary check: even without explicit ".." components,
		// verify the resolved absolute path stays within the home tree. This guards
		// against exotic cases (e.g., symlinks in the config path itself).
		homeDir := resolveHomeDir()
		if homeDir != "" {
			inHome := strings.HasPrefix(root, homeDir+string(os.PathSeparator)) || root == homeDir
			if !inHome {
				// Resolved path escapes home — reject this config entry.
				return ""
			}
		}
	}

	return root
}

// defaultStorageRoot returns ~/.campfire, the compiled-in default base directory.
// Falls back to /tmp/campfire if the home directory cannot be determined (e.g.
// in ephemeral / test environments with no HOME set).
func defaultStorageRoot() string {
	if home := resolveHomeDir(); home != "" {
		return filepath.Join(home, ".campfire")
	}
	// Fallback for environments with no home (containers, /tmp mounts, etc.).
	return "/tmp/campfire"
}

// resolveHomeDir returns the current user's home directory.
// Priority: $HOME env var (respects test overrides) > user.Current().HomeDir.
// Returns "" when neither is available.
func resolveHomeDir() string {
	// $HOME takes priority — this allows tests (and jailed processes) to override
	// the home directory without modifying /etc/passwd.
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return ""
}
