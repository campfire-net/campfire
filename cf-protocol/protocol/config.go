package protocol

// config.go — TOML config cascade loader for campfire 0.16.
//
// Resolution order (lowest to highest priority):
//  1. Compiled-in defaults
//  2. ~/.cf/config.toml         (global user config)
//  3. <ancestor-N>/.cf/config.toml (furthest ancestor with .cf/)
//  4. ...
//  5. <ancestor-1>/.cf/config.toml (nearest ancestor)
//  6. ./.cf/config.toml         (project config at CWD)
//
// Security model: S1-S7 from 0.16 design doc.
// Config files seed protocol INPUTS only — never outputs (trust, roles).

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// compiled-in defaults
const (
	defaultIdentityFile     = "identity.json"
	defaultStoreFile        = "store.db"
	defaultTransportType    = "http"
	defaultTransportEndpoint = "https://mcp.getcampfire.dev"
	configFilename          = "config.toml"
	cfDir                   = ".cf"
)

// rawConfig is the TOML-parsed structure before security validation and merge.
// Unexported — callers use Config after merge.
type rawConfig struct {
	Identity  rawIdentityConfig  `toml:"identity"`
	Store     rawStoreConfig     `toml:"store"`
	Transport rawTransportConfig `toml:"transport"`
	Naming    rawNamingConfig    `toml:"naming"`
	Behavior  rawBehaviorConfig  `toml:"behavior"`
	Scope     rawScopeConfig     `toml:"scope"`
	// Prohibited sections — detected by custom decoder.
	Trust interface{} `toml:"trust"`
	Roles interface{} `toml:"roles"`
}

type rawScopeConfig struct {
	Campfires        []string `toml:"campfires"`
	OperationClasses []string `toml:"operation_classes"`
}

type rawTrustConfig struct {
	Anchors []string `toml:"anchors"`
}

type rawIdentityConfig struct {
	File        string         `toml:"file"`
	DisplayName string         `toml:"display_name"`
	PresentAs   string         `toml:"present_as"`
	Trust       rawTrustConfig `toml:"trust"`
	Backend     string         `toml:"backend"`
	Fingerprint string         `toml:"fingerprint"`
}

type rawStoreConfig struct {
	File string `toml:"file"`
}

type rawTransportConfig struct {
	Type     string `toml:"type"`
	Endpoint string `toml:"endpoint"`
	Dir      string `toml:"dir"`
	Relay    string `toml:"relay"`
}

type rawNamingConfig struct {
	Root  string   `toml:"root"`
	Seeds []string `toml:"seeds"`
}

type rawBehaviorConfig struct {
	WalkUp   *bool    `toml:"walk_up"`
	AutoJoin []string `toml:"auto_join"`
}

// ScopeConfig limits which campfires and operation classes an agent may access.
// All fields are optional — omitting scope means unrestricted.
type ScopeConfig struct {
	// Campfires is an explicit allowlist of campfire IDs (64-hex) the agent
	// may interact with. Empty = allow all.
	Campfires []string `toml:"campfires"`

	// OperationClasses lists which SDK operation classes are permitted.
	// Values: "read", "write", "admin", "identity".
	// Empty = allow all.
	OperationClasses []string `toml:"operation_classes"`
}

// Config is the resolved, merged configuration after cascade application.
// All fields are ready to use — security-checked and merged from all layers.
type Config struct {
	Identity  IdentityConfig
	Store     StoreConfig
	Transport TransportConfig
	Naming    NamingConfig
	Behavior  BehaviorConfig
	Scope     ScopeConfig
}

// IdentityConfig holds identity-related configuration.
type IdentityConfig struct {
	// File is the path to the Ed25519 keypair, relative to the config file's directory.
	File string
	// DisplayName is the human-readable name sent as identity:profile on join.
	DisplayName string
	// PresentAs is the campfire ID to present as (set by cf home be, cleared by cf home be --self).
	// When non-empty, the agent presents as this campfire identity rather than its machine key.
	PresentAs string
	// TrustAnchors is the decoded list of ed25519 public keys from [identity.trust] anchors.
	// Project configs EXTEND (not override) the global anchor list.
	// Invalid anchor strings are skipped with a warning during merge.
	TrustAnchors []ed25519.PublicKey
	// Backend specifies the signing backend for root-identity operations.
	// Valid values: "file" (default) or "ssh-agent".
	// When "ssh-agent", Fingerprint must also be set.
	Backend string
	// Fingerprint is the SHA256 fingerprint of the ssh-agent key to use for signing.
	// Required when Backend = "ssh-agent". Format: "SHA256:...".
	Fingerprint string
}

