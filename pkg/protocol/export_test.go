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

// ResolveInputForTest exposes the internal resolveInput function for unit testing.
// This export is test-only — resolveInput is an internal function.
func ResolveInputForTest(s string, resolver NamingResolver) (campfireID string, hint *TransportHint, err error) {
	return resolveInput(s, resolver)
}
