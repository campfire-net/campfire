// Package provenance provides the production ProvenanceCheckerV2 implementation
// (cf-authority L3), replacing the allow-all stub shipped at L2
// (convention.NewAllowAllProvenanceChecker).
//
// The L2 convention.ProvenanceCheckerV2 interface is the contract; this package
// supplies a real implementation backed by an operator-provenance attestation
// store (pkg/provenance), which computes levels 0–3 per the Operator Provenance
// Convention v0.1. Consumers (rd, dontguess, social) wire it via
// Executor.WithProvenanceV2 / Server gates to enforce min_operator_level.
package provenance

import (
	"context"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/pkg/provenance"
)

// LevelSource resolves an operator provenance level (0–3) for a public-key hex.
// Both *provenance.Store and *provenance.FileStore satisfy it, as does any
// AttestationStore. It is the single dependency of the production checker, so
// callers can plug in any attestation backend without this package importing a
// concrete store type.
type LevelSource interface {
	Level(keyHex string) provenance.Level
}

// Checker is the production convention.ProvenanceCheckerV2. It resolves a
// sender's operator provenance level from an attestation-backed LevelSource and
// returns it for min_operator_level gating. It holds no mutable state beyond the
// source, so it is safe for concurrent use when the source is.
type Checker struct {
	src LevelSource
}

// NewChecker returns a production ProvenanceCheckerV2 backed by src.
// A nil src is permitted and fails closed: every sender resolves to
// ProvenanceLevelAnonymous (0), so gated operations are rejected rather than
// silently allowed (the opposite of the allow-all stub).
func NewChecker(src LevelSource) *Checker {
	return &Checker{src: src}
}

// CheckProvenance implements convention.ProvenanceCheckerV2.
func (c *Checker) CheckProvenance(_ context.Context, req convention.ProvenanceRequest) convention.ProvenanceResult {
	if c == nil || c.src == nil {
		return convention.ProvenanceResult{Level: convention.ProvenanceLevelAnonymous}
	}
	return convention.ProvenanceResult{Level: toConventionLevel(c.src.Level(req.SenderKey))}
}

// toConventionLevel maps a pkg/provenance.Level onto the L2 convention level.
// The two enums share identical semantics for 0–3; values outside that range
// clamp to the nearest valid level (defensive against future divergence).
func toConventionLevel(l provenance.Level) convention.ProvenanceLevel {
	switch {
	case l <= provenance.LevelAnonymous:
		return convention.ProvenanceLevelAnonymous
	case l == provenance.LevelClaimed:
		return convention.ProvenanceLevelClaimed
	case l == provenance.LevelContactable:
		return convention.ProvenanceLevelContactable
	default: // l >= LevelPresent
		return convention.ProvenanceLevelPresent
	}
}