// StoreConfig holds store-related configuration.
type StoreConfig struct {
	// File is the path to the SQLite store, relative to the config file's directory.
	File string
}

// TransportConfig holds transport-related configuration.
type TransportConfig struct {
	// Type is the default transport for campfire creation: "http" | "fs" | "github".
	Type string
	// Endpoint is the HTTP transport endpoint.
	Endpoint string
	// Dir is the FS transport directory.
	Dir string
	// Relay is the default relay URL used when --relay flag is omitted from cf create.
	// When non-empty, cf create passes this URL as the relay for the new campfire.
	Relay string
}

// NamingConfig holds naming-related configuration.
type NamingConfig struct {
	// Root is the global naming root campfire (global config only).
	Root string
	// Seeds is the list of additional seed registries.
	Seeds []string
}

// BehaviorConfig holds behavioral configuration.
type BehaviorConfig struct {
	// WalkUp enables parent-directory walk-up for center campfire discovery.
	WalkUp bool
	// AutoJoin is the list of campfires to auto-join on Init().
	AutoJoin []string
}

// ConfigLayer describes one config file in the cascade.
type ConfigLayer struct {
	// Path is the absolute path to the config file.
	Path string
	// Source is "global", "ancestor", or "project".
	Source string
	// Fields lists the field names this layer contributed to the merged Config.
	Fields []string
	// Skipped is true if the file was found but not loaded due to a security check.
	Skipped bool
	// SkipReason explains why the file was skipped: "ownership", "world-writable", or "symlink".
	SkipReason string
}

// LoadConfig discovers and merges config files from the cascade.
// It returns the merged Config, the list of layers examined (including skipped),
// and any non-fatal warnings.
//
// The caller provides globalDir (typically ~/.cf) and projectDir (CWD or
// the directory being initialized). Walk-up collects intermediate .cf/ directories
// between projectDir and globalDir.
func LoadConfig(globalDir, projectDir string) (*Config, []ConfigLayer, []string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = globalDir
	}

	// Start with compiled-in defaults.
	merged := compiledDefaults()

	var layers []ConfigLayer
	var warnings []string

	// 1. Global config.
	// Note: os.Stat here followed by loadAndCheck (which calls EvalSymlinks internally)
	// creates a narrow TOCTOU window. The risk is acceptable: exploiting the race
	// requires write access to the config directory, which the S1 ownership check
	// (directory must be owned by current UID) already prevents for untrusted paths.
	// An attacker with write access to an owned directory could write a malicious
	// config.toml directly without needing a race.
	globalPath := filepath.Join(globalDir, configFilename)
	if _, err := os.Stat(globalPath); err == nil {
		layer, raw, warn, err := loadAndCheck(globalPath, "global", true)
		warnings = append(warnings, warn...)
		if err != nil {
			return nil, nil, nil, err
		}
		layers = append(layers, layer)
		if !layer.Skipped {
			contributed, warn, err := mergeLayer(&merged, raw, globalPath, true)
			if err != nil {
				return nil, nil, nil, err
			}
			warnings = append(warnings, warn...)
			layers[len(layers)-1].Fields = contributed
		}
	}

	// 2. Walk-up intermediate ancestors (furthest → nearest).
	ancestors := collectAncestors(projectDir, homeDir, globalDir)
	for _, ancestorDir := range ancestors {
		path := filepath.Join(ancestorDir, cfDir, configFilename)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		layer, raw, warn, err := loadAndCheck(path, "ancestor", false)
		warnings = append(warnings, warn...)
		if err != nil {
			return nil, nil, nil, err
		}
		layers = append(layers, layer)
		if !layer.Skipped {
			contributed, warn, err := mergeLayer(&merged, raw, path, false)
			if err != nil {
				return nil, nil, nil, err
			}
			warnings = append(warnings, warn...)
			layers[len(layers)-1].Fields = contributed
		}
	}

	// 3. Project config (.cf/config.toml in projectDir).
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectPath := filepath.Join(abs, cfDir, configFilename)
		// Only load if it differs from global config location.
		if projectPath != globalPath {
			if _, err := os.Stat(projectPath); err == nil {
				layer, raw, warn, err := loadAndCheck(projectPath, "project", false)
				warnings = append(warnings, warn...)
				if err != nil {
					return nil, nil, nil, err
				}
				layers = append(layers, layer)
				if !layer.Skipped {
					contributed, warn, err := mergeLayer(&merged, raw, projectPath, false)
					if err != nil {
						return nil, nil, nil, err
					}
					warnings = append(warnings, warn...)
					layers[len(layers)-1].Fields = contributed
				}
			}
		}
	}

	// Validate: identity.fingerprint is required when identity.backend = "ssh-agent".
	// This check runs on the FINAL merged Config (after all layers are applied) so that
	// backend and fingerprint set in different config layers (e.g. backend in global config,
	// fingerprint in project config) are both visible before the check fires.
	if merged.Identity.Backend == "ssh-agent" && merged.Identity.Fingerprint == "" {
		return nil, nil, nil, fmt.Errorf("config: identity.fingerprint is required when identity.backend = \"ssh-agent\"")
	}

	return &merged, layers, warnings, nil
}

