package hosting

import (
	"context"
	"errors"
)

// ErrAnonymousDurableStorage is returned when an anonymous (unauthenticated)
// client attempts to write a durable message. It maps to HTTP 401 at the
// handler layer — the client must supply a valid operator API key.
var ErrAnonymousDurableStorage = errors.New("durable storage requires operator identity: provide a valid API key")

// IdentityGate enforces that only operators with a valid Forge identity can
// write to durable storage. It is a precondition check; identity resolution
// is handled upstream (see ForgeIdentityResolver) and billing enforcement is
// downstream (billing gate).
type IdentityGate struct{}

// RequireIdentity returns nil when identity is non-nil and has a non-empty
// AccountID, indicating a valid operator. Returns ErrAnonymousDurableStorage
// for nil identity or empty AccountID (anonymous session).
func (g *IdentityGate) RequireIdentity(ctx context.Context, identity *OperatorIdentity) error {
	if identity == nil || identity.AccountID == "" {
		return ErrAnonymousDurableStorage
	}
	return nil
}
