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

	// configDir, when non-empty, overrides the global config directory used by
	// InitWithConfig(). Useful for testing or non-standard home layouts.
	configDir string

	// namingResolver, when non-nil, enables cf:// URI resolution in all SDK
	// methods. Injected via WithNamingResolver.
	namingResolver NamingResolver

	// presentAs, when non-empty, is the campfire ID this agent presents as.
	// Stored for 0.17+ "present as" signing behavior; 0.16 preserves it but
	// does not act on it yet.
	presentAs string

	// scope, when set (non-zero Campfires or OperationClasses), restricts which
	// campfires and operation classes this client may access.
	scope ScopeConfig

	// backend specifies the signing backend for root-identity operations.
	// Valid values: "file" (default) or "ssh-agent".
	// When "ssh-agent", fingerprint must also be set.
	backend string

	// fingerprint is the SHA256 fingerprint of the ssh-agent key to use for signing.
	// Required when backend = "ssh-agent". Format: "SHA256:...".
	fingerprint string
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

// WithConfigDir overrides the global config directory used by InitWithConfig().
// By default InitWithConfig() uses ~/.cf (or the CF_HOME environment variable).
// This option is an escape hatch for testing and non-standard home layouts.
// It has no effect on Init(), which takes configDir as an explicit argument.
func WithConfigDir(dir string) Option {
	return func(o *options) {
		o.configDir = dir
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

// WithPresentAs sets the campfire ID this agent presents as. The value is
// stored in InitResult.PresentAs and forwarded to the client for 0.17+ signing
// support. In 0.16 the field is preserved but not yet acted upon.
func WithPresentAs(campfireID string) Option {
	return func(o *options) {
		o.presentAs = campfireID
	}
}

// WithScope restricts this client to the campfires and operation classes in cfg.
// When cfg.Campfires and cfg.OperationClasses are both empty, no restriction is
// applied (unrestricted access — same as not calling WithScope at all).
func WithScope(cfg ScopeConfig) Option {
	return func(o *options) {
		o.scope = cfg
	}
}

// WithBackend sets the signing backend for root-identity operations.
// Valid values: "file" (default) or "ssh-agent".
// When "ssh-agent" is specified, WithFingerprint must also be provided.
func WithBackend(backend string) Option {
	return func(o *options) {
		o.backend = backend
	}
}

// WithFingerprint sets the SHA256 fingerprint of the ssh-agent key to use
// for signing. Required when backend = "ssh-agent". Format: "SHA256:...".
func WithFingerprint(fingerprint string) Option {
	return func(o *options) {
		o.fingerprint = fingerprint
	}
}
