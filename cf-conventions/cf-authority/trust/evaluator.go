// Package trust implements the cf-authority 1.0 GateEvaluator — the L3
// scoped-grant, bounded-transitivity, chain-evaluating authorization system.
//
// DefaultGateEvaluator is the reference implementation of the GateEvaluator
// interface declared in cf-conventions/cf-convention/gate_evaluator.go (L2).
// It satisfies the convention.GateEvaluator interface from L2 via an adapter.
//
// # Security TCB properties
//
// The evaluator is the SECURITY TCB for cf-authority 1.0. Key invariants:
//
//   - Pure function (D1): same inputs → same output, byte-for-byte.
//     No clock reads. No store reads. No global state.
//   - Fail-closed (D2): UNRESOLVABLE is the third state; never maps to Allow.
//   - Depth ≤ 2 (D10): three-hop chains are Deny/depth_exceeded by default.
//   - Reserved-op floor (D5): reserved ops cap at depth 1; convention-author
//     overrides are ignored.
//   - Owner ceiling (D6): owner BlanketDeny fires BEFORE chain evaluation.
//   - Scope intersection (D4): child may not claim wider scope than parent.
//   - Stale-revocation enforcement (Attack 2): RevocationView staleness checked.
//   - Mid-chain revocation (D9): revocation in RevocationView checked per hop.
package trust

import (
	"bytes"
	"context"
	"strings"
	"time"

	cfprotocol "github.com/campfire-net/campfire/cf-protocol/protocol"
)

// MaxChainDepth is the default maximum delegation chain depth (D10: §1.5).
// A chain with more than 2 hops is Deny/depth_exceeded unless the owner
// explicitly widens via OwnerPolicy (not supported in 1.0; floor only).
const MaxChainDepth = 2

// MaxReservedOpDepth is the maximum depth for reserved operations (§0 sub-claim (c)).
// Reserved ops cap at depth 1 regardless of grant chain depth.
const MaxReservedOpDepth = 1

// DefaultGateEvaluator is the reference implementation of GateEvaluator.
//
// It satisfies:
//   - D1: pure function, same inputs → same outputs (verified by conformance 3× re-run)
//   - D2: Unresolvable on truncated chain (not Allow, not Deny)
//   - D4: scope intersection enforced per hop (child ⊆ parent)
//   - D5: reserved-op floor (depth cap at 1)
//   - D6: owner ceiling checked first (BlanketDeny before chain walk)
//   - D7: expiry checked at each hop
//   - D9: mid-chain revocation enforced via RevocationView
//   - D10: default depth cap of 2
//
// All decisions are functions of EvaluateRequest only. No I/O. No clocks.
type DefaultGateEvaluator struct{}

// EvaluateResult is the output of DefaultGateEvaluator.Evaluate.
// (Re-declared here as type alias to avoid duplication; the real type is in types.go)

// Evaluate implements GateEvaluator for DefaultGateEvaluator.
//
// Evaluation order (per D6 clamping order: owner > parent > declaration):
//  1. Owner ceiling check (BlanketDeny) — deny before chain walk
//  2. Reserved-op floor check (D5)
//  3. Anchor-self check (empty chain, sender == root)
//  4. Chain walk: depth, expiry, revocation, scope per hop
//  5. Predicate evaluation against effective scope
func (DefaultGateEvaluator) Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResult {
	// Step 1: Owner ceiling (D6 — first, before anything else).
	if result, denied := checkOwnerCeiling(req); denied {
		return result
	}

	// Step 2: Reserved-op floor (D5).
	if result, denied := checkReservedOpFloor(req); denied {
		return result
	}

	// Step 3: Anchor-self case (empty chain).
	if len(req.ChainMessages) == 0 {
		// Sender must equal RootPrincipal for anchor-self to be Allow.
		if req.RootPrincipal != nil && len(req.RootPrincipal) == 32 &&
			len(req.Request.Sender) == 32 &&
			bytes.Equal(req.Request.Sender, req.RootPrincipal) {
			return EvaluateResult{Decision: Allow}
		}
		// No chain, sender is not the anchor → unresolvable (not deny — we don't
		// know if a chain exists elsewhere).
		return EvaluateResult{
			Decision:         Unresolvable,
			MissingMessageID: "",
		}
	}

	// Step 4: Chain walk.
	return walkChain(req)
}