// compiled-in backend default
const defaultBackend = "file"

// compiledDefaults returns a Config with all compiled-in default values.
func compiledDefaults() Config {
	return Config{
		Identity: IdentityConfig{
			File:    defaultIdentityFile,
			Backend: defaultBackend,
		},
		Store: StoreConfig{
			File: defaultStoreFile,
		},
		Transport: TransportConfig{
			Type:     defaultTransportType,
			Endpoint: defaultTransportEndpoint,
		},
	}
}


// collectAncestors returns intermediate directories between projectDir and homeDir
// (exclusive of homeDir and globalDir), ordered furthest-first (global → local).
func collectAncestors(projectDir, homeDir, globalDir string) []string {
	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		absProject = projectDir
	}
	absHome, err := filepath.Abs(homeDir)
	if err != nil {
		absHome = homeDir
	}
	absGlobal, err := filepath.Abs(globalDir)
	if err != nil {
		absGlobal = globalDir
	}

	// Walk from projectDir up to homeDir, collecting intermediate dirs.
	var dirs []string
	dir := absProject
	for {
		parent := filepath.Dir(dir)
		if parent == dir || dir == absHome {
			break
		}
		// Skip projectDir itself and the global config dir.
		if dir != absProject && filepath.Join(dir, cfDir) != absGlobal {
			dirs = append(dirs, dir)
		}
		dir = parent
	}

	// Reverse so order is furthest-from-project → nearest-to-project.
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// loadAndCheck parses a config file and performs security checks.
// Returns the ConfigLayer (possibly skipped), the parsed rawConfig (nil if skipped),
// any warnings, and any hard errors.
func loadAndCheck(path string, source string, isGlobal bool) (ConfigLayer, *rawConfig, []string, error) {
	layer := ConfigLayer{
		Path:   path,
		Source: source,
	}

	// S3: Resolve symlinks first.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// File exists (we checked) but EvalSymlinks failed — treat as symlink issue.
		layer.Skipped = true
		layer.SkipReason = "symlink"
		return layer, nil, []string{fmt.Sprintf("skipping %s: symlink resolution failed: %v", path, err)}, nil
	}

	// S3: Symlink resolves outside home tree check.
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" && resolved != path {
		// Use HasPrefix with trailing separator to prevent sibling directory bypass.
		// e.g. /home/baron must not match /home/baronmalicious/...
		inHome := strings.HasPrefix(resolved, homeDir+string(os.PathSeparator)) || resolved == homeDir
		if !inHome {
			layer.Skipped = true
			layer.SkipReason = "symlink"
			return layer, nil, []string{fmt.Sprintf("skipping %s: symlink resolves outside home directory", path)}, nil
		}
	}

	// S1: UID ownership check on containing directory.
	dirPath := filepath.Dir(resolved)
	if !isOwnerTrusted(dirPath) {
		layer.Skipped = true
		layer.SkipReason = "ownership"
		return layer, nil, []string{fmt.Sprintf("skipping %s: directory not owned by current user", path)}, nil
	}

	// S2: World-writable check on the file itself.
	info, err := os.Stat(resolved)
	if err != nil {
		layer.Skipped = true
		layer.SkipReason = "ownership"
		return layer, nil, []string{fmt.Sprintf("skipping %s: stat failed: %v", path, err)}, nil
	}
	if info.Mode().Perm()&0o002 != 0 {
		layer.Skipped = true
		layer.SkipReason = "world-writable"
		return layer, nil, []string{fmt.Sprintf("skipping %s: file is world-writable", path)}, nil
	}

	// S2: World-writable check on the containing directory.
	dirInfo, err := os.Stat(filepath.Dir(resolved))
	if err != nil || dirInfo.Mode().Perm()&0o002 != 0 {
		layer.Skipped = true
		layer.SkipReason = "world-writable-dir"
		return layer, nil, []string{fmt.Sprintf("skipping %s: containing directory is world-writable", path)}, nil
	}

	// Parse TOML.
	var raw rawConfig
	if _, err := toml.DecodeFile(resolved, &raw); err != nil {
		return layer, nil, nil, fmt.Errorf("config %s: parse error: %w", path, err)
	}

	// S5: Reject [trust] section.
	if raw.Trust != nil {
		return layer, nil, nil, fmt.Errorf(
			"config %s: Config cannot pre-seed trust. Trust is campfire-scoped and established via vouches (protocol spec §Trust). Use behavior.auto_join to pre-seed membership attempts.",
			path,
		)
	}

	// S5: Reject [roles] section.
	if raw.Roles != nil {
		return layer, nil, nil, fmt.Errorf(
			"config %s: Config cannot pre-assign roles. Roles are set by the campfire's admission policy.",
			path,
		)
	}

	// S6: naming.root may only appear in global config.
	if !isGlobal && raw.Naming.Root != "" {
		return layer, nil, nil, fmt.Errorf(
			"config %s: naming.root may only be set in the global config (~/.cf/config.toml). Use naming.seeds for project-level name resolution contexts.",
			path,
		)
	}

	// S4: Validate identity.file on the raw TOML value, before resolveRelativePath
	// converts it to an absolute path. This catches traversal attempts like "../../etc/passwd".
	if raw.Identity.File != "" {
		if err := ValidateIdentityPath(raw.Identity.File); err != nil {
			return layer, nil, nil, fmt.Errorf("config %s: identity.file: %w", path, err)
		}
	}

	return layer, &raw, nil, nil
}

