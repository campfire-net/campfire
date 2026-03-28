// Package hosting provides operator identity resolution and sign-up backed by
// Forge accounts. campfire-hosting uses this package to authenticate operators
// at request time and to provision new operator accounts.
package hosting

import (
	"context"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
)

// OperatorIdentity holds the resolved identity of an operator whose API key
// has been validated against Forge.
type OperatorIdentity struct {
	AccountID string
	Name      string
	Role      string
}

// ForgeKeyResolver is the subset of forge.Client methods needed to resolve a key.
// Defining an interface here lets tests inject a mock without a real Forge server.
type ForgeKeyResolver interface {
	ResolveKey(ctx context.Context, apiKey string) (forge.KeyRecord, error)
}

// IdentityResolver resolves an operator API key to an OperatorIdentity.
type IdentityResolver interface {
	ResolveKey(ctx context.Context, apiKey string) (OperatorIdentity, error)
}

// cacheEntry holds a resolved identity plus its expiry time.
type cacheEntry struct {
	identity  OperatorIdentity
	expiresAt time.Time
}

// ForgeIdentityResolver implements IdentityResolver using a Forge API client.
// Resolved keys are cached in-memory with a configurable TTL to avoid
// per-request round-trips to Forge.
type ForgeIdentityResolver struct {
	client ForgeKeyResolver

	// TTL is how long a resolved identity is considered fresh.
	// Defaults to 5 minutes if zero.
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry

	// now is injectable for testing; defaults to time.Now.
	now func() time.Time
}

// NewForgeIdentityResolver returns a ForgeIdentityResolver backed by client.
func NewForgeIdentityResolver(client ForgeKeyResolver) *ForgeIdentityResolver {
	return &ForgeIdentityResolver{
		client: client,
		cache:  make(map[string]cacheEntry),
		now:    time.Now,
	}
}

func (r *ForgeIdentityResolver) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return 5 * time.Minute
}

// ResolveKey resolves an operator API key to an OperatorIdentity by calling
// Forge's GET /v1/keys with the key as the bearer token. Resolved identities
// are cached for TTL (default 5 min). Returns an error if the key is invalid
// or Forge is unreachable.
func (r *ForgeIdentityResolver) ResolveKey(ctx context.Context, apiKey string) (OperatorIdentity, error) {
	now := r.now()

	// Check cache under lock.
	r.mu.Lock()
	entry, found := r.cache[apiKey]
	r.mu.Unlock()

	if found && now.Before(entry.expiresAt) {
		return entry.identity, nil
	}

	// Cache miss or expired — call Forge.
	rec, err := r.client.ResolveKey(ctx, apiKey)
	if err != nil {
		return OperatorIdentity{}, err
	}

	identity := OperatorIdentity{
		AccountID: rec.AccountID,
		Role:      rec.Role,
	}

	// Store in cache.
	r.mu.Lock()
	r.cache[apiKey] = cacheEntry{
		identity:  identity,
		expiresAt: r.now().Add(r.ttl()),
	}
	r.mu.Unlock()

	return identity, nil
}
