// export_test.go — test-only exports for the protocol_test package.
// These allow external test packages to access internal Client state
// without exposing it as part of the public API.
package protocol

import (
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// ClientStore returns the underlying store for use in tests.
func (c *Client) ClientStore() store.Store { return c.store }

// ClientIdentity returns the underlying identity for use in tests.
func (c *Client) ClientIdentity() *identity.Identity { return c.identity }
