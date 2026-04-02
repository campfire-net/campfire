package protocol

import (
	"fmt"
	"path/filepath"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/projection"
	"github.com/campfire-net/campfire/pkg/store"
)

const identityFilename = "identity.json"

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
//   - WithNoWalkUp()        — disables parent-directory walk-up for center discovery.
//
// The caller is responsible for calling Close on the returned *Client when done.
func Init(configDir string, optFuncs ...Option) (*Client, error) {
	opts := defaultOptions()
	for _, fn := range optFuncs {
		fn(&opts)
	}

	idPath := filepath.Join(configDir, identityFilename)

	var id *identity.Identity
	if identity.Exists(idPath) {
		loaded, err := identity.Load(idPath)
		if err != nil {
			return nil, fmt.Errorf("protocol.Init: loading identity: %w", err)
		}
		id = loaded
	} else {
		generated, err := identity.Generate()
		if err != nil {
			return nil, fmt.Errorf("protocol.Init: generating identity: %w", err)
		}
		if err := generated.Save(idPath); err != nil {
			return nil, fmt.Errorf("protocol.Init: saving identity: %w", err)
		}
		id = generated
	}

	storePath := filepath.Join(configDir, "store.db")
	rawStore, err := store.Open(storePath)
	if err != nil {
		return nil, fmt.Errorf("protocol.Init: opening store: %w", err)
	}

	// Wrap store with ProjectionMiddleware to maintain named filter projection views.
	// The middleware intercepts AddMessage for on-write views and provides lazy
	// delta evaluation for all views via ReadView.
	s := projection.New(rawStore)

	c := New(s, id)
	c.opts = opts
	c.configDir = configDir

	// Issue context key delegation if a center campfire is found in the walk-up path.
	// Best-effort: errors are ignored so Init() never fails solely because delegation
	// is unavailable (e.g. center campfire not yet in store).
	c.maybeIssueContextKeyDelegation(configDir) //nolint:errcheck

	// Recentering slide-in: detect existing center via walk-up, optionally
	// prompt once, post two-signature claim. Non-fatal — Init always succeeds.
	_ = c.maybeRecenter(configDir)

	return c, nil
}

// Close releases resources held by the Client (currently the underlying store).
// It is safe to call Close multiple times; subsequent calls return the first error.
func (c *Client) Close() error {
	if c.store == nil {
		return nil
	}
	return c.store.Close()
}
