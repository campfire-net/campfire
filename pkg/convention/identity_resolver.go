package convention

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/convention/delegation"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// IdentityInfo holds resolved identity information for a convention handler invocation.
// It is populated by the Server before dispatching each incoming message to a handler.
type IdentityInfo struct {
	// MachineKey is the Ed25519 public key that signed the message.
	// Always populated when the message carries a valid hex sender pubkey.
	MachineKey ed25519.PublicKey

	// Identity is the verified home campfire ID, if any.
	// Empty string if no verified identity is present.
	Identity string

	// IdentityVerified is true when Identity was resolved from a verified
	// VerificationCache entry (echo ceremony confirmed).
	IdentityVerified bool

	// Chain is the sequence of delegation grants walked from the sender back to
	// the trust anchor. Nil when TrustResolved is false or no GrantChainResolver
	// is installed. Empty slice when the sender is itself a trust anchor.
	Chain []delegation.GrantInfo

	// Anchor is the trust-anchor public key at which the grant walk terminated.
	// Nil when TrustResolved is false or no GrantChainResolver is installed.
	Anchor ed25519.PublicKey

	// TrustResolved is true when a GrantChainResolver successfully resolved the
	// sender back to a trust anchor (delegation.Resolved outcome).
	TrustResolved bool

	// Provenance maps bool field names to the Name() of the resolver that first
	// set them to true. Only fields set to true have entries; false fields are
	// absent. Populated by CompositeResolver using first-setter-wins semantics.
	// Nil when resolution was performed by a non-composite resolver.
	//
	// Field names: "identity_verified", "trust_resolved".
	Provenance map[string]string
}

// IdentityResolver resolves sender pubkeys to IdentityInfo.
// The convention Server calls it for each incoming message before dispatching
// to the registered handler.
//
// The trustworthiness of IdentityVerified=true is entirely determined by the
// resolver implementation. Only use CacheIdentityResolver with a cache populated
// by the verified echo ceremony (cf home be / homeLinkCmd).
type IdentityResolver interface {
	// Resolve returns IdentityInfo for the given sender pubkey.
	// ctx is threaded for cancellation and deadline propagation.
	Resolve(ctx context.Context, senderPubkey ed25519.PublicKey) (IdentityInfo, error)

	// Name returns a stable human-readable identifier for this resolver.
	// Used by CompositeResolver to populate IdentityInfo.Provenance.
	Name() string
}

// NoopIdentityResolver returns IdentityInfo with only MachineKey set.
// Used when no VerificationCache is available. This is the default resolver.
type NoopIdentityResolver struct{}

// Name implements IdentityResolver.
func (NoopIdentityResolver) Name() string { return "noop" }

// Resolve implements IdentityResolver. It returns IdentityInfo with only
// MachineKey populated; IdentityVerified is always false.
func (NoopIdentityResolver) Resolve(_ context.Context, senderPubkey ed25519.PublicKey) (IdentityInfo, error) {
	return IdentityInfo{MachineKey: senderPubkey}, nil
}

// CacheIdentityResolver uses a protocol.VerificationCache to resolve sender
// pubkeys to verified home campfire IDs.
type CacheIdentityResolver struct {
	cache protocol.VerificationCache
}

// NewCacheIdentityResolver returns a CacheIdentityResolver backed by cache.
func NewCacheIdentityResolver(cache protocol.VerificationCache) *CacheIdentityResolver {
	return &CacheIdentityResolver{cache: cache}
}

// Name implements IdentityResolver.
func (r *CacheIdentityResolver) Name() string { return "cache" }

// Resolve implements IdentityResolver. If cache has a verified entry for
// senderPubkey, it returns IdentityInfo with Identity and IdentityVerified set.
// On a cache miss (or empty campfire ID), it returns MachineKey-only.
func (r *CacheIdentityResolver) Resolve(_ context.Context, senderPubkey ed25519.PublicKey) (IdentityInfo, error) {
	info := IdentityInfo{MachineKey: senderPubkey}
	if id, ok := r.cache.Get(senderPubkey); ok && id != "" {
		info.Identity = id
		info.IdentityVerified = true
	}
	return info, nil
}

