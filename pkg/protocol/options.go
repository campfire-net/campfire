package protocol

// options holds the configuration applied to Init via functional Option values.
type options struct {
	// authorizeFunc is called when any SDK operation requires authorization.
	// It receives a human-readable description of the operation and returns
	// whether the caller approves. nil means no hook is registered.
	authorizeFunc func(description string) (bool, error)

	// remoteURL, when non-empty, configures the client to prefer an HTTP
	// remote transport at the given URL (e.g. "https://mcp.example.com").
	remoteURL string

	// walkUp controls whether Init walks up parent directories looking for
	// an existing center campfire. Default is false (walk-up opt-in).
	walkUp bool
}

// defaultOptions returns the options struct with all defaults applied.
func defaultOptions() options {
	return options{
		walkUp: false,
	}
}

// Option is a functional option for protocol.Init.
type Option func(*options)

// WithAuthorizeFunc registers a hook that the SDK calls when any operation
// requires authorization (e.g. a quorum call or center-linking). The hook
// receives a human-readable description and must return (approved, error).
// The SDK respects a false return by refusing the operation.
func WithAuthorizeFunc(fn func(description string) (bool, error)) Option {
	return func(o *options) {
		o.authorizeFunc = fn
	}
}

// WithRemote configures the client to use an HTTP remote transport at url
// (e.g. "https://mcp.example.com") as the default transport for operations
// that require a network-accessible campfire endpoint.
func WithRemote(url string) Option {
	return func(o *options) {
		o.remoteURL = url
	}
}

// WithWalkUp enables parent-directory walk-up for center campfire discovery.
// Walk-up is disabled by default (opt-in). Use this option in developer
// tooling and environments where ascending directory trees is desirable.
func WithWalkUp() Option {
	return func(o *options) {
		o.walkUp = true
	}
}

// WithNoWalkUp disables parent-directory walk-up for center campfire discovery.
//
// Deprecated: walk-up is now disabled by default (opt-in via WithWalkUp()).
// WithNoWalkUp() is a no-op on a default-initialized client and will be
// removed in a future release. Callers that relied on walk-up must now
// explicitly pass WithWalkUp() to restore the behavior.
func WithNoWalkUp() Option {
	return func(o *options) {
		o.walkUp = false
	}
}
