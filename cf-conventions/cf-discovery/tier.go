package discovery

// tier.go — 3-tier discovery fallthrough and multi-level chain resolution.
//
// Implements the three tiers defined in design v2 §3:
//   Tier 1: Pre-published snippets in the parent namespace (naming:preview)
//   Tier 2: Scoped auto-join with post-join verification (probe-write-then-observe)
//   Tier 3: Declared-public preview op at the convention layer (level:0 + rate-limit)
//
// Multi-level chains (§3.6) are also supported: browsing nested namespaces as a
// lazy chain of per-hop Tier-1+Tier-2 cycles with minimum-freshness-window composition.
//
// Spec citations: design v2 §3, cf-discovery-spec.md §11 (post-join verification).

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrInviteOnly is returned when a campfire requires an explicit invite and
// cannot be auto-joined via Tier-2 scoped auto-join.
var ErrInviteOnly = errors.New("campfire requires explicit invite (join_protocol != open)")

// ErrPostJoinVerificationFailed is returned when post-join probe-write-then-observe
// fails: the probe message was not returned, indicating the campfire silently drops
// or queues writes from new members (honeypot detection — §11).
var ErrPostJoinVerificationFailed = errors.New("post-join verification failed: probe message not returned (possible honeypot)")

// DefaultProbeTimeout is the default timeout for the post-join probe-and-observe step.
// Configurable via config key discovery.probe_timeout. See cf-discovery-spec.md §11.3.1
// for the false-positive risk trade-off discussion.
const DefaultProbeTimeout = 5 * time.Second

// ProbeTag is the message tag used for post-join probe messages (§11.3).
const ProbeTag = "discovery:probe"

// UnjoinDeclarationTag is the tag for unjoin-declaration messages (§11.5).
const UnjoinDeclarationTag = "discovery:unjoin-declaration"

// DiscoveryTier indicates which discovery tier produced a result.
type DiscoveryTier int

const (
	// TierSnippet is Tier 1: result from a naming:preview snippet in the parent namespace.
	TierSnippet DiscoveryTier = 1
	// TierAutoJoin is Tier 2: result from scoped auto-join with post-join verification.
	TierAutoJoin DiscoveryTier = 2
	// TierPreviewOp is Tier 3: result from a declared-public level:0 preview convention op.
	TierPreviewOp DiscoveryTier = 3
)

// DiscoveryResult is a single entry returned from a discovery query.
type DiscoveryResult struct {
	// CampfireID is the hex-encoded campfire public key.
	CampfireID string
	// Name is the registered name of this campfire in its parent namespace.
	Name string
	// Description is the human-readable description (may be from snippet or direct query).
	Description string
	// Tier indicates which discovery tier produced this result.
	Tier DiscoveryTier
	// Snippet is the validated snippet, if this result came from Tier 1.
	Snippet *SnippetResult
	// Stale is true when any hop in the resolution chain has an expired freshness window.
	Stale bool
	// ChainDepth is the number of namespace hops traversed to produce this result.
	ChainDepth int
	// FreshnessWindow is the minimum freshness window across all chain hops.
	FreshnessWindow time.Duration
}

// Discoverer is the interface for performing 3-tier discovery queries.
// Implementations are expected to be safe for concurrent use.
type Discoverer interface {
	// Discover queries a parent campfire for its registered children.
	// Returns a slice of DiscoveryResult, one per visible child.
	// The resolver tries Tier 1 first, then Tier 2, then Tier 3.
	Discover(ctx context.Context, parentCampfireID string) ([]DiscoveryResult, error)

	// DiscoverChain performs a multi-level lazy chain walk to enumerate
	// children at a given namespace path. segments is the chain of name
	// components to walk (e.g., ["metropolis", "lot42"] for freeso.metropolis.lot42).
	// Returns the results at the leaf level with min-freshness-window composition.
	DiscoverChain(ctx context.Context, segments []string) ([]DiscoveryResult, error)
}

// SnippetStore is the read interface for accessing naming:preview snippets
// from a parent campfire's message store.
type SnippetStore interface {
	// ListSnippets returns all naming:preview messages from the given campfire.
	// Each entry includes the message payload and timestamp.
	ListSnippets(campfireID string) ([]RawSnippetMessage, error)
}

// RawSnippetMessage is a raw naming:preview message with its envelope metadata.
type RawSnippetMessage struct {
	// Payload is the raw JSON bytes of the snippet.
	Payload []byte
	// Timestamp is the Unix timestamp of the enclosing campfire message.
	Timestamp int64
	// SignerPub is the Ed25519 public key of the message signer (used to verify
	// that the signer is the parent campfire — cf-discovery-spec.md §4.3).
	SignerPub []byte
}

// AutoJoinFunc is called to auto-join a campfire during Tier-2 discovery.
// Returns nil on success, ErrInviteOnly if the campfire requires explicit invite,
// or ErrPostJoinVerificationFailed if probe-write-then-observe failed.
type AutoJoinFunc func(ctx context.Context, campfireID string) error