// GrantChainResolver resolves sender pubkeys by walking the local campfire grant
// log via delegation.Resolve. It implements IdentityResolver.
//
// Servers install it via Server.WithIdentityResolver to expose req.Identity.Chain
// and req.Identity.Anchor to convention handlers. Servers without it fall back to
// NoopIdentityResolver (backward compatible: Chain is nil, TrustResolved is false).
type GrantChainResolver struct {
	client        *protocol.Client
	campfireIDHex string
	campfireIDRaw []byte // decoded once at construction for delegation.Resolve
	anchors       []ed25519.PublicKey
}

// NewGrantChainResolver returns a GrantChainResolver that walks the campfire
// identified by campfireID (hex string) using client. anchors is the list of
// trusted root pubkeys; a sender that IS an anchor resolves immediately with
// an empty chain.
//
// campfireID must be a valid 64-character hex string (32-byte Ed25519 pubkey).
// If decoding fails, the resolver is still returned but all Resolve calls will
// return TrustResolved=false.
func NewGrantChainResolver(client *protocol.Client, campfireID string, anchors []ed25519.PublicKey) *GrantChainResolver {
	raw, err := hex.DecodeString(campfireID)
	if err != nil {
		// Return a resolver that fail-closes: no campfireIDRaw means all
		// Resolve calls return TrustResolved=false until the ID is corrected.
		fmt.Fprintf(os.Stderr, "convention: NewGrantChainResolver: invalid campfire ID hex %q: %v\n", campfireID, err)
		raw = nil
	}
	return &GrantChainResolver{
		client:        client,
		campfireIDHex: campfireID,
		campfireIDRaw: raw,
		anchors:       anchors,
	}
}

// Name implements IdentityResolver.
func (r *GrantChainResolver) Name() string { return "grant-chain" }

// Resolve implements IdentityResolver. It calls delegation.Resolve threading
// the caller's context through. The current delegation.Resolve implementation
// performs synchronous local store reads and does not yet use the context
// (all reads are microsecond-fast); context is threaded here so future
// implementations can respect cancellation and deadlines without a signature
// change (campfire-6b1).
//
// On a Resolved outcome it returns TrustResolved=true with Chain and Anchor set.
// On any other outcome (DeadEnd, InvalidGrant, DepthExceeded, ReadError) it returns
// TrustResolved=false with nil Chain and nil Anchor.
func (r *GrantChainResolver) Resolve(ctx context.Context, senderPubkey ed25519.PublicKey) (IdentityInfo, error) {
	info := IdentityInfo{MachineKey: senderPubkey}
	if r.campfireIDRaw == nil {
		// campfireID was invalid at construction — fail-closed.
		return info, nil
	}
	out := delegation.Resolve(ctx, r.client, r.campfireIDRaw, senderPubkey, r.anchors)
	if resolved, ok := out.(delegation.Resolved); ok {
		info.TrustResolved = true
		info.Chain = resolved.Chain
		info.Anchor = resolved.Anchor
	}
	return info, nil
}

// FromConfig returns a GrantChainResolver that reads trust anchors from the
// provided campfire config. If cfg is nil or cfg.Identity.TrustAnchors is
// empty, a warning is printed to stderr and the resolver is returned in a
// fail-closed state: all Resolve calls return TrustResolved=false.
//
// campfireID must be a valid 64-character hex string identifying the campfire.
// client is the protocol.Client for reading the grant log.
func FromConfig(client *protocol.Client, campfireID string, cfg *protocol.Config) *GrantChainResolver {
	var anchors []ed25519.PublicKey
	if cfg != nil {
		anchors = cfg.Identity.TrustAnchors
	}
	if len(anchors) == 0 {
		fmt.Fprintf(os.Stderr, "convention: FromConfig: no trust anchors configured — resolver will fail-close (all senders untrusted)\n")
	}
	return NewGrantChainResolver(client, campfireID, anchors)
}

