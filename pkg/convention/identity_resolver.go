package convention

import (
	"crypto/ed25519"
	"encoding/hex"

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
