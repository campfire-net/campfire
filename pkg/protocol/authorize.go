package protocol

import "fmt"

// Authorize invokes the registered authorize hook with the given description.
// It is called internally by SDK operations that require authorization (e.g.
// center linking, recentering, quorum calls).
//
// If no authorize hook is registered, Authorize returns (false, nil) — the
// operation is denied silently. Callers should treat a false return as a
// permission refusal.
//
// The description must be non-empty; it is displayed to the user by whatever
// UX the app has registered (terminal prompt, modal, push notification, etc.).
func (c *Client) Authorize(description string) (bool, error) {
	if description == "" {
		return false, fmt.Errorf("Authorize: description must not be empty")
	}
	if c.opts.authorizeFunc == nil {
		return false, nil
	}
	return c.opts.authorizeFunc(description)
}

// RemoteURL returns the configured remote HTTP transport URL, or an empty
// string if no remote was configured via WithRemote.
func (c *Client) RemoteURL() string {
	return c.opts.remoteURL
}

// WalkUpEnabled reports whether parent-directory walk-up is enabled for
// center campfire discovery. Defaults to true; disabled by WithWalkUp(false).
func (c *Client) WalkUpEnabled() bool {
	return c.opts.walkUp
}
