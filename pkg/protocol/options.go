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
	// an existing center campfire. Default is true (walk-up enabled).
	walkUp bool
}

// defaultOptions returns the options struct with all defaults applied.
func defaultOptions() options {
	return options{
		walkUp: true,
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

// WithNoWalkUp disables parent-directory walk-up for center campfire discovery.
// Useful in agents and containers where the directory tree is unpredictable.
// Walk-up is enabled by default.
func WithNoWalkUp() Option {
	return func(o *options) {
		o.walkUp = false
	}
}