// ownerTrustedFn is the package-level owner-check function used by loadAndCheck.
// Tests may replace this variable to inject a fake owner check without
// creating files owned by a different UID (which requires root).
var ownerTrustedFn = defaultOwnerTrusted

// defaultOwnerTrusted is platform-specific — see config_unix.go and config_windows.go.

// isOwnerTrusted is the exported-name alias kept for call sites. It delegates
// to ownerTrustedFn so test injection works transparently.
func isOwnerTrusted(dirPath string) bool {
	return ownerTrustedFn(dirPath)
}

// mergeLayer applies a parsed rawConfig into the current merged Config.
// isGlobal controls whether naming.root is applied.
// Returns the list of field names contributed by this layer, any non-fatal
// warnings (e.g. skipped invalid anchors), and an error if a field value fails
// format validation.
func mergeLayer(dst *Config, raw *rawConfig, path string, isGlobal bool) ([]string, []string, error) {
	if raw == nil {
		return nil, nil, nil
	}

	var contributed []string
	var warnings []string

	// identity.file — scalar: deepest wins (non-empty overrides).
	if raw.Identity.File != "" {
		dst.Identity.File = resolveRelativePath(raw.Identity.File, filepath.Dir(path))
		contributed = append(contributed, "identity.file")
	}

	// identity.display_name — scalar.
	if raw.Identity.DisplayName != "" {
		dst.Identity.DisplayName = raw.Identity.DisplayName
		contributed = append(contributed, "identity.display_name")
	}

	// identity.backend — scalar: "file" or "ssh-agent". deepest wins.
	if raw.Identity.Backend != "" {
		if raw.Identity.Backend != "file" && raw.Identity.Backend != "ssh-agent" {
			return nil, nil, fmt.Errorf("config %s: identity.backend %q is not valid (expected \"file\" or \"ssh-agent\")", path, raw.Identity.Backend)
		}
		dst.Identity.Backend = raw.Identity.Backend
		contributed = append(contributed, "identity.backend")
	}

	// identity.fingerprint — scalar: required when backend = "ssh-agent".
	if raw.Identity.Fingerprint != "" {
		dst.Identity.Fingerprint = raw.Identity.Fingerprint
		contributed = append(contributed, "identity.fingerprint")
	}

	// identity.present_as — scalar (deepest wins; empty string clears).
	// Set by cf home be, cleared by cf home be --self.
	if raw.Identity.PresentAs != "" {
		if !hexIDRe.MatchString(raw.Identity.PresentAs) {
			return nil, nil, fmt.Errorf("config %s: identity.present_as %q is not a valid campfire ID (expected 64 lowercase hex characters)", path, raw.Identity.PresentAs)
		}
		dst.Identity.PresentAs = raw.Identity.PresentAs
		contributed = append(contributed, "identity.present_as")
	}

	// identity.trust.anchors — list: project configs EXTEND (not override) the global list.
	// Invalid anchor strings are returned as warnings and skipped (not fatal).
	if len(raw.Identity.Trust.Anchors) > 0 {
		for _, anchor := range raw.Identity.Trust.Anchors {
			key, err := decodeAnchor(anchor)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("identity.trust.anchors: skipping invalid anchor %q: %v", anchor, err))
				continue
			}
			// Deduplicate: skip if already present (byte-level equality).
			if !containsAnchor(dst.Identity.TrustAnchors, key) {
				dst.Identity.TrustAnchors = append(dst.Identity.TrustAnchors, key)
			}
		}
		contributed = append(contributed, "identity.trust.anchors")
	}

	// store.file — scalar.
	if raw.Store.File != "" {
		dst.Store.File = resolveRelativePath(raw.Store.File, filepath.Dir(path))
		contributed = append(contributed, "store.file")
	}

	// transport.type — scalar.
	if raw.Transport.Type != "" {
		dst.Transport.Type = raw.Transport.Type
		contributed = append(contributed, "transport.type")
	}

	// transport.endpoint — scalar.
	if raw.Transport.Endpoint != "" {
		dst.Transport.Endpoint = raw.Transport.Endpoint
		contributed = append(contributed, "transport.endpoint")
	}

	// transport.dir — scalar.
	if raw.Transport.Dir != "" {
		dst.Transport.Dir = raw.Transport.Dir
		contributed = append(contributed, "transport.dir")
	}

	// transport.relay — scalar.
	if raw.Transport.Relay != "" {
		dst.Transport.Relay = raw.Transport.Relay
		contributed = append(contributed, "transport.relay")
	}

	// naming.root — global-only scalar.
	if isGlobal && raw.Naming.Root != "" {
		dst.Naming.Root = raw.Naming.Root
		contributed = append(contributed, "naming.root")
	}

	// naming.seeds — list with optional "!replace" sentinel.
	if len(raw.Naming.Seeds) > 0 {
		merged := mergeList(dst.Naming.Seeds, raw.Naming.Seeds)
		dst.Naming.Seeds = merged
		contributed = append(contributed, "naming.seeds")
	}

	// behavior.walk_up — scalar pointer: apply only when the key was present in TOML.
	// Using *bool allows distinguishing "walk_up = false" (pointer to false) from
	// "key absent" (nil pointer), so a project-level false correctly overrides a
	// global true.
	if raw.Behavior.WalkUp != nil {
		dst.Behavior.WalkUp = *raw.Behavior.WalkUp
		contributed = append(contributed, "behavior.walk_up")
	}

	// behavior.auto_join — list with optional "!replace" sentinel.
	if len(raw.Behavior.AutoJoin) > 0 {
		merged := mergeList(dst.Behavior.AutoJoin, raw.Behavior.AutoJoin)
		dst.Behavior.AutoJoin = merged
		contributed = append(contributed, "behavior.auto_join")
	}

	// scope.campfires — list with optional "!replace" sentinel.
	// Security: reject bare !replace (["!replace"] with no additional entries) when
	// the global allowlist is non-empty. mergeList would produce an empty slice, and
	// CheckCampfire treats empty as "allow all" — silently converting a restrictive
	// allowlist into unrestricted access. Skip this layer field instead.
	if len(raw.Scope.Campfires) > 0 {
		if len(dst.Scope.Campfires) > 0 && len(raw.Scope.Campfires) == 1 && raw.Scope.Campfires[0] == "!replace" {
			// Bare !replace would discard the existing campfire allowlist, producing
			// allow-all semantics. Treat as a no-op to prevent accidental privilege escalation.
			// To extend the allowlist use ["!replace", "id1", ...] with at least one entry.
		} else {
			merged := mergeList(dst.Scope.Campfires, raw.Scope.Campfires)
			dst.Scope.Campfires = merged
			contributed = append(contributed, "scope.campfires")
		}
	}

	// scope.operation_classes — list with optional "!replace" sentinel.
	// Security: same bare-!replace guard as scope.campfires — a bare ["!replace"] from a
	// project config would silently discard an inherited operation-class allowlist and
	// produce unrestricted access. Treat as a no-op when dst already has an allowlist.
	if len(raw.Scope.OperationClasses) > 0 {
		if len(dst.Scope.OperationClasses) > 0 && len(raw.Scope.OperationClasses) == 1 && raw.Scope.OperationClasses[0] == "!replace" {
			// Bare !replace would discard the existing operation-class allowlist.
			// To replace the list, use ["!replace", "read", ...] with at least one entry.
		} else {
			merged := mergeList(dst.Scope.OperationClasses, raw.Scope.OperationClasses)
			dst.Scope.OperationClasses = merged
			contributed = append(contributed, "scope.operation_classes")
		}
	}

	return contributed, warnings, nil
}