// CompositeResolver chains multiple IdentityResolvers. Each resolver's result
// is merged into a single IdentityInfo: any non-zero field from later resolvers
// overwrites the zero value from earlier ones, but a field already set by an
// earlier resolver is NOT overwritten by a zero value from a later one.
//
// Resolvers are called in order. The first resolver in the slice wins for any
// field it sets; subsequent resolvers fill in fields the earlier ones did not set.
//
// Provenance semantics: first-setter-wins per bool field. The resolver whose
// Name() is recorded in Provenance["identity_verified"] (or "trust_resolved")
// was the first to set that field to true. Resolver order therefore matters for
// provenance attribution.
type CompositeResolver struct {
	resolvers []IdentityResolver
}

// NewCompositeResolver returns a CompositeResolver that calls each resolver in
// order and merges their results. MachineKey is always taken from the first
// resolver in the chain (i.e., the key is decoded once from senderPubkey and
// not re-merged).
func NewCompositeResolver(resolvers ...IdentityResolver) *CompositeResolver {
	return &CompositeResolver{resolvers: resolvers}
}

// Name implements IdentityResolver.
func (c *CompositeResolver) Name() string { return "composite" }

// Resolve implements IdentityResolver. It calls each resolver in order and
// merges results: fields set by earlier resolvers win over later ones.
// Provenance is populated with first-setter-wins per bool field.
func (c *CompositeResolver) Resolve(ctx context.Context, senderPubkey ed25519.PublicKey) (IdentityInfo, error) {
	merged := IdentityInfo{Provenance: map[string]string{}}
	for _, r := range c.resolvers {
		info, err := r.Resolve(ctx, senderPubkey)
		if err != nil {
			return IdentityInfo{}, err
		}
		name := r.Name()
		// Always prefer the first non-nil MachineKey.
		if merged.MachineKey == nil && info.MachineKey != nil {
			merged.MachineKey = info.MachineKey
		}
		// Merge string Identity: first non-empty wins.
		if merged.Identity == "" && info.Identity != "" {
			merged.Identity = info.Identity
		}
		// Merge bool flags: first-setter-wins; record provenance for first setter.
		if info.IdentityVerified && !merged.IdentityVerified {
			merged.IdentityVerified = true
			merged.Provenance["identity_verified"] = name
		}
		if info.TrustResolved && !merged.TrustResolved {
			merged.TrustResolved = true
			merged.Provenance["trust_resolved"] = name
			// Chain and Anchor are evidence of trust resolution — adopt them
			// from the same resolver that set TrustResolved=true.
			if info.Chain != nil {
				merged.Chain = info.Chain
			}
			if info.Anchor != nil {
				merged.Anchor = info.Anchor
			}
		}
	}
	return merged, nil
}

// resolveIdentity decodes the hex sender pubkey from a protocol.Message and
// calls resolver.Resolve. On hex decode failure it returns IdentityInfo with
// nil MachineKey and IdentityVerified=false. On resolver error it returns
// MachineKey-only with IdentityVerified=false.
func resolveIdentity(ctx context.Context, senderHex string, resolver IdentityResolver) IdentityInfo {
	pub, err := hex.DecodeString(senderHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		// Malformed sender — return zero IdentityInfo (MachineKey nil).
		return IdentityInfo{}
	}
	info, err := resolver.Resolve(ctx, ed25519.PublicKey(pub))
	if err != nil {
		// Resolver error — return MachineKey-only, IdentityVerified=false.
		return IdentityInfo{MachineKey: ed25519.PublicKey(pub)}
	}
	return info
}
