// Package protocol provides a high-level client API for campfire operations.
// It unifies the duplicate read/send stacks that exist in cmd/cf, cmd/cf-mcp,
// and pkg/convention, enabling those callers to migrate to a shared implementation.
package protocol

import (
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// Client is a high-level campfire client that wraps a store and optional identity
// to provide campfire operations with correct sync-before-query semantics.
//
// For filesystem-transport campfires, operations sync from the filesystem into
// the local store before querying. For HTTP-transport campfires, messages are
// delivered via push, so sync is skipped.
//
// Client is NOT safe for concurrent use from multiple goroutines without external
// synchronization. Each goroutine should use its own Client.
type Client struct {
	store    store.Store
	identity *identity.Identity
}

// New creates a Client wrapping the given store.
// identity may be nil for read-only clients that do not need to sign messages.
func New(s store.Store, id *identity.Identity) *Client {
	return &Client{store: s, identity: id}
}

// Store returns the underlying store. Callers may use this for operations not
// yet abstracted by the Client API.
func (c *Client) Store() store.Store {
	return c.store
}