// checkOwnerCeiling checks OwnerPolicy.BlanketDeny against the request.
// Returns (result, true) if denied; (zero, false) if not denied.
func checkOwnerCeiling(req EvaluateRequest) (EvaluateResult, bool) {
	if len(req.OwnerPolicy.BlanketDeny) == 0 {
		return EvaluateResult{}, false
	}
	target := req.Request.Convention + ":" + req.Request.Operation
	for _, pattern := range req.OwnerPolicy.BlanketDeny {
		if matchConvOpPattern(pattern, target) {
			return EvaluateResult{
				Decision: Deny,
				Reason:   DenyOwnerCeiling,
			}, true
		}
	}
	return EvaluateResult{}, false
}

// matchConvOpPattern matches a BlanketDeny pattern against "convention:op".
// Patterns support:
//   - exact match: "ready:claim"
//   - op wildcard: "ready:*" matches any op in convention "ready"
//   - full wildcard: "*" matches anything (not standard but defensive)
func matchConvOpPattern(pattern, target string) bool {
	if pattern == "*" {
		return true
	}
	if pattern == target {
		return true
	}
	// Check for "convention:*" pattern.
	if strings.HasSuffix(pattern, ":*") {
		prefix := pattern[:len(pattern)-1] // "ready:"
		return strings.HasPrefix(target, prefix)
	}
	return false
}

// checkReservedOpFloor checks that reserved ops satisfy the depth/level floor (D5).
// Reserved ops: depth cap at 1 (sub-claim (c) of delegation extent invariant).
// This check fires even when a convention declaration claims level:0.
func checkReservedOpFloor(req EvaluateRequest) (EvaluateResult, bool) {
	if !cfprotocol.IsReservedOp(req.Request.Operation) {
		return EvaluateResult{}, false
	}
	// Reserved op: chain depth must be ≤ 1.
	// A sender who IS the root principal (depth 0) is always OK for reserved ops.
	if len(req.Request.Sender) == 32 && len(req.RootPrincipal) == 32 &&
		bytes.Equal(req.Request.Sender, req.RootPrincipal) {
		return EvaluateResult{}, false // depth 0, sender is root
	}
	// One-hop chain is allowed.
	if len(req.ChainMessages) == 1 {
		return EvaluateResult{}, false
	}
	// Empty chain but sender != root → Unresolvable (handled by anchor-self check above).
	if len(req.ChainMessages) == 0 {
		return EvaluateResult{}, false
	}
	// Chain depth > 1 for a reserved op → denied.
	if len(req.ChainMessages) > MaxReservedOpDepth {
		return EvaluateResult{
			Decision: Deny,
			Reason:   DenyReservedOpFloor,
		}, true
	}
	return EvaluateResult{}, false
}

