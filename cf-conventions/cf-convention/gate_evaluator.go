package convention

// gate_evaluator.go — GateEvaluator interface and supporting types (L2-owned contract).
//
// Tracked bead: campfireagent-2b9 (Stage 2 — interface declaration).
//
// WHY THIS FILE EXISTS IN L2 (not L3):
//
// The ConventionDispatcher (L2 machinery) consults a GateEvaluator before
// dispatching any convention operation that carries a GrantChain. The evaluator
// decides whether the presented delegation chain authorises the dispatch.
//
// L2 declares the interface and the contract types so that:
//   - The L3 implementation (cf-authority/trust, Stage 3) can satisfy this interface
//     without creating an import cycle (L3 imports L2, not the reverse).
//   - The dispatcher can hold a GateEvaluator field today and compile cleanly
//     before the Stage 3 implementation lands.
//
// LAYER SEPARATION INVARIANT (design v2 §4.6, campfireagent-4d7):
//   - This file MUST NOT import cf-conventions/cf-authority/* (L3).
//   - Depguard rule L2-no-extensions enforces this boundary.
//   - An adversarial test in gate_evaluator_test.go proves the rule fires on
//     an intentional violation (depguard probe).
//
// STAGE WIRING:
//   Stage 2 (this bead): interface declared; AllowAllGateEvaluator stub present.
//   Stage 3 (campfireagent-02a): ConventionDispatcher.GateEvaluator field wired;
//     cf-authority/trust default implementation satisfies this interface.
//
// CONTRACT TYPES (L2-owned, frozen at cf-authority 1.0):
//   These types are the cross-layer contract. cf-authority/trust imports them.
//   Any change to these types requires a design review.
//
// OMISSIONS vs. cf-authority/trust.EvaluateRequest:
//   The L3 EvaluateRequest uses cf-authority/trust.PredicateAST (an L3 type).
//   The L2 contract uses SerializedPredicate ([]byte, CBOR-encoded AST) to avoid
//   importing the AST implementation. The L3 adapter decodes SerializedPredicate
//   into a PredicateAST before calling the chain walker.

