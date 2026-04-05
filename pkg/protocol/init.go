package protocol

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/projection"
	"github.com/campfire-net/campfire/pkg/store"
)

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