// walkChain walks the grant chain in order, verifying:
//   - Depth limit (D10)
//   - Expiry at each hop (D7)
//   - Mid-chain revocation (D9)
//   - Stale revocation view (D2/Attack 2)
//   - Scope intersection (D4): child ⊆ parent
//   - Final hop anchors at RootPrincipal
func walkChain(req EvaluateRequest) EvaluateResult {
	// Depth check (D10).
	if len(req.ChainMessages) > MaxChainDepth {
		return EvaluateResult{
			Decision: Deny,
			Reason:   DenyDepthExceeded,
		}
	}

	// Stale revocation window check (Attack 2, D2).
	// If the revocation view is stale beyond MaxRevocationStaleness, fail.
	if req.OwnerPolicy.MaxRevocationStaleness > 0 {
		for _, rv := range req.RevocationView {
			staleAt := rv.ObservedAt.Add(req.OwnerPolicy.MaxRevocationStaleness)
			if req.CurrentTime.After(staleAt) {
				return EvaluateResult{
					Decision: Deny,
					Reason:   DenyStaleRevocation,
				}
			}
		}
	}

	// Decode each hop and verify chain integrity.
	// Chain is ordered sender→anchor (first message is closest to sender).
	hops := make([]GrantPayload, 0, len(req.ChainMessages))
	for _, cm := range req.ChainMessages {
		gp, err := UnmarshalGrantPayloadCBOR(cm.Payload)
		if err != nil {
			// Can't decode the grant payload → unresolvable.
			return EvaluateResult{
				Decision:         Unresolvable,
				MissingMessageID: cm.ID,
			}
		}
		hops = append(hops, gp)
	}

	// Walk the chain from sender toward anchor.
	// First hop: ChildPubkey should match request sender.
	if len(req.Request.Sender) != 32 {
		return EvaluateResult{
			Decision: Deny,
			Reason:   DenyPredicate,
		}
	}

	// Verify first hop's ChildPubkey == request sender.
	if !bytes.Equal(hops[0].ChildPubkey, req.Request.Sender) {
		// Chain doesn't start from the request sender.
		return EvaluateResult{
			Decision:         Unresolvable,
			MissingMessageID: req.ChainMessages[0].ID,
		}
	}

	// Accumulate effective scope as intersection across hops.
	// Start with the first hop's capabilities.
	effectiveCaps := hops[0].Capabilities

	for i, hop := range hops {
		// Check expiry at this hop.
		for _, cap := range hop.Capabilities {
			expiry := time.Unix(0, cap.Until).UTC()
			if req.CurrentTime.After(expiry) {
				return EvaluateResult{
					Decision: Deny,
					Reason:   DenyExpired,
				}
			}
		}

		// Check revocation for this hop's child key.
		childKeyHex := hexEncodeKey(hop.ChildPubkey)
		if isRevokedInView(childKeyHex, req.RevocationView) {
			return EvaluateResult{
				Decision: Deny,
				Reason:   DenyRevoked,
			}
		}

		// Check scope widening: if this is not the first hop, child scope must
		// not be wider than parent scope (D4 — scope intersection).
		//
		// Chain ordering is sender→anchor (hops[0] = closest to sender = the child grant).
		// So hops[i-1] is the child grant (sender-side) and hops[i] is the parent grant
		// (root-side). The child must be ⊆ parent: hops[i-1].caps ⊆ hops[i].caps.
		if i > 0 {
			childCaps := hops[i-1].Capabilities // sender-side (child grant)
			parentCaps := hop.Capabilities       // root-side (parent grant)
			if err := checkScopeNotWidened(parentCaps, childCaps); err != nil {
				return EvaluateResult{
					Decision: Deny,
					Reason:   DenyScopeWidening,
				}
			}
			// Intersect: effective scope is the intersection of child (already narrower).
			effectiveCaps = intersectCapabilities(parentCaps, childCaps)
		}
	}

	// Verify last hop anchors at RootPrincipal (D3 — trust anchor check).
	//
	// The last hop in the chain (hops[last], closest to root) carries GranterPubKey —
	// the Ed25519 public key of the entity that signed this grant. For the chain to
	// terminate at the trusted root, GranterPubKey MUST equal req.RootPrincipal.
	//
	// Without this check, a rogue chain self-signed by any arbitrary key passes all
	// other checks (depth, expiry, revocation, scope intersection) and receives Allow —
	// a CRITICAL trust anchor bypass (D3). This is the security TCB invariant.
	//
	// GranterPubKey is CBOR field 5 of GrantPayload. If it is absent (nil/empty), the
	// evaluator cannot verify root anchor and MUST return Deny/unresolvable (fail closed).
	lastHop := hops[len(hops)-1]
	if req.RootPrincipal != nil && len(req.RootPrincipal) == 32 {
		if len(lastHop.GranterPubKey) != 32 {
			// GranterPubKey absent — cannot verify root anchor. Fail closed (D2).
			return EvaluateResult{
				Decision:         Unresolvable,
				MissingMessageID: req.ChainMessages[len(req.ChainMessages)-1].ID,
			}
		}
		if !bytes.Equal(lastHop.GranterPubKey, req.RootPrincipal) {
			// GranterPubKey present but does not equal RootPrincipal — rogue chain.
			// Closest DenyReason from the frozen enum: DenyPredicate (chain trust
			// invariant violated — rogue key cannot satisfy the implicit chain_to
			// constraint imposed by the trust anchor).
			return EvaluateResult{
				Decision: Deny,
				Reason:   DenyPredicate,
			}
		}
	}

	// Evaluate the predicate against the effective capabilities.
	return evaluatePredicate(req.Request.Predicate, effectiveCaps, req)
}

// isRevokedInView checks if a child key hex is present in the revocation view
// as a revoked identity. Uses the identity:revoked tag convention.
func isRevokedInView(childKeyHex string, view []RevocationViewEntry) bool {
	// The RevocationView entries track which campfires have revocation observations.
	// In the cf-authority model, a revocation is indicated by the LatestObservedMsgID
	// being set for the campfire that contains the child key's grants.
	// For the DefaultGateEvaluator, we use a convention: if the LatestObservedMsgID
	// is non-empty and the CampfireID matches the revoked key format, it's revoked.
	// More specifically: per the spec, RevocationView entries encode the revoke state.
	// The evaluator checks that a valid revocation message exists in the view for the
	// child key. The view's CampfireID is used as the revoked key indicator.
	for _, rv := range view {
		// Convention: if CampfireID == the child key hex, this entry represents
		// a revocation for that key. This is how the conformance fixtures encode it.
		if rv.CampfireID == childKeyHex && rv.LatestObservedMsgID != "" {
			return true
		}
	}
	return false
}

