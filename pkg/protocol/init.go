package protocol

import (
	"fmt"
	"path/filepath"

	"github.com/campfire-net/campfire/pkg/identity"
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
// The caller is responsible for calling Close on the returned *Client when done.
func Init(configDir string) (*Client, error) {
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
	s, err := store.Open(storePath)
	if err != nil {
		return nil, fmt.Errorf("protocol.Init: opening store: %w", err)
	}

	return New(s, id), nil
}

// Close releases resources held by the Client (currently the underlying store).
// It is safe to call Close multiple times; subsequent calls return the first error.
func (c *Client) Close() error {
	if c.store == nil {
		return nil
	}
	return c.store.Close()
}
