package protocol

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/projection"
	"github.com/campfire-net/campfire/pkg/store"
)

// userHomeDirFn is the function used to resolve the user's home directory.
// It defaults to os.UserHomeDir and can be overridden in tests.
var userHomeDirFn = os.UserHomeDir

// resolveGlobalDir returns the global campfire home directory.
// Order: WithConfigDir option > CF_HOME env var > ~/.cf
func resolveGlobalDir(opts options) (string, error) {
	if opts.configDir != "" {
		abs, err := filepath.Abs(opts.configDir)
		if err != nil {
			return "", fmt.Errorf("protocol.InitWithConfig: resolving configDir: %w", err)
		}
		return abs, nil
	}
	if env := os.Getenv("CF_HOME"); env != "" {
		abs, err := filepath.Abs(env)
		if err != nil {
			return "", fmt.Errorf("protocol.InitWithConfig: resolving CF_HOME: %w", err)
		}
		return abs, nil
	}
	home, err := userHomeDirFn()
	if err != nil {
		return "", fmt.Errorf("protocol.InitWithConfig: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".cf"), nil
}

// InitWithConfig discovers and merges config files from the cascade (global
// ~/.cf/config.toml, ancestor .cf/config.toml files, and CWD .cf/config.toml),
// translates the merged config into Option values, and delegates to Init().
//
// This is the recommended entry point for 0.16+ callers. The older Init(configDir)
// signature continues to work without modification.
//
// Option precedence (highest wins):
//  1. opts passed to InitWithConfig() — explicit always wins
//  2. Config files in the cascade
//  3. Compiled-in defaults
//
// The returned *InitResult is extended with ConfigLayers, IdentitySource, and
// AutoJoined compared to Init(). All other InitResult fields behave identically.
func InitWithConfig(optFuncs ...Option) (*Client, *InitResult, error) {
	// Build options to read WithConfigDir and any caller-supplied overrides.
	opts := defaultOptions()
	for _, fn := range optFuncs {
		fn(&opts)
	}

	// Resolve global config directory.
	globalDir, err := resolveGlobalDir(opts)
	if err != nil {
		return nil, nil, err
	}

	// CWD as project dir for cascade walk.
	cwd, err := os.Getwd()
	if err != nil {
		cwd = globalDir
	}

	// Load and merge the config cascade.
	cfg, layers, cfgWarnings, err := LoadConfig(globalDir, cwd)
	if err != nil {
		return nil, nil, fmt.Errorf("protocol.InitWithConfig: loading config: %w", err)
	}

	// Resolve domain references in naming config (naming.root, naming.seeds).
	// This must happen before the config is consumed, so downstream code sees
	// canonical 64-hex campfire IDs.
	namingWarnings, err := resolveNamingConfig(&cfg.Naming)
	if err != nil {
		return nil, nil, fmt.Errorf("protocol.InitWithConfig: %w", err)
	}
	cfgWarnings = append(cfgWarnings, namingWarnings...)

	// Determine IdentitySource: did any config layer contribute identity.file?
	identitySource := "default"
	for _, l := range layers {
		if !l.Skipped {
			for _, f := range l.Fields {
				if f == "identity.file" {
					identitySource = "config"
					break
				}
			}
		}
		if identitySource == "config" {
			break
		}
	}
	// CF_HOME / env resolution qualifies as "env".
	if identitySource == "default" && (opts.configDir != "" || os.Getenv("CF_HOME") != "") {
		identitySource = "env"
	}

	// Translate config into options, but only when the caller has NOT already
	// set the same option explicitly. We achieve this by applying config-derived
	// options first, then re-applying the caller's opts on top.
	var configOpts []Option

	// transport.endpoint → WithRemote (only when set in config and non-default)
	if cfg.Transport.Endpoint != "" && cfg.Transport.Endpoint != defaultTransportEndpoint {
		configOpts = append(configOpts, WithRemote(cfg.Transport.Endpoint))
	}

	// behavior.walk_up → WithWalkUp
	if cfg.Behavior.WalkUp {
		configOpts = append(configOpts, WithWalkUp())
	}

	// identity.present_as → WithPresentAs
	if cfg.Identity.PresentAs != "" {
		configOpts = append(configOpts, WithPresentAs(cfg.Identity.PresentAs))
	}

	// Build final option list: config-derived first, then caller overrides.
	finalOpts := append(configOpts, optFuncs...)

	// Resolve the merged options so we can read fields (e.g. presentAs) that
	// Init() consumes but does not surface in *InitResult.
	mergedOpts := defaultOptions()
	for _, fn := range finalOpts {
		fn(&mergedOpts)
	}

	// Delegate to the existing Init() using the globalDir as configDir.
	client, result, err := Init(globalDir, finalOpts...)
	if err != nil {
		return nil, nil, err
	}

	// Populate extended InitResult fields.
	result.ConfigLayers = layers
	result.IdentitySource = identitySource
	result.Warnings = append(result.Warnings, cfgWarnings...)
	result.PresentAs = mergedOpts.presentAs

	// Apply scope config from the merged config cascade when no WithScope was
	// provided. WithScope wins because it flows through Init() via opts.scope.
	if len(mergedOpts.scope.Campfires) == 0 && len(mergedOpts.scope.OperationClasses) == 0 {
		client.applyEnforcer(cfg.Scope)
	}

	// Auto-join campfires listed in behavior.auto_join.
	for _, addr := range cfg.Behavior.AutoJoin {
		joined, warn := client.autoJoinEntry(addr)
		if warn != "" {
			result.Warnings = append(result.Warnings, warn)
		}
		if joined {
			result.AutoJoined = append(result.AutoJoined, addr)
		}
	}

	return client, result, nil
}

const identityFilename = "identity.json"

// InitResult reports what protocol.Init() did during initialization.
// Every field is informational — callers that ignore InitResult continue working.
//
// IdentityPath is always populated (never empty) so an agent always knows
// which keypair it is using.
type InitResult struct {
	// IdentityCreated is true when a new Ed25519 keypair was generated.
	// False means an existing identity was loaded.
	IdentityCreated bool

	// IdentityPath is the absolute path to the identity.json file that was
	// loaded or created. Always populated.
	IdentityPath string

	// StorePath is the absolute path to the SQLite store file.
	StorePath string

	// DelegationIssued is true when a context key delegation was posted to the
	// center campfire during this Init() call (walk-up found a center and the
	// context key did not already exist).
	DelegationIssued bool

	// Recentered is true when a two-signature recenter claim was posted to the
	// center campfire during this Init() call.
	Recentered bool

	// WalkUpPath contains the directories examined during the walk-up search
	// for a center campfire sentinel. Empty when walk-up is disabled (default).
	WalkUpPath []string

	// Warnings contains non-fatal diagnostic messages produced during Init().
	Warnings []string

	// ConfigLayers lists every config file examined during the cascade,
	// including files that were found but skipped (Skipped=true + SkipReason).
	// Populated only by InitWithConfig(); empty when Init() is called directly.
	ConfigLayers []ConfigLayer

	// IdentitySource describes how the identity path was determined.
	// "config" — identity.file was set in a config file.
	// "env"    — identity path was derived from CF_HOME or an env override.
	// "default" — compiled-in default was used (no config file set it).
	// Populated only by InitWithConfig(); empty when Init() is called directly.
	IdentitySource string

	// AutoJoined contains the campfire IDs that were auto-joined during Init()
	// because they appeared in behavior.auto_join in the config cascade.
	// Populated only by InitWithConfig(); empty when Init() is called directly.
	AutoJoined []string

	// PresentAs is the campfire ID this agent presents as, sourced from
	// identity.present_as in the config cascade (or WithPresentAs option).
	// Empty when not configured. In 0.16 the value is preserved but the
	// signing behavior it enables is deferred to 0.17+.
	// Populated only by InitWithConfig(); empty when Init() is called directly.
	PresentAs string
}

// Init opens or creates a fully-functional *Client backed by an Ed25519
// identity and a SQLite store, both rooted at configDir.
//
// Identity lifecycle:
//   - If configDir/identity.json exists it is loaded (idempotent).
//   - If it does not exist, a new Ed25519 keypair is generated and persisted.
//
// Store lifecycle:
//   - The SQLite store is opened (or created) at configDir/store.db.
//   - All schema migrations are applied automatically by store.Open.
//
// Optional configuration is supplied via functional options:
//   - WithAuthorizeFunc(fn) — registers a hook called when authorization is required.
//   - WithRemote(url)       — configures a remote HTTP transport endpoint.
//   - WithWalkUp()          — enables parent-directory walk-up for center discovery (opt-in).
//   - WithNoWalkUp()        — deprecated: walk-up is now off by default; this is a no-op.
//
// The returned *InitResult is always non-nil when err is nil.
// InitResult.IdentityPath is always populated — never empty.
//
// The caller is responsible for calling Close on the returned *Client when done.
func Init(configDir string, optFuncs ...Option) (*Client, *InitResult, error) {
	opts := defaultOptions()
	for _, fn := range optFuncs {
		fn(&opts)
	}

	idPath := filepath.Join(configDir, identityFilename)

	result := &InitResult{
		IdentityPath: idPath,
		StorePath:    filepath.Join(configDir, "store.db"),
	}

	var id *identity.Identity
	if identity.Exists(idPath) {
		loaded, err := identity.Load(idPath)
		if err != nil {
			return nil, nil, fmt.Errorf("protocol.Init: loading identity: %w", err)
		}
		id = loaded
		result.IdentityCreated = false
	} else {
		generated, err := identity.Generate()
		if err != nil {
			return nil, nil, fmt.Errorf("protocol.Init: generating identity: %w", err)
		}
		if err := generated.Save(idPath); err != nil {
			return nil, nil, fmt.Errorf("protocol.Init: saving identity: %w", err)
		}
		id = generated
		result.IdentityCreated = true
	}

	rawStore, err := store.Open(result.StorePath)
	if err != nil {
		return nil, nil, fmt.Errorf("protocol.Init: opening store: %w", err)
	}

	// Wrap store with ProjectionMiddleware to maintain named filter projection views.
	// The middleware intercepts AddMessage for on-write views and provides lazy
	// delta evaluation for all views via ReadView.
	s := projection.New(rawStore)

	c := New(s, id)
	c.opts = opts
	c.configDir = configDir
	c.applyEnforcer(opts.scope)
	c.profileCache = NewProfileCache(configDir)

	// Collect WalkUpPath when walk-up is enabled.
	if opts.walkUp {
		result.WalkUpPath = collectWalkUpPath(configDir)
	}

	// Issue context key delegation if a center campfire is found in the walk-up path.
	// Best-effort: errors are ignored so Init() never fails solely because delegation
	// is unavailable (e.g. center campfire not yet in store).
	// Detect whether delegation was newly issued by checking for the context key file
	// before and after.
	if opts.walkUp {
		campfireDir := filepath.Join(configDir, campfireSubdir)
		contextKeyPubPath := filepath.Join(campfireDir, contextKeyPubFile)
		beforeDelegation := fileExists(contextKeyPubPath)
		c.maybeIssueContextKeyDelegation(configDir) //nolint:errcheck
		if !beforeDelegation && fileExists(contextKeyPubPath) {
			result.DelegationIssued = true
		}
	} else {
		c.maybeIssueContextKeyDelegation(configDir) //nolint:errcheck
	}

	// Recentering slide-in: detect existing center via walk-up, optionally
	// prompt once, post two-signature claim. Non-fatal — Init always succeeds.
	// Detect whether recenter happened by checking for the claimed state file.
	if opts.walkUp {
		claimedPath := filepath.Join(configDir, recenterClaimedFile)
		beforeRecenter := fileExists(claimedPath)
		_ = c.maybeRecenter(configDir)
		if !beforeRecenter && fileExists(claimedPath) {
			result.Recentered = true
		}
	} else {
		_ = c.maybeRecenter(configDir)
	}

	return c, result, nil
}

// autoJoinEntry attempts to auto-join the campfire identified by addr.
// addr may be a 64-hex ID, a "beacon:BASE64" string, or a cf:// URI.
//
// Returns (true, "") on success, (false, warning) on failure.
// Returns (false, "") when addr is already a member (skip, no warning).
func (c *Client) autoJoinEntry(addr string) (joined bool, warning string) {
	// Resolve the campfire ID (and extract transport hint for beacons).
	campfireID, hint, resolveErr := resolveInput(addr, c.opts.namingResolver)
	if resolveErr != nil {
		return false, fmt.Sprintf("auto_join %q: resolving address: %v", addr, resolveErr)
	}

	// Skip if already a member — idempotent across sessions.
	if m, _ := c.store.GetMembership(campfireID); m != nil {
		return false, ""
	}

	// Build a JoinRequest with the appropriate transport from the beacon hint.
	req := JoinRequest{CampfireID: campfireID}
	if hint != nil {
		switch hint.Transport {
		case "p2p-http", "http":
			if hint.Endpoint != "" {
				req.Transport = P2PHTTPTransport{PeerEndpoint: hint.Endpoint}
			}
		case "filesystem", "fs", "":
			// Filesystem beacons carry the dir in the config map, not in hint.Endpoint.
			if dir := extractFSDir(addr); dir != "" {
				req.Transport = FilesystemTransport{Dir: dir}
			}
		}
	}

	if _, joinErr := c.Join(req); joinErr != nil {
		// Produce an actionable message that tells the user how to join manually.
		shortID := campfireID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		return false, fmt.Sprintf(
			"auto_join %q: could not join (%v) — run 'cf join %s' to join manually",
			addr, joinErr, shortID,
		)
	}
	return true, ""
}

// extractFSDir attempts to extract the filesystem transport directory from a
// beacon string. Returns "" on any error or if the transport is not filesystem.
func extractFSDir(addr string) string {
	lower := strings.ToLower(addr)
	var beaconData string
	if strings.HasPrefix(lower, "beacon:") {
		beaconData = addr[len("beacon:"):]
	} else if strings.HasPrefix(lower, "cf+beacon://") {
		beaconData = addr[len("cf+beacon://"):]
	} else {
		return ""
	}
	b, err := decodeBeaconBase64(beaconData)
	if err != nil {
		return ""
	}
	if b.Transport.Protocol != "filesystem" {
		return ""
	}
	if d, ok := b.Transport.Config["dir"]; ok {
		return d
	}
	return ""
}

// collectWalkUpPath returns the sequence of directories examined during a
// walk-up search starting from startDir, up to the filesystem root.
func collectWalkUpPath(startDir string) []string {
	resolved, err := filepath.EvalSymlinks(startDir)
	if err != nil {
		resolved = startDir
	}

	var dirs []string
	dir := resolved
	for {
		dirs = append(dirs, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dirs
}

// fileExists reports whether path exists on disk.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Close releases resources held by the Client (currently the underlying store).
// It is safe to call Close multiple times; subsequent calls return the first error.
func (c *Client) Close() error {
	if c.store == nil {
		return nil
	}
	return c.store.Close()
}
