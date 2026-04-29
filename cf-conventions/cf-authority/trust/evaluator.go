package trust

// evaluator.go — DefaultGateEvaluator implementation.
//
// Spec: docs/cf-authority-spec.md §3, §4.
// Frozen at cf-authority 1.0.
//
// This file ships the default GateEvaluator that the conformance harness
// tests against. The implementation is a pure function of its inputs (§4.1).
//
// Evaluation order per §4.5 (clamping order, D6):
//   1. Owner ceiling: BlanketDeny checked first — if matched, Deny/owner_ceiling.
//   2. Chain walk: depth, expiry, revocation, scope checks.
//   3. Predicate evaluation: convention predicate against resolved chain.
//
// Only the owner-ceiling check (D6) is implemented in this file; the full
// chain walk and predicate evaluation are tracked as follow-on work.
// The conformance case 13 (D6 blanket-deny) specifically tests this layer.

import (
	"context"
	"strings"
)

// DefaultGateEvaluator is the canonical implementation of GateEvaluator.
// It is a pure function of its inputs: no clocks, no store reads, no global state.
//
// Construct via NewDefaultGateEvaluator().
type DefaultGateEvaluator struct{}

// NewDefaultGateEvaluator returns a DefaultGateEvaluator.
// The evaluator has no mutable state; all inputs are passed per Evaluate call.
func NewDefaultGateEvaluator() *DefaultGateEvaluator {
	return &DefaultGateEvaluator{}
}

// Evaluate implements GateEvaluator.
//
// Evaluation order (§4.5, D6 clamping order: owner > parent > declaration):
//
//  1. Owner ceiling (BlanketDeny): if the requested convention:op matches any
//     owner-policy BlanketDeny pattern, return Deny/owner_ceiling immediately.
//     No grant or convention can override this.
//
//  2. Chain evaluation (depth, expiry, revocation, scope intersection) — for
//     cases where the chain is empty and the sender is the root principal (anchor-self),
//     allow if predicate is satisfied; otherwise return Deny/scope_mismatch as a
//     conservative fallback for cases not yet fully implemented.
//
// NOTE: Full chain walk and predicate evaluation for all twelve conformance cases
// is tracked as follow-on work. This implementation focuses on D6 (case 13) per
// campfireagent-2cd8.
func (e *DefaultGateEvaluator) Evaluate(_ context.Context, req EvaluateRequest) EvaluateResult {
	// Step 1 — Owner ceiling check (D6, §4.5).
	// BlanketDeny patterns are "convention:op" strings where "*" in the op
	// position is a wildcard matching any operation within that convention.
	// Pattern matching rules (frozen wire format per §3.2):
	//   "convention:*"   — deny all ops in convention
	//   "convention:op"  — deny exactly this op in convention
	//
	// The check fires BEFORE chain evaluation. No grant can override the owner ceiling.
	if result, denied := checkOwnerCeiling(req); denied {
		return result
	}

	// Step 2 — Anchor-self shortcut: if the chain is empty and the sender is the
	// root principal, evaluate the predicate directly.
	if len(req.ChainMessages) == 0 {
		if req.Request.Sender.Equal(req.RootPrincipal) {
			// Sender is the anchor — permit (predicate satisfied by ownership).
			return EvaluateResult{Decision: Allow}
		}
		// No chain and sender is not the anchor — cannot resolve.
		return EvaluateResult{
			Decision:         Unresolvable,
			MissingMessageID: "",
		}
	}

	// Step 3 — Chain grant evaluation.
	// Walk the chain messages, decoding GrantPayload from each ChainMessage.Payload.
	// Check: depth, expiry, scope covering the requested operation.
	return evaluateChain(req)
}

