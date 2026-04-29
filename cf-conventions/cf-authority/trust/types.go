// Package trust implements the cf-authority 1.0 GateEvaluator — the L3
// scoped-grant, bounded-transitivity, chain-evaluating authorization system.
//
// Spec: docs/cf-authority-spec.md (frozen at cf-authority 1.0, 2026-04-28).
//
// Layer: L3 — RFC convention (cf-conventions/cf-authority/).
// Depends on: pkg/message (Message type), pkg/encoding (CBOR codec).
// Does NOT import cf-protocol/ (would violate the L1-narrow boundary).
//
// # Invariants
//
// GateEvaluator.Evaluate is a PURE FUNCTION of its inputs:
//   - Same inputs → same output, byte-for-byte.
//   - No clock reads inside the call (CurrentTime is an input).
//   - No store reads inside the call (ChainMessages is an input).
//   - No global mutable state.
//
// §4.1 determinism is enforced by the conformance harness (3× re-run).
// §4.2 fail-closed: MUST NOT return Allow on unresolvable inputs.
package trust

import (
	"context"
	"crypto/ed25519"
	"time"
)

// Decision is the three-way gate result (frozen at cf-authority 1.0).
type Decision uint8

const (
	// Allow — request is permitted per the delegation chain and gate predicate.
	Allow Decision = iota + 1
	// Deny — request is denied; Reason is populated.
	Deny
	// Unresolvable — chain is truncated; evaluator cannot reach a decision.
	// Dispatchers MUST treat Unresolvable as Deny (fail closed) and MAY
	// synthesize a delegation:request future to populate the local view.
	Unresolvable
)

// DenyReason enumerates the stable deny codes (frozen at cf-authority 1.0).
// Two implementations that agree on Deny MUST agree on the reason code for
// any canonical input. The conformance harness asserts this.
type DenyReason string

const (
	DenyExpired         DenyReason = "expired"              // grant.until < CurrentTime
	DenyRevoked         DenyReason = "revoked"              // active revocation in view
	DenyDepthExceeded   DenyReason = "depth_exceeded"       // chain depth > 2
	DenyScopeMismatch   DenyReason = "scope_mismatch"       // grant scope does not cover request
	DenyScopeWidening   DenyReason = "scope_widening"       // child claimed wider than parent
	DenyStaleRevocation DenyReason = "stale_revocation"     // view stale beyond max staleness
	DenyReservedOpFloor DenyReason = "reserved_op_floor"    // reserved-op depth/level floor
	DenyOwnerCeiling    DenyReason = "owner_ceiling"        // owner policy blanket denies
	DenyPredicate       DenyReason = "predicate_unsatisfied" // gate predicate not satisfied
	DenyStoreRead       DenyReason = "store_read_error"     // chain truncated; fail-closed
)

// GateEvaluator decides whether a request is permitted, denied, or
// unresolvable given a delegation chain, root principal, current time,
// freshness-bounded revocation view, and owner policy.
//
// Frozen at cf-authority 1.0. Implementations MUST be pure functions of
// their inputs (§4.1). Implementations MUST fail closed (§4.2).
type GateEvaluator interface {
	Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResult
}

// EvaluateRequest is the input to GateEvaluator.Evaluate.
//
// All time-sensitive and store-sensitive inputs are passed explicitly so
// the evaluator remains a pure function of its inputs (§4.1 determinism).
type EvaluateRequest struct {
	// Request describes the operation under gate.
	Request OpRequest

	// ChainMessages is the verified delegation chain, ordered sender→anchor.
	// May be empty (anchor-self case). Signature verification is the caller's
	// responsibility — the evaluator assumes pre-verified inputs.
	//
	// Each element is a raw CBOR-encoded grant payload (GrantPayload §1.2).
	ChainMessages []ChainMessage

	// RootPrincipal is the trust anchor at which ChainMessages terminates.
	// Expressed as a key (chain_to predicate is by KEY, not name).
	RootPrincipal ed25519.PublicKey

	// CurrentTime is the wall-clock time used for expiry/freshness checks.
	// Passed as input (not read from a clock) to ensure determinism.
	CurrentTime time.Time

	// RevocationView is the local observation of revocation state.
	// Empty/missing entries are NOT "no revocations" — they are "no view,"
	// which the evaluator MAY surface as Unresolvable depending on
	// OwnerPolicy.MaxRevocationStaleness.
	RevocationView []RevocationViewEntry

	// OwnerPolicy is the owner-of-record's ceiling.
	// Composition: owner > parent > declaration (one-directional).
	OwnerPolicy OwnerPolicy
}

// ChainMessage is a single hop in the delegation chain. It carries the raw
// signed CBOR payload (GrantPayload) and the campfire message ID for
// Unresolvable reporting.
type ChainMessage struct {
	// ID is the campfire message ID (used for MissingMessageID in Unresolvable).
	ID string
	// Payload is the raw CBOR-encoded GrantPayload (§1.2).
	// The evaluator decodes it. Signature verification is the caller's
	// responsibility — the evaluator assumes pre-verified inputs.
	Payload []byte
}

// OpRequest describes the operation being gate-evaluated.
type OpRequest struct {
	Convention  string            // exact convention name
	Operation   string            // operation name within convention
	CampfireID  string            // target campfire (hex)
	Tags        []string          // request tags (for tag-matcher gates)
	Sender      ed25519.PublicKey // request sender
	Predicate   PredicateAST      // the gate's compiled predicate tree
}

// RevocationViewEntry describes the local view of revocation state for one campfire.
type RevocationViewEntry struct {
	CampfireID          string
	LatestObservedMsgID string
	ObservedAt          time.Time
}

// OwnerPolicy is the owner-of-record's policy ceiling.
// Composition order: owner > parent > declaration (one-directional).
type OwnerPolicy struct {
	MaxRevocationStaleness time.Duration
	MinLevelOverride       int      // owner-imposed level floor (0 = no override)
	BlanketDeny            []string // convention:op patterns blanket-denied by owner
}

// EvaluateResult is the output of GateEvaluator.Evaluate.
type EvaluateResult struct {
	Decision Decision

	// Reason is set when Decision == Deny. Required, non-empty for Deny.
	// Stable codes are part of the conformance contract.
	Reason DenyReason

	// MissingMessageID is set when Decision == Unresolvable.
	// Identifies the chain message the evaluator could not resolve.
	MissingMessageID string
}
