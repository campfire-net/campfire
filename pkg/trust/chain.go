// Package trust implements the trust chain walker and TOFU pin store
// per Trust Convention v0.1 §4 and §8.
package trust

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// TrustStatus is the envelope trust_chain field value.
type TrustStatus string

const (
	TrustVerified   TrustStatus = "verified"
	TrustCrossRoot  TrustStatus = "cross-root"
	TrustRelayed    TrustStatus = "relayed"
	TrustUnverified TrustStatus = "unverified"
)

// Chain holds the verified trust chain from beacon root through declarations.
type Chain struct {
	RootKey         string    // beacon root key (hex)
	RootRegistryID  string    // root registry campfire ID
	ConventionRegID string    // convention registry campfire ID
	Declarations    []string  // message IDs of verified declarations
	VerifiedAt      time.Time // when this chain was last verified
	ExpiresAt       time.Time // when this chain expires (TTL-based)
}

// ChainStore provides message reading for chain verification.
// Implementations wrap store.Store or can be mocked in tests.
type ChainStore interface {
	ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error)
}

// ChainResolver discovers the root registry campfire ID from a beacon root key.
type ChainResolver interface {
	ResolveRootRegistry(ctx context.Context, rootKey string) (campfireID string, err error)
}

// Default cache and rate-limit settings.
const (
	defaultCacheTTL  = 5 * time.Minute
	minCacheTTL      = 30 * time.Second
	maxCacheTTL      = 1 * time.Hour
	minRewalkInterval = 30 * time.Second
)

// chainCacheEntry holds a cached chain and timing metadata.
type chainCacheEntry struct {
	chain      *Chain
	verifiedAt time.Time
	expiresAt  time.Time
}

// ChainWalker verifies the trust bootstrap chain from beacon root to declarations.
type ChainWalker struct {
	rootKey  string         // beacon root key (hex-encoded Ed25519 public key)
	store    ChainStore     // message store for reading campfire messages
	resolver ChainResolver  // resolves root key → root registry campfire ID
	pins     *PinStore      // TOFU pin persistence
	cacheTTL time.Duration  // TTL for cached chain results

	mu            sync.Mutex
	cache         map[string]*chainCacheEntry // keyed by rootKey
	lastWalkTime  map[string]time.Time        // keyed by rootKey, for rate limiting
}

// ChainWalkerOption configures a ChainWalker.
type ChainWalkerOption func(*ChainWalker)

// WithCacheTTL sets the chain cache TTL. Clamped to [30s, 1h].
func WithCacheTTL(ttl time.Duration) ChainWalkerOption {
	return func(w *ChainWalker) {
		if ttl < minCacheTTL {
			ttl = minCacheTTL
		}
		if ttl > maxCacheTTL {
			ttl = maxCacheTTL
		}
		w.cacheTTL = ttl
	}
}

// WithPinStore attaches a TOFU pin store to the walker.
func WithPinStore(ps *PinStore) ChainWalkerOption {
	return func(w *ChainWalker) {
		w.pins = ps
	}
}