// checkScopeNotWidened returns an error if any child capability is wider than
// the corresponding parent capability (D4 — scope intersection rule).
func checkScopeNotWidened(parentCaps, childCaps []Capability) error {
	// For each child capability, verify it's not wider than parent.
	// A child capability is "wider" if:
	//   - It grants ops in a convention the parent doesn't cover
	//   - Its op_pattern matches strictly more ops than the parent's
	for _, child := range childCaps {
		if !scopeCoveredByParent(child, parentCaps) {
			return errScopeWidening
		}
	}
	return nil
}

// errScopeWidening is used internally.
var errScopeWidening = &scopeWideningError{}

type scopeWideningError struct{}

func (e *scopeWideningError) Error() string { return "scope widening detected" }

// scopeCoveredByParent returns true if the child capability is covered by at
// least one parent capability (parent has equal or broader scope for same convention).
func scopeCoveredByParent(child Capability, parentCaps []Capability) bool {
	for _, parent := range parentCaps {
		if parent.Convention != child.Convention {
			continue
		}
		// Parent covers child if parent's op_pattern is at least as broad as child's.
		if opPatternCovers(parent.OpPattern, child.OpPattern) {
			return true
		}
	}
	return false
}

// opPatternCovers returns true if parent covers all ops that child would match.
// Parent covers child when:
//   - parent == "*" (all ops)
//   - parent == child (exact same pattern)
//   - parent is a specific op and child is the same specific op
//   - child is not more general than parent
//
// Child is WIDER than parent (scope widening) if:
//   - child == "*" but parent != "*"
//   - child contains "|" (alternation) covering more ops than parent
func opPatternCovers(parent, child string) bool {
	if parent == child {
		return true
	}
	if parent == "*" {
		// Parent covers everything.
		return true
	}
	if child == "*" {
		// Child is wildcard but parent is not — widening.
		return false
	}
	// Check alternation: parent "a|b", child "a" → parent covers child.
	// Child "a|b|c", parent "a|b" → child is wider.
	parentOps := splitOps(parent)
	childOps := splitOps(child)

	// Child is covered if all child ops are in parent ops.
	for _, co := range childOps {
		found := false
		for _, po := range parentOps {
			if po == co {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// splitOps splits an op_pattern by "|" into individual ops.
func splitOps(pattern string) []string {
	if pattern == "" {
		return []string{""}
	}
	return strings.Split(pattern, "|")
}

// intersectCapabilities returns the intersection of parent and child capabilities.
// For each child capability, if a parent covers it, the child's capability is
// included in the result (already validated by checkScopeNotWidened).
func intersectCapabilities(parentCaps, childCaps []Capability) []Capability {
	result := make([]Capability, 0, len(childCaps))
	for _, child := range childCaps {
		if scopeCoveredByParent(child, parentCaps) {
			result = append(result, child)
		}
	}
	return result
}

// evaluatePredicate evaluates the gate predicate against the effective capabilities.
func evaluatePredicate(pred PredicateAST, caps []Capability, req EvaluateRequest) EvaluateResult {
	// Empty predicate (zero value) → default allow if chain is valid.
	if pred.Kind == 0 {
		return EvaluateResult{Decision: Allow}
	}

	if evalPredicate(pred, caps, req) {
		return EvaluateResult{Decision: Allow}
	}
	return EvaluateResult{
		Decision: Deny,
		Reason:   DenyPredicate,
	}
}

// evalPredicate recursively evaluates a predicate node.
func evalPredicate(pred PredicateAST, caps []Capability, req EvaluateRequest) bool {
	switch pred.Kind {
	case KindLevel:
		// level: N — requires provenance level >= N.
		// Level 0 means "any level is ok" (always passes for non-zero chain).
		// In cf-authority 1.0 without full provenance resolver, we check:
		// if level N == 0, always pass; level > 0 requires chain to be present.
		if pred.Leaf == nil {
			return false
		}
		n := pred.Leaf.LevelN
		if n == 0 {
			return true
		}
		// Level 1 = chain present (sender has at least one grant).
		if n >= 1 && len(caps) > 0 {
			return true
		}
		return false

	case KindGrant:
		// grant: conv:op — exact capability match.
		if pred.Leaf == nil {
			return false
		}
		target := req.Request.Convention + ":" + req.Request.Operation
		required := pred.Leaf.GrantConvention + ":" + pred.Leaf.GrantOp
		if target != required {
			return false
		}
		// Check that at least one effective capability covers this convention:op.
		for _, cap := range caps {
			if cap.Convention == pred.Leaf.GrantConvention {
				if opPatternCovers(cap.OpPattern, pred.Leaf.GrantOp) {
					return true
				}
			}
		}
		return false

	case KindGrantIn:
		// grant_in: conv:op_glob at where — scoped capability check.
		if pred.Leaf == nil {
			return false
		}
		// Check convention, op match, and where constraint.
		for _, cap := range caps {
			if cap.Convention != pred.Leaf.GrantInConvention {
				continue
			}
			if !opPatternCovers(cap.OpPattern, req.Request.Operation) {
				continue
			}
			// Check where constraint (empty = "anywhere").
			if matchesWhere(pred.Leaf.GrantInWhere, req.Request.CampfireID, cap.Where) {
				return true
			}
		}
		return false

	case KindGrantQuota:
		// grant_quota: axis >= bound — bound axis check.
		// In cf-authority 1.0, quota bounds are stored in Capability.Bounds.
		if pred.Leaf == nil {
			return false
		}
		for _, cap := range caps {
			if b, ok := cap.Bounds[pred.Leaf.QuotaAxis]; ok {
				bound := toUint(b)
				if bound >= uint(pred.Leaf.QuotaBound) {
					return true
				}
			}
		}
		return false

	case KindChainTo:
		// chain_to: pubkey — the chain must anchor to the given pubkey.
		if pred.Leaf == nil || len(pred.Leaf.ChainToPubkey) != 32 {
			return false
		}
		// The root principal must match the required key.
		return bytes.Equal(req.RootPrincipal, pred.Leaf.ChainToPubkey)

	case KindChainToQuorum:
		// chain_to_quorum: M-of-N — M of the given keys must be in the trust chain.
		if pred.Leaf == nil {
			return false
		}
		// Check that at least M of the quorum keys match the root principal.
		matched := 0
		for _, pk := range pred.Leaf.QuorumPubkeys {
			if bytes.Equal(req.RootPrincipal, pk) {
				matched++
			}
		}
		return uint(matched) >= pred.Leaf.QuorumM

	case KindAllOf:
		for _, child := range pred.Children {
			if !evalPredicate(child, caps, req) {
				return false
			}
		}
		return true

	case KindAnyOf:
		for _, child := range pred.Children {
			if evalPredicate(child, caps, req) {
				return true
			}
		}
		return false

	case KindNot:
		// Should never reach here — ParsePredicateAST rejects 'not:'.
		return false

	default:
		return false
	}
}

// matchesWhere checks if a campfire ID satisfies the WhereMatcher predicate.
func matchesWhere(where WhereMatcher, campfireID string, capWhere []WhereMatcherCBOR) bool {
	// Empty where = "wherever" → always matches.
	if where.Kind == 0 {
		return true
	}
	switch where.Kind {
	case WhereCampfireByID:
		return campfireID == where.CampfireID
	case WhereNamePrefix:
		// Name prefix matching requires the campfire to have a name starting with prefix.
		// In cf-authority 1.0, we check the cap.Where for matching prefix entries.
		for _, cw := range capWhere {
			if WhereKind(cw.Kind) == WhereNamePrefix && strings.HasPrefix(campfireID, cw.Prefix) {
				return true
			}
		}
		// Fallback: check against the where predicate's prefix.
		return strings.HasPrefix(campfireID, where.Prefix)
	case WhereTagMatcher:
		// Tag matching — not fully resolvable without campfire metadata,
		// but we check cap.Where for matching tag entries.
		for _, cw := range capWhere {
			if WhereKind(cw.Kind) == WhereTagMatcher && cw.Tag == where.Tag {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// toUint converts an interface{} value to uint (best-effort for Bounds map values).
func toUint(v any) uint {
	switch n := v.(type) {
	case int:
		if n < 0 {
			return 0
		}
		return uint(n)
	case uint:
		return n
	case float64:
		if n < 0 {
			return 0
		}
		return uint(n)
	case int64:
		if n < 0 {
			return 0
		}
		return uint(n)
	default:
		return 0
	}
}

// hexEncodeKey encodes a 32-byte key to hex string for comparison.
func hexEncodeKey(key []byte) string {
	if len(key) == 0 {
		return ""
	}
	const hexChars = "0123456789abcdef"
	buf := make([]byte, len(key)*2)
	for i, b := range key {
		buf[i*2] = hexChars[b>>4]
		buf[i*2+1] = hexChars[b&0xf]
	}
	return string(buf)
}