// mergeList merges a new list into an existing list.
// If newList starts with "!replace", the existing list is discarded and
// "!replace" is stripped from the result.
// Otherwise, newList is appended and duplicates are removed.
func mergeList(existing, newList []string) []string {
	if len(newList) == 0 {
		return existing
	}

	if newList[0] == "!replace" {
		// Replace: discard inherited, strip sentinel.
		result := make([]string, 0, len(newList)-1)
		result = append(result, newList[1:]...)
		return result
	}

	// Append: add new entries, deduplicate by value equality.
	seen := make(map[string]bool, len(existing))
	for _, v := range existing {
		seen[v] = true
	}
	result := make([]string, len(existing))
	copy(result, existing)
	for _, v := range newList {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

// resolveRelativePath resolves a config value that is relative to the config file's
// directory. Absolute paths are returned as-is (validation is done at Init time).
func resolveRelativePath(value, configDir string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(configDir, value)
}

// decodeAnchor decodes a base64- or hex-encoded ed25519 public key string.
// Hex is detected first (only [0-9a-fA-F] chars) to avoid misinterpreting a
// hex string as base64 — both charsets overlap. Base64 is tried next (with
// padding, then without). Returns an error if decoding fails or the decoded
// bytes are not exactly ed25519.PublicKeySize (32) bytes.
func decodeAnchor(s string) (ed25519.PublicKey, error) {
	var raw []byte
	var err error

	if isHexString(s) {
		raw, err = hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("hex decode: %w", err)
		}
	} else if raw, err = base64.StdEncoding.DecodeString(s); err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(s); err != nil {
			return nil, fmt.Errorf("not valid base64 or hex: %w", err)
		}
	}

	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("expected %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// isHexString reports whether s consists solely of [0-9a-fA-F] characters.
// This is used to distinguish hex-encoded keys from base64-encoded ones before
// attempting decode — the charsets overlap (A-F appear in both).
func isHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// containsAnchor reports whether key is already present in the list (byte equality).
func containsAnchor(list []ed25519.PublicKey, key ed25519.PublicKey) bool {
	for _, existing := range list {
		if existing.Equal(key) {
			return true
		}
	}
	return false
}

// ValidateIdentityPath validates an identity.file value from config.
// Returns an error if the path is absolute or contains ".." components.
//
// S4: Identity path constraints.
func ValidateIdentityPath(identityPath string) error {
	if filepath.IsAbs(identityPath) {
		return fmt.Errorf("identity.file must be a relative path, got: %s", identityPath)
	}
	// Check raw path for ".." before cleaning, since Clean("sub/../x") == "x"
	// which would pass the cleaned check but still represents a traversal attempt.
	parts := strings.Split(filepath.ToSlash(identityPath), "/")
	for _, part := range parts {
		if part == ".." {
			return fmt.Errorf("identity.file must not contain '..' components, got: %s", identityPath)
		}
	}
	return nil
}