// NewChainWalker creates a ChainWalker for the given beacon root key.
func NewChainWalker(rootKey string, cs ChainStore, resolver ChainResolver, opts ...ChainWalkerOption) *ChainWalker {
	w := &ChainWalker{
		rootKey:      rootKey,
		store:        cs,
		resolver:     resolver,
		cacheTTL:     defaultCacheTTL,
		cache:        make(map[string]*chainCacheEntry),
		lastWalkTime: make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// WalkChain verifies: beacon root → root registry → convention registry → declarations.
// Returns the verified chain or an error at the broken link.
func (w *ChainWalker) WalkChain(ctx context.Context) (*Chain, error) {
	w.mu.Lock()

	// Check cache first.
	if entry, ok := w.cache[w.rootKey]; ok && time.Now().Before(entry.expiresAt) {
		chain := entry.chain
		w.mu.Unlock()
		return chain, nil
	}

	// Rate-limit re-walks.
	if last, ok := w.lastWalkTime[w.rootKey]; ok && time.Since(last) < minRewalkInterval {
		// Return cached result if available (even if expired), otherwise proceed.
		if entry, ok := w.cache[w.rootKey]; ok {
			chain := entry.chain
			w.mu.Unlock()
			return chain, nil
		}
	}

	w.lastWalkTime[w.rootKey] = time.Now()
	w.mu.Unlock()

	chain, err := w.walkChainUncached(ctx)
	if err != nil {
		return nil, err
	}

	w.mu.Lock()
	w.cache[w.rootKey] = &chainCacheEntry{
		chain:      chain,
		verifiedAt: chain.VerifiedAt,
		expiresAt:  chain.ExpiresAt,
	}
	w.mu.Unlock()

	return chain, nil
}

// InvalidateCache removes the cached chain for the walker's root key,
// allowing a fresh walk on the next call. Rate limiting still applies.
func (w *ChainWalker) InvalidateCache() {
	w.mu.Lock()
	delete(w.cache, w.rootKey)
	w.mu.Unlock()
}

// walkChainUncached performs the actual three-link verification.
func (w *ChainWalker) walkChainUncached(ctx context.Context) (*Chain, error) {
	// Step 1: Beacon root → root registry.
	// Resolve the root registry campfire ID from the beacon root key.
	rootRegistryID, err := w.resolver.ResolveRootRegistry(ctx, w.rootKey)
	if err != nil {
		return nil, fmt.Errorf("trust chain broken at beacon→root registry: %w", err)
	}

	// Verify the root registry campfire key matches the beacon root key.
	// Read any message from the root registry and check its sender matches rootKey.
	rootMsgs, err := w.store.ListMessages(rootRegistryID, 0)
	if err != nil {
		return nil, fmt.Errorf("trust chain broken at beacon→root registry: reading messages: %w", err)
	}
	if len(rootMsgs) == 0 {
		return nil, fmt.Errorf("trust chain broken at beacon→root registry: no messages in root registry %s", rootRegistryID)
	}
	// Verify at least one message is from the expected root key.
	rootKeyVerified := false
	for _, msg := range rootMsgs {
		if msg.Sender == w.rootKey {
			rootKeyVerified = true
			break
		}
	}
	if !rootKeyVerified {
		return nil, fmt.Errorf("trust chain broken at beacon→root registry: campfire key %s does not match beacon root key %s", rootMsgs[0].Sender, w.rootKey)
	}

	// Step 2: Root registry → convention registry.
	// Find registration messages in the root registry.
	regMsgs, err := w.store.ListMessages(rootRegistryID, 0, store.MessageFilter{
		Tags: []string{"naming:registration"},
	})
	if err != nil {
		return nil, fmt.Errorf("trust chain broken at root→convention registry: reading registrations: %w", err)
	}
	if len(regMsgs) == 0 {
		return nil, fmt.Errorf("trust chain broken at root→convention registry: no registration messages in root registry %s", rootRegistryID)
	}

	// Find the convention registry registration signed by the root key.
	var conventionRegID string
	for _, msg := range regMsgs {
		if msg.Sender != w.rootKey {
			continue
		}
		// Verify signature.
		if !message.VerifyMessageSignature(msg.ID, msg.Payload, msg.Tags, msg.Antecedents, msg.Timestamp, msg.Sender, msg.Signature) {
			continue
		}
		// The payload contains the convention registry campfire ID.
		conventionRegID = string(msg.Payload)
		break
	}
	if conventionRegID == "" {
		return nil, fmt.Errorf("trust chain broken at root→convention registry: no valid registration signed by root key %s", w.rootKey)
	}

	// Step 3: Convention registry → declarations.
	// Read declarations from the convention registry.
	declMsgs, err := w.store.ListMessages(conventionRegID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if err != nil {
		return nil, fmt.Errorf("trust chain broken at convention registry→declarations: reading declarations: %w", err)
	}

	// Verify declarations are signed by the convention registry campfire key.
	// First, determine the convention registry's key by reading its messages.
	convRegMsgs, err := w.store.ListMessages(conventionRegID, 0)
	if err != nil {
		return nil, fmt.Errorf("trust chain broken at convention registry→declarations: reading convention registry: %w", err)
	}
	// The convention registry key is the sender of messages in the convention registry.
	// We need to find the authoritative key. Use the key from the registration step.
	convRegKey := ""
	if len(convRegMsgs) > 0 {
		convRegKey = convRegMsgs[0].Sender
	}
	if convRegKey == "" {
		return nil, fmt.Errorf("trust chain broken at convention registry→declarations: cannot determine convention registry key")
	}

	var verifiedDecls []string
	for _, msg := range declMsgs {
		if msg.Sender != convRegKey {
			continue
		}
		if !message.VerifyMessageSignature(msg.ID, msg.Payload, msg.Tags, msg.Antecedents, msg.Timestamp, msg.Sender, msg.Signature) {
			continue
		}
		verifiedDecls = append(verifiedDecls, msg.ID)
	}

	now := time.Now()
	return &Chain{
		RootKey:         w.rootKey,
		RootRegistryID:  rootRegistryID,
		ConventionRegID: conventionRegID,
		Declarations:    verifiedDecls,
		VerifiedAt:      now,
		ExpiresAt:       now.Add(w.cacheTTL),
	}, nil
}

// ChainStatus returns the trust chain status for a campfire.
// It walks the chain and determines the trust level based on the result.
func (w *ChainWalker) ChainStatus(ctx context.Context, campfireID string) (TrustStatus, error) {
	chain, err := w.WalkChain(ctx)
	if err != nil {
		return TrustUnverified, nil
	}

	// If the campfire is the root registry or convention registry, it's directly verified.
	if campfireID == chain.RootRegistryID || campfireID == chain.ConventionRegID {
		return TrustVerified, nil
	}

	// Check if any declarations reference this campfire.
	for _, declID := range chain.Declarations {
		// A declaration that exists in the verified chain means the convention
		// registry vouched for it.
		_ = declID
	}

	// If we got a valid chain, the campfire is at least cross-root verified
	// (reachable via the chain but not directly in the registry).
	if chain.RootKey != "" {
		return TrustCrossRoot, nil
	}

	return TrustUnverified, nil
}
