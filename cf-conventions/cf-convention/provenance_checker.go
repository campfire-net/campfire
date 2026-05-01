package convention

// provenance_checker.go — ProvenanceCheckerV2 interface declaration (L2 contract).
//
// Bead: campfireagent-2ac
// Design: 0.30-design.md §4.2 — Two interfaces declared at L2 with default
// implementations supplied at L3:
//   - GateEvaluator interface — default impl in cf-authority/trust.
//   - ProvenanceChecker interface — default impl in cf-authority/provenance.
//
// This file declares the v2 ProvenanceCheckerV2 interface and its supporting types.
// L2 owns the contract types (ProvenanceRequest, ProvenanceResult, ProvenanceLevel).
// L3 cf-authority/provenance provides the real implementation.
//
// The v1 interface (ProvenanceChecker, Level(key string) int) is preserved in
// executor.go for backward compatibility with existing implementations.
//
// Layer: L2 — Convention machinery (cf-conventions/cf-convention/).
// Does NOT import L3 (depguard L2-no-authority rule enforces this constraint).

import "context"

// ProvenanceLevel is the operator provenance level (0–3).
// The four levels match pkg/provenance/provenance.go and design v2 §2.1.
type ProvenanceLevel int

const (
	// ProvenanceLevelAnonymous (0): valid keypair only. Nothing is known about the operator.
	ProvenanceLevelAnonymous ProvenanceLevel = 0
	// ProvenanceLevelClaimed (1): self-asserted operator identity. Tainted — not verified.
	ProvenanceLevelClaimed ProvenanceLevel = 1
	// ProvenanceLevelContactable (2): verified by challenge/response (human responded)
	// or message traversed a blind-relay hop.
	ProvenanceLevelContactable ProvenanceLevel = 2
	// ProvenanceLevelPresent (3): level 2 within a freshness window.
	ProvenanceLevelPresent ProvenanceLevel = 3
)

// ProvenanceRequest is the input to ProvenanceCheckerV2.CheckProvenance.
// L2 owns this type so the dispatcher can invoke CheckProvenance without importing L3.
type ProvenanceRequest struct {
	// SenderKey is the public key (hex) of the sender whose provenance level is queried.
	SenderKey string
}

// ProvenanceResult is the output of ProvenanceCheckerV2.CheckProvenance.
type ProvenanceResult struct {
	// Level is the resolved provenance level for the sender.
	Level ProvenanceLevel
}

// ProvenanceCheckerV2 is the v2 provenance interface declared at L2.
// Default implementation is at L3 (cf-authority/provenance).
//
// Implementations MUST be deterministic: same inputs → same output.
// Implementations MUST be concurrency-safe.
//
// The v2 interface supersedes the v1 ProvenanceChecker (Level(key string) int)
// for new callers. The Executor accepts both via WithProvenance (v1) and
// WithProvenanceV2 (v2); v2 takes precedence when both are set.
//
// Design §4.2: "ProvenanceChecker interface — default impl in cf-authority/provenance."
type ProvenanceCheckerV2 interface {
	// CheckProvenance returns the provenance level for the sender identified in
	// req.SenderKey. Callers use the returned Level to enforce min_operator_level
	// gates declared in convention operations.
	//
	// ctx may carry deadline or cancellation. Implementations should respect ctx.
	CheckProvenance(ctx context.Context, req ProvenanceRequest) ProvenanceResult
}

// allowAllProvenanceChecker is the stub default that always returns ProvenanceLevelPresent (3).
// Exported via NewAllowAllProvenanceChecker. Stage 3 will replace this with the real
// implementation from cf-authority/provenance.
type allowAllProvenanceChecker struct{}

// NewAllowAllProvenanceChecker returns a ProvenanceCheckerV2 stub that always returns
// ProvenanceLevelPresent (3) for any sender key.
//
// Use this:
//   - In tests that do not need provenance enforcement.
//   - As the default scaffold before Stage 3 supplies the real implementation.
//
// Do NOT use in production — it bypasses all provenance gates.
func NewAllowAllProvenanceChecker() ProvenanceCheckerV2 {
	return &allowAllProvenanceChecker{}
}

// CheckProvenance implements ProvenanceCheckerV2.
// Always returns ProvenanceLevelPresent (3), permitting all provenance gates.
func (a *allowAllProvenanceChecker) CheckProvenance(_ context.Context, _ ProvenanceRequest) ProvenanceResult {
	return ProvenanceResult{Level: ProvenanceLevelPresent}
}