import (
	"context"
	"crypto/ed25519"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Decision / DenyReason
// ─────────────────────────────────────────────────────────────────────────────

// Decision is the three-way gate result (frozen at cf-authority 1.0).
//
// Dispatchers MUST treat Unresolvable as Deny (fail closed) and MAY synthesize
// a delegation:request future to populate the local view.
type Decision uint8

const (
	// GateAllow — request is permitted per the delegation chain and gate predicate.
	GateAllow Decision = iota + 1
	// GateDeny — request is denied; Reason is populated on EvaluateResult.
	GateDeny
	// GateUnresolvable — chain is truncated; evaluator cannot reach a decision.
	GateUnresolvable
)

// DenyReason enumerates the stable deny codes (frozen at cf-authority 1.0).
// Two implementations that agree on Deny MUST agree on the reason code for any
// canonical input. The conformance harness (cf-authority/trust/conformance) asserts this.
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

// ─────────────────────────────────────────────────────────────────────────────
// GateOpRequest — L2-owned description of the operation under gate.
// ─────────────────────────────────────────────────────────────────────────────

// GateOpRequest describes the convention operation being gate-evaluated.
//
// The L2 dispatcher populates this from the parsed conventionOpPayload before
// calling GateEvaluator.Evaluate.
type GateOpRequest struct {
	// Convention is the exact convention name (e.g. "swarm-coordination").
	Convention string

	// Operation is the operation name within the convention (e.g. "claim-item").
	Operation string

	// CampfireID is the target campfire (hex-encoded).
	CampfireID string

	// Tags are the composed request tags on the incoming message.
	Tags []string

	// Sender is the public key of the message sender.
	Sender ed25519.PublicKey

	// SerializedPredicate is the gate predicate compiled from the convention
	// declaration, encoded as CBOR (GrantPayload §1.4).
	//
	// L2 treats this as an opaque byte slice. The L3 evaluator decodes it
	// into a trust.PredicateAST before walking the grant chain. Using []byte
	// here avoids importing L3's predicate type definitions (design v2 §4.6).
	SerializedPredicate []byte
}

// ─────────────────────────────────────────────────────────────────────────────
// EvaluateRequest / EvaluateResult
// ─────────────────────────────────────────────────────────────────────────────

// GateChainMessage is a single hop in the delegation chain.
//
// It carries the raw CBOR-encoded GrantPayload and the campfire message ID
// (used for MissingMessageID in GateUnresolvable results).
type GateChainMessage struct {
	// ID is the campfire message ID (used for MissingMessageID on Unresolvable).
	ID string

	// Payload is the raw CBOR-encoded GrantPayload (§1.2).
	// The evaluator decodes and verifies the payload. Signature verification is
	// the caller's responsibility — the evaluator assumes pre-verified inputs.
	Payload []byte
}

// GateRevocationViewEntry describes the local view of revocation state for one campfire.
//
// Empty/missing entries are NOT "no revocations" — they are "no view", which the
// evaluator MAY surface as GateUnresolvable depending on OwnerPolicy.MaxRevocationStaleness.
type GateRevocationViewEntry struct {
	CampfireID          string
	LatestObservedMsgID string
	ObservedAt          time.Time
}

// GateOwnerPolicy is the owner-of-record's policy ceiling.
//
// Composition order: owner > parent > declaration (one-directional, §2.4.2).
type GateOwnerPolicy struct {
	// MaxRevocationStaleness is the owner's freshness requirement on the revocation view.
	MaxRevocationStaleness time.Duration

	// MinLevelOverride is the owner-imposed operator level floor (0 = no override).
	MinLevelOverride int

	// BlanketDeny lists convention:op patterns blanket-denied by the owner.
	BlanketDeny []string
}

// EvaluateRequest is the input to GateEvaluator.Evaluate.
//
// All time-sensitive and store-sensitive inputs are passed explicitly so the
// evaluator remains a pure function of its inputs (§4.1 determinism invariant).
//
// INVARIANT: GateEvaluator.Evaluate MUST be a pure function:
//   - Same inputs → same output.
//   - No clock reads inside the call (CurrentTime is an input).
//   - No store reads inside the call (ChainMessages is an input).
//   - No global mutable state.
//
// Dispatchers MUST treat GateUnresolvable as GateDeny (fail closed).
type EvaluateRequest struct {
	// Request describes the operation under gate.
	Request GateOpRequest

	// ChainMessages is the verified delegation chain, ordered sender→anchor.
	// May be empty (anchor-self case). Signature verification is the caller's
	// responsibility — the evaluator assumes pre-verified inputs.
	ChainMessages []GateChainMessage

	// RootPrincipal is the trust anchor at which ChainMessages terminates.
	// Expressed as a key (chain_to predicate is by KEY, not name).
	RootPrincipal ed25519.PublicKey

	// CurrentTime is the wall-clock time used for expiry/freshness checks.
	// Passed as input (not read from a clock) to ensure §4.1 determinism.
	CurrentTime time.Time

	// RevocationView is the local observation of revocation state.
	RevocationView []GateRevocationViewEntry

	// OwnerPolicy is the owner-of-record's ceiling.
	OwnerPolicy GateOwnerPolicy
}

// EvaluateResult is the output of GateEvaluator.Evaluate.
type EvaluateResult struct {
	// Decision is the three-way gate result.
	Decision Decision

	// Reason is set when Decision == GateDeny.
	// Must be non-empty for Deny. Stable codes are part of the conformance contract.
	Reason DenyReason

	// MissingMessageID is set when Decision == GateUnresolvable.
	// Identifies the chain message the evaluator could not resolve.
	MissingMessageID string
}

// ─────────────────────────────────────────────────────────────────────────────
// GateEvaluator interface (design v2 §2.5.1)
// ─────────────────────────────────────────────────────────────────────────────

// GateEvaluator decides whether a convention operation is permitted, denied, or
// unresolvable given a delegation chain, root principal, current time,
// freshness-bounded revocation view, and owner policy.
//
// Implementations MUST be pure functions of their inputs (§4.1 determinism).
// Implementations MUST fail closed (§4.2): MUST NOT return GateAllow on
// unresolvable inputs.
//
// The interface is part of the cf-authority 1.0 frozen API surface.
// It is declared here in L2 so the dispatcher can hold a reference and so
// that cf-authority/trust (L3) can satisfy it without creating an import cycle.
//
// Stage wiring: Stage 3 (campfireagent-02a) wires the cf-authority/trust
// default implementation to ConventionDispatcher.GateEvaluator.
type GateEvaluator interface {
	Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResult
}

// ─────────────────────────────────────────────────────────────────────────────
// AllowAllGateEvaluator — stub default (Stage 2 only, replaced in Stage 3)
// ─────────────────────────────────────────────────────────────────────────────

// AllowAllGateEvaluator is a stub GateEvaluator that unconditionally allows all
// requests. It is the default implementation used before Stage 3 wires the real
// cf-authority/trust evaluator to the dispatcher.
//
// IMPORTANT: This stub MUST NOT be used in production after Stage 3 lands.
// It exists solely so that the dispatcher compiles and unit tests run between
// Stage 2 and Stage 3. Replace with the cf-authority/trust default implementation
// when campfireagent-02a is merged (design v2 §2.5.1).
//
// Stage 3 transition: ConventionDispatcher will accept a WithGateEvaluator option;
// the stub is used when no evaluator is provided, preserving backwards compatibility
// for callers that do not yet supply a real evaluator.
type AllowAllGateEvaluator struct{}

// Evaluate always returns GateAllow. See AllowAllGateEvaluator for usage constraints.
func (AllowAllGateEvaluator) Evaluate(_ context.Context, _ EvaluateRequest) EvaluateResult {
	return EvaluateResult{Decision: GateAllow}
}
