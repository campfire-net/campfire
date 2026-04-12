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
	Resolve(senderPubkey ed25519.PublicKey) IdentityInfo
}

// NoopIdentityResolver returns IdentityInfo with only MachineKey set.
// Used when no VerificationCache is available. This is the default resolver.
type NoopIdentityResolver struct{}

// Resolve implements IdentityResolver. It returns IdentityInfo with only
// MachineKey populated; IdentityVerified is always false.
func (NoopIdentityResolver) Resolve(senderPubkey ed25519.PublicKey) IdentityInfo {
	return IdentityInfo{MachineKey: senderPubkey}
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

// Resolve implements IdentityResolver. If cache has a verified entry for
// senderPubkey, it returns IdentityInfo with Identity and IdentityVerified set.
// On a cache miss (or empty campfire ID), it returns MachineKey-only.
func (r *CacheIdentityResolver) Resolve(senderPubkey ed25519.PublicKey) IdentityInfo {
	info := IdentityInfo{MachineKey: senderPubkey}
	if id, ok := r.cache.Get(senderPubkey); ok && id != "" {
		info.Identity = id
		info.IdentityVerified = true
	}
	return info
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
//
// NOTE: IdentityResolver.Resolve does not accept a context parameter. The caller
// context is not available here; context.Background() is used as the threading
// point until the IdentityResolver interface is updated to accept ctx.
func (r *GrantChainResolver) Resolve(senderPubkey ed25519.PublicKey) IdentityInfo {
	info := IdentityInfo{MachineKey: senderPubkey}
	if r.campfireIDRaw == nil {
		// campfireID was invalid at construction — fail-closed.
		return info
	}
	out := delegation.Resolve(context.Background(), r.client, r.campfireIDRaw, senderPubkey, r.anchors)
	if resolved, ok := out.(delegation.Resolved); ok {
		info.TrustResolved = true
		info.Chain = resolved.Chain
		info.Anchor = resolved.Anchor
	}
	return info
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

// Resolve implements IdentityResolver. It calls each resolver in order and
// merges results: fields set by earlier resolvers win over later ones.
func (c *CompositeResolver) Resolve(senderPubkey ed25519.PublicKey) IdentityInfo {
	merged := IdentityInfo{}
	for _, r := range c.resolvers {
		info := r.Resolve(senderPubkey)
		// Always prefer the first non-nil MachineKey.
		if merged.MachineKey == nil && info.MachineKey != nil {
			merged.MachineKey = info.MachineKey
		}
		// Merge string Identity: first non-empty wins.
		if merged.Identity == "" && info.Identity != "" {
			merged.Identity = info.Identity
		}
		// Merge bool flags: once set to true, stay true.
		if info.IdentityVerified {
			merged.IdentityVerified = true
		}
		if info.TrustResolved {
			merged.TrustResolved = true
		}
		// Merge Chain/Anchor from first resolver that resolved trust.
		if merged.Chain == nil && info.Chain != nil {
			merged.Chain = info.Chain
		}
		if merged.Anchor == nil && info.Anchor != nil {
			merged.Anchor = info.Anchor
		}
	}
	return merged
}

// resolveIdentity decodes the hex sender pubkey from a protocol.Message and
// calls resolver.Resolve. On hex decode failure it returns IdentityInfo with
// nil MachineKey and IdentityVerified=false.
func resolveIdentity(senderHex string, resolver IdentityResolver) IdentityInfo {
	pub, err := hex.DecodeString(senderHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		// Malformed sender — return zero IdentityInfo (MachineKey nil).
		return IdentityInfo{}
	}
	return resolver.Resolve(ed25519.PublicKey(pub))
}
