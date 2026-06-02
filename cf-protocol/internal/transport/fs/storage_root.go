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

import (
	"os"
	"os/user"
	"path/filepath"

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

// ResolveStorageRoot returns the base storage directory for fs-transport
// campfire data, applying the resolution order described in the file header.
//
// cwd is the directory from which the tree-walk starts (normally os.Getwd()).
// Passing an empty cwd skips the tree-walk and falls through to the default.
func ResolveStorageRoot(cwd string) string {
	// 1. CF_TRANSPORT_DIR — highest priority (explicit, back-compat).
	if v := os.Getenv("CF_TRANSPORT_DIR"); v != "" {
		return v
	}

	// 2. CF_HOME — explicit override; returns CF_HOME/campfires (back-compat).
	if cfHome := os.Getenv("CF_HOME"); cfHome != "" {
		return filepath.Join(cfHome, "campfires")
	}

	// 3. Tree-walk from cwd looking for a .cf/config.toml with storage_root.
	if cwd != "" {
		if root := walkForStorageRoot(cwd); root != "" {
			return root
		}
	}

	// 4. Default: ~/.campfire.
	return defaultStorageRoot()
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
// path. Returns "" if the file does not exist, cannot be parsed, or the field
// is empty. Relative paths are resolved relative to the config file's directory.
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

	// Resolve relative paths against the config file's containing directory
	// (the .cf/ directory), consistent with protocol/config.go mergeLayer.
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(cfgPath), root)
		root = filepath.Clean(root)
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
