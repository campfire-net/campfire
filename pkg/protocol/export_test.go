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

// ResolveGlobalDirForTest exposes the internal resolveGlobalDir function for
// unit testing. This allows tests to verify the CF_HOME and default branches
// without going through the full InitWithConfig stack.
func ResolveGlobalDirForTest(configDir string) (string, error) {
	opts := defaultOptions()
	if configDir != "" {
		opts.configDir = configDir
	}
	return resolveGlobalDir(opts)
}

// SetUserHomeDirFnForTest replaces the userHomeDirFn variable for the duration
// of a test, restoring the original when the test completes. This allows tests
// to exercise the os.UserHomeDir error path in resolveGlobalDir without
// modifying the OS environment.
func SetUserHomeDirFnForTest(t interface{ Cleanup(func()) }, fn func() (string, error)) {
	orig := userHomeDirFn
	userHomeDirFn = fn
	t.Cleanup(func() { userHomeDirFn = orig })
}