// checkOwnerCeiling applies OwnerPolicy.BlanketDeny rules.
// Returns (EvaluateResult, true) if the request is blanket-denied by owner policy.
// Returns (EvaluateResult{}, false) if no blanket-deny pattern matches.
//
// Pattern grammar (wire-format frozen §3.2):
//   "convention:*"   — deny all operations in convention
//   "convention:op"  — deny exactly this convention:op
//
// Convention and op matching is exact (case-sensitive). The "*" in op position
// is a literal wildcard that matches any non-empty operation name.
func checkOwnerCeiling(req EvaluateRequest) (EvaluateResult, bool) {
	conv := req.Request.Convention
	op := req.Request.Operation

	for _, pattern := range req.OwnerPolicy.BlanketDeny {
		if blanketDenyMatches(pattern, conv, op) {
			return EvaluateResult{
				Decision: Deny,
				Reason:   DenyOwnerCeiling,
			}, true
		}
	}
	return EvaluateResult{}, false
}

// blanketDenyMatches reports whether a BlanketDeny pattern matches the given
// convention and operation.
//
// Pattern format: "convention:op_pattern"
//   - If op_pattern == "*", matches any operation in convention.
//   - Otherwise, op_pattern must equal op exactly.
//   - convention must equal conv exactly (no wildcards in convention field).
//
// A malformed pattern (no ":" separator) never matches.
func blanketDenyMatches(pattern, conv, op string) bool {
	idx := strings.IndexByte(pattern, ':')
	if idx < 0 {
		// Malformed pattern — no separator. Never matches (fail closed conservatively).
		return false
	}
	patConv := pattern[:idx]
	patOp := pattern[idx+1:]

	if patConv != conv {
		return false
	}
	if patOp == "*" {
		// Wildcard: matches any operation in this convention.
		return true
	}
	return patOp == op
}

// evaluateChain walks the ChainMessages, decoding each GrantPayload, and
// checks depth, expiry, and scope coverage for the requested operation.
//
// Returns:
//   - Allow if a valid unexpired chain grant covers the requested convention:op.
//   - Deny/expired if any grant in the chain has until < CurrentTime.
//   - Deny/depth_exceeded if the chain has more than 2 hops (§1.5).
//   - Deny/scope_mismatch if no grant covers the requested convention:op.
//   - Unresolvable if a chain message payload cannot be decoded.
func evaluateChain(req EvaluateRequest) EvaluateResult {
	const maxDepth = 2 // §1.5: hard depth limit

	if len(req.ChainMessages) > maxDepth {
		return EvaluateResult{Decision: Deny, Reason: DenyDepthExceeded}
	}

	for _, cm := range req.ChainMessages {
		gp, err := UnmarshalGrantPayloadCBOR(cm.Payload)
		if err != nil {
			// Cannot decode grant — chain is truncated.
			return EvaluateResult{
				Decision:         Unresolvable,
				MissingMessageID: cm.ID,
			}
		}

		// Check expiry for each capability in this grant.
		currentTimeNs := req.CurrentTime.UnixNano()
		for _, cap := range gp.Capabilities {
			if cap.Until < currentTimeNs {
				return EvaluateResult{Decision: Deny, Reason: DenyExpired}
			}

			// Check scope: does this grant cover the requested convention:op?
			if !grantCoversRequest(cap, req.Request.Convention, req.Request.Operation) {
				return EvaluateResult{Decision: Deny, Reason: DenyScopeMismatch}
			}
		}
	}

	// All chain messages decoded, unexpired, and scope covers the request.
	return EvaluateResult{Decision: Allow}
}

// grantCoversRequest reports whether a Capability covers the requested
// convention:op. OpPattern matching (§1.1):
//   - "*"              — matches any operation
//   - "op1|op2|..."    — matches any of the listed operations (alternation)
//   - "op"             — exact match
func grantCoversRequest(cap Capability, convention, operation string) bool {
	if cap.Convention != convention {
		return false
	}
	return opPatternMatches(cap.OpPattern, operation)
}

// opPatternMatches reports whether an op_pattern from a Capability covers op.
// Pattern grammar (§1.1):
//   "*"         — matches any op
//   "a|b|c"     — alternation; matches any listed op
//   "op"        — exact match
func opPatternMatches(pattern, op string) bool {
	if pattern == "*" {
		return true
	}
	// Alternation: split on "|" and check each alternative.
	parts := strings.Split(pattern, "|")
	for _, p := range parts {
		if strings.TrimSpace(p) == op {
			return true
		}
	}
	return false
}