// Tier2Verifier is the interface for performing post-join probe-write-then-observe
// verification per cf-discovery-spec.md §11.
type Tier2Verifier interface {
	// ProbeAndObserve sends a discovery:probe message to the campfire and reads back
	// the message stream to verify the probe is returned. Returns nil if the campfire
	// is behaving consistently with join_protocol:open. Returns ErrPostJoinVerificationFailed
	// if the probe is not returned within the configured timeout.
	ProbeAndObserve(ctx context.Context, campfireID string, probeTimeout time.Duration) error

	// PostUnjoinDeclaration signs and posts a discovery:unjoin-declaration message
	// to the campfire before leaving, per §11.4.
	PostUnjoinDeclaration(ctx context.Context, campfireID, probeMsgID string) error
}

// ChainEntry is one resolved hop in a multi-level chain walk (§3.6).
type ChainEntry struct {
	// CampfireID is the campfire at this hop.
	CampfireID string
	// Name is the name segment at this hop.
	Name string
	// Snippet is the snippet that resolved this hop (may be nil for root).
	Snippet *SnippetResult
	// Joined is true if Tier-2 auto-join was triggered at this hop.
	Joined bool
}

// ResolveChain walks a multi-segment name chain and returns the ordered list of
// resolved hops. segments must have len >= 1.
// rootID is the campfire ID of the root namespace.
// autoJoin is invoked for each intermediate campfire in the chain (may be nil — disables Tier 2).
// snippetStore is used to read naming:preview snippets at each hop.
// parentPub is a function that returns the Ed25519 public key for a campfire (used to verify snippets).
//
// The returned chain has len == len(segments). The last entry is the leaf campfire.
// The error ErrInviteOnly or ErrPostJoinVerificationFailed propagates from autoJoin.
func ResolveChain(
	ctx context.Context,
	segments []string,
	rootID string,
	snippetStore SnippetStore,
	parentPub func(campfireID string) ([]byte, error),
	autoJoin AutoJoinFunc,
) ([]ChainEntry, error) {
	if len(segments) == 0 {
		return nil, fmt.Errorf("empty segment chain")
	}

	chain := make([]ChainEntry, 0, len(segments))
	currentID := rootID

	for _, seg := range segments {
		// Read snippets from the current campfire
		rawMsgs, err := snippetStore.ListSnippets(currentID)
		if err != nil {
			return nil, fmt.Errorf("listing snippets at campfire %s: %w", shortCampfireID(currentID), err)
		}

		// Get the parent pub key for signature verification
		pubBytes, err := parentPub(currentID)
		if err != nil {
			return nil, fmt.Errorf("getting parent pub key for campfire %s: %w", shortCampfireID(currentID), err)
		}

		var matched *SnippetResult
		now := time.Now()
		for _, raw := range rawMsgs {
			var s Snippet
			if jsonErr := unmarshalSnippet(raw.Payload, &s); jsonErr != nil {
				continue
			}
			if s.Name != seg {
				continue
			}
			result, verifyErr := VerifySnippet(s, pubBytes, raw.Timestamp, now)
			if verifyErr != nil {
				continue // skip invalid snippets
			}
			// Verify the message signer matches the parent campfire (§4.3)
			if len(raw.SignerPub) > 0 && !bytesEqual(raw.SignerPub, pubBytes) {
				continue // signer is not the parent campfire — reject
			}
			matched = result
			break
		}

		var nextID string
		var joined bool
		if matched != nil {
			// Tier 1: snippet found — extract campfire ID from the beacon if present
			nextID = matched.Beacon // may be empty; caller should check
		}

		if nextID == "" {
			// Tier 2: try auto-join if available
			if autoJoin != nil {
				if err := autoJoin(ctx, currentID); err != nil {
					if errors.Is(err, ErrInviteOnly) || errors.Is(err, ErrPostJoinVerificationFailed) {
						return nil, fmt.Errorf("chain hop %q: %w", seg, err)
					}
					return nil, fmt.Errorf("chain hop %q: auto-join: %w", seg, err)
				}
				joined = true
			}
		}

		chain = append(chain, ChainEntry{
			CampfireID: currentID,
			Name:       seg,
			Snippet:    matched,
			Joined:     joined,
		})

		// Advance to the next level. If we have a beacon, use it; otherwise we have
		// already auto-joined this campfire and the next Scan/ListSnippets will use currentID.
		if nextID != "" {
			currentID = nextID
		}
	}

	return chain, nil
}

// ComposeFreshnessFromChain computes the min-freshness-window and stale state
// across a chain of ChainEntries per §3.2. Entries without a snippet are ignored
// (they contribute no freshness state).
func ComposeFreshnessFromChain(chain []ChainEntry) (minWindow time.Duration, anyStale bool) {
	minWindow = maxFreshnessWindow + 1 // sentinel
	for _, entry := range chain {
		if entry.Snippet == nil {
			continue
		}
		fw, err := time.ParseDuration(entry.Snippet.FreshnessWindow)
		if err != nil {
			continue
		}
		if fw < minWindow {
			minWindow = fw
		}
		if entry.Snippet.Stale {
			anyStale = true
		}
	}
	if minWindow > maxFreshnessWindow {
		minWindow = 0 // no snippets in chain
	}
	return
}

// shortCampfireID truncates a campfire ID to 12 chars for display.
func shortCampfireID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// unmarshalSnippet parses snippet JSON.
func unmarshalSnippet(data []byte, s *Snippet) error {
	return jsonUnmarshal(data, s)
}

// bytesEqual compares two byte slices.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
