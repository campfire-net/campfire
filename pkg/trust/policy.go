// Package trust implements the local policy engine per Trust Convention v0.2.
//
// v0.2 inverts the trust model: the agent's own keypair is the trust anchor.
// There is no compiled root key, no chain verification, no external authority.
// Conventions are adopted voluntarily. The policy engine manages adopted
// conventions and evaluates incoming declarations against local policy.
package trust

import (
	"fmt"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
)

// TrustStatus is the envelope trust_status field value (Trust Convention v0.2 §6.2).
// Reports compatibility with the agent's local policy — not position in a root chain.
type TrustStatus string

const (
	// TrustAdopted — campfire's conventions are adopted in the agent's local policy.
	// Full interoperability.
	TrustAdopted TrustStatus = "adopted"

	// TrustCompatible — campfire's conventions have matching semantic fingerprints
	// but are not explicitly adopted. Interoperability likely.
	TrustCompatible TrustStatus = "compatible"

	// TrustDivergent — campfire has conventions with mismatched fingerprints.
	// Interoperability uncertain.
	TrustDivergent TrustStatus = "divergent"

	// TrustUnknown — campfire has conventions the agent has not encountered before,
	// or was joined directly by ID without convention comparison. Formerly "none".
	TrustUnknown TrustStatus = "unknown"
)

// AdoptionSource identifies where a convention was adopted from.
type AdoptionSource string

const (
	SourceSeed   AdoptionSource = "seed"
	SourceLocal  AdoptionSource = "local"
	SourcePeer   AdoptionSource = "peer"
	SourceManual AdoptionSource = "manual"
)

// AdoptedConvention records a locally adopted convention declaration.
type AdoptedConvention struct {
	Convention  string         `json:"convention"`
	Operation   string         `json:"operation"`
	Version     string         `json:"version"`
	Fingerprint string         `json:"fingerprint"` // sha256:<hex>
	Source      AdoptionSource `json:"source"`
	SourceID    string         `json:"source_id,omitempty"` // campfire ID or peer key
	AdoptedAt   time.Time      `json:"adopted_at"`
}

// AutoAdoptScope controls which declarations may be auto-adopted from a source.
type AutoAdoptScope string

const (
	AutoAdoptDisabled   AutoAdoptScope = "disabled"
	AutoAdoptNewOnly    AutoAdoptScope = "new-only"
	AutoAdoptNewUpdates AutoAdoptScope = "new-and-updates"
)

// AutoAdoptRule configures auto-adoption for a specific source campfire.
// Fingerprint mismatches always block auto-adoption regardless of this rule.
type AutoAdoptRule struct {
	SourceID string         `json:"source_id"`
	Scope    AutoAdoptScope `json:"scope"`
}

// PolicyEngine manages locally adopted conventions and evaluates incoming
// declarations against local policy per Trust Convention v0.2 §4, §5.
//
// The agent's own keypair is the trust anchor. The policy engine:
//   - Manages adopted conventions (what the agent trusts)
//   - Evaluates incoming declarations against local policy
//   - Computes trust_status and fingerprint_match for the safety envelope
//   - Enforces auto-adoption constraints (fingerprint mismatch blocks auto-adoption)
//   - Maintains bootstrap ordering (trust → provenance → gated ops)
type PolicyEngine struct {
	mu         sync.RWMutex
	adopted    map[string]*AdoptedConvention // key: convention+":"+operation
	autoAdopt  map[string]*AutoAdoptRule     // key: sourceID
	initialized bool
}

// NewPolicyEngine creates an empty policy engine.
// Call SeedConventions to populate with initial seed declarations.
func NewPolicyEngine() *PolicyEngine {
	return &PolicyEngine{
		adopted:   make(map[string]*AdoptedConvention),
		autoAdopt: make(map[string]*AutoAdoptRule),
	}
}

// adoptKey returns the map key for a convention+operation pair.
func adoptKey(conventionSlug, operation string) string {
	return conventionSlug + ":" + operation
}

// SeedConventions promotes seed declarations into the policy engine as defaults.
// Seeds are a starter kit — they carry no special authority. Per §4.3, the
// agent can override, replace, extend, or remove any of them.
func (e *PolicyEngine) SeedConventions(decls []*convention.Declaration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, decl := range decls {
		fp := SemanticFingerprint(decl)
		key := adoptKey(decl.Convention, decl.Operation)
		if _, exists := e.adopted[key]; !exists {
			e.adopted[key] = &AdoptedConvention{
				Convention:  decl.Convention,
				Operation:   decl.Operation,
				Version:     decl.Version,
				Fingerprint: fp,
				Source:      SourceSeed,
				AdoptedAt:   time.Now(),
			}
		}
	}
	e.initialized = true
}

// MarkInitialized marks the policy engine as initialized (even with no seed declarations).
// Call this after the seed phase completes so the engine reports its state correctly.
func (e *PolicyEngine) MarkInitialized() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.initialized = true
}

// IsInitialized reports whether the trust layer has completed initialization.
// Per §4.6: trust initializes first; operator_provenance reports null during this phase.
func (e *PolicyEngine) IsInitialized() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.initialized
}

// Adopt adds or replaces a convention in the local policy.
// This is the explicit operator action for adopting a convention.
func (e *PolicyEngine) Adopt(decl *convention.Declaration, source AdoptionSource, sourceID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fp := SemanticFingerprint(decl)
	key := adoptKey(decl.Convention, decl.Operation)
	e.adopted[key] = &AdoptedConvention{
		Convention:  decl.Convention,
		Operation:   decl.Operation,
		Version:     decl.Version,
		Fingerprint: fp,
		Source:      source,
		SourceID:    sourceID,
		AdoptedAt:   time.Now(),
	}
}

// Revoke removes a convention from the local policy.
func (e *PolicyEngine) Revoke(conventionSlug, operation string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.adopted, adoptKey(conventionSlug, operation))
}

// ListAdopted returns all currently adopted conventions.
func (e *PolicyEngine) ListAdopted() []*AdoptedConvention {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]*AdoptedConvention, 0, len(e.adopted))
	for _, ac := range e.adopted {
		cp := *ac
		result = append(result, &cp)
	}
	return result
}

// GetAdopted returns the adopted convention for a specific convention+operation, if any.
func (e *PolicyEngine) GetAdopted(conventionSlug, operation string) (*AdoptedConvention, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ac, ok := e.adopted[adoptKey(conventionSlug, operation)]
	if !ok {
		return nil, false
	}
	cp := *ac
	return &cp, true
}

// SetAutoAdoptRule configures auto-adoption behavior for a source campfire.
// Per §5.2.1: fingerprint mismatches always block auto-adoption regardless of scope.
func (e *PolicyEngine) SetAutoAdoptRule(sourceID string, scope AutoAdoptScope) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.autoAdopt[sourceID] = &AutoAdoptRule{SourceID: sourceID, Scope: scope}
}

// EvaluationResult holds the result of evaluating an incoming declaration.
type EvaluationResult struct {
	Status          TrustStatus
	FingerprintMatch bool
	// Held is true when auto-adoption was blocked due to a fingerprint mismatch.
	Held bool
	// Reason explains the trust status determination.
	Reason string
}

// Evaluate assesses an incoming declaration against local policy.
// Returns the trust status and fingerprint match result per §6.2.
//
// Trust status taxonomy (v0.2):
//   - "adopted":    convention is in local policy, fingerprints match
//   - "compatible": fingerprints match but convention is not explicitly adopted
//   - "divergent":  convention is adopted/known but fingerprints mismatch
//   - "unknown":    convention not encountered before
func (e *PolicyEngine) Evaluate(decl *convention.Declaration) EvaluationResult {
	fp := SemanticFingerprint(decl)

	e.mu.RLock()
	adopted, isAdopted := e.adopted[adoptKey(decl.Convention, decl.Operation)]
	e.mu.RUnlock()

	if !isAdopted {
		// Unknown — not in local policy.
		return EvaluationResult{
			Status:           TrustUnknown,
			FingerprintMatch: false,
			Reason:           "convention not in local policy",
		}
	}

	fingerprintMatch := adopted.Fingerprint == fp

	if fingerprintMatch {
		return EvaluationResult{
			Status:           TrustAdopted,
			FingerprintMatch: true,
			Reason:           "convention adopted, fingerprints match",
		}
	}

	// Adopted but fingerprints diverge.
	return EvaluationResult{
		Status:           TrustDivergent,
		FingerprintMatch: false,
		Reason:           fmt.Sprintf("convention adopted but fingerprint mismatch: local=%s incoming=%s", adopted.Fingerprint, fp),
	}
}

// EvaluateCampfire determines the trust status for a campfire based on its declarations.
// If no declarations are provided, returns TrustUnknown.
// If all declarations match adopted fingerprints → TrustAdopted.
// If any declaration fingerprints match but convention is not adopted → TrustCompatible.
// If any fingerprint mismatches against an adopted convention → TrustDivergent.
// Otherwise → TrustUnknown.
func (e *PolicyEngine) EvaluateCampfire(decls []*convention.Declaration) (TrustStatus, bool) {
	if len(decls) == 0 {
		return TrustUnknown, false
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	allAdopted := true
	anyMatch := false
	anyDivergent := false

	for _, decl := range decls {
		fp := SemanticFingerprint(decl)
		adopted, isAdopted := e.adopted[adoptKey(decl.Convention, decl.Operation)]

		if !isAdopted {
			allAdopted = false
			// Not in policy — check if compatible fingerprint (hypothetical match).
			// We check all known adoptions for same fingerprint.
			for _, ac := range e.adopted {
				if ac.Fingerprint == fp {
					anyMatch = true
					break
				}
			}
			continue
		}

		if adopted.Fingerprint == fp {
			anyMatch = true
		} else {
			anyDivergent = true
			allAdopted = false
		}
	}

	if anyDivergent {
		return TrustDivergent, false
	}
	if allAdopted && anyMatch {
		return TrustAdopted, true
	}
	if anyMatch {
		return TrustCompatible, true
	}
	return TrustUnknown, false
}

// TryAutoAdopt attempts to auto-adopt an incoming declaration from a source.
// Returns (adopted=true) if auto-adoption succeeded.
// Returns (adopted=false, held=true) if blocked by fingerprint mismatch.
// Returns (adopted=false, held=false) if auto-adoption is disabled or not configured.
//
// Per §5.2.1:
//   - Fingerprint mismatches ALWAYS block auto-adoption.
//   - NewOnly scope: only adopts conventions not yet in local policy.
//   - NewAndUpdates scope: also adopts same-fingerprint version updates.
func (e *PolicyEngine) TryAutoAdopt(decl *convention.Declaration, sourceID string) (adopted bool, held bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rule, hasRule := e.autoAdopt[sourceID]
	if !hasRule || rule.Scope == AutoAdoptDisabled {
		return false, false
	}

	key := adoptKey(decl.Convention, decl.Operation)
	fp := SemanticFingerprint(decl)
	existing, isAdopted := e.adopted[key]

	if isAdopted {
		// Already adopted. Check fingerprint match.
		if existing.Fingerprint != fp {
			// Fingerprint mismatch — MUST block auto-adoption per §5.2.1.
			return false, true
		}
		// Same fingerprint — version update if NewAndUpdates scope.
		if rule.Scope == AutoAdoptNewUpdates {
			e.adopted[key] = &AdoptedConvention{
				Convention:  decl.Convention,
				Operation:   decl.Operation,
				Version:     decl.Version,
				Fingerprint: fp,
				Source:      SourcePeer,
				SourceID:    sourceID,
				AdoptedAt:   time.Now(),
			}
			return true, false
		}
		// NewOnly scope: already adopted, no action.
		return false, false
	}

	// New convention, not yet adopted.
	if rule.Scope == AutoAdoptNewOnly || rule.Scope == AutoAdoptNewUpdates {
		e.adopted[key] = &AdoptedConvention{
			Convention:  decl.Convention,
			Operation:   decl.Operation,
			Version:     decl.Version,
			Fingerprint: fp,
			Source:      SourcePeer,
			SourceID:    sourceID,
			AdoptedAt:   time.Now(),
		}
		return true, false
	}

	return false, false
}

// ConventionCompatibility holds the compatibility result for a single convention
// declaration. Produced by CompareCampfireDeclarations per Trust v0.2 §5.3.
type ConventionCompatibility struct {
	Convention       string      `json:"convention"`
	Operation        string      `json:"operation"`
	Status           TrustStatus `json:"status"`
	FingerprintMatch bool        `json:"fingerprint_match"`
	// LocalFingerprint is the fingerprint of the locally adopted convention (sha256:hex).
	LocalFingerprint string `json:"local_fingerprint,omitempty"`
	// RemoteFingerprint is the fingerprint of the campfire's declaration (sha256:hex).
	RemoteFingerprint string `json:"remote_fingerprint,omitempty"`
}

// CampfireCompatibilityReport summarizes per-convention fingerprint compatibility
// between a campfire and the local policy engine.
// Produced by CompareCampfireDeclarations for cf join and cf bridge.
type CampfireCompatibilityReport struct {
	// OverallStatus is the aggregate trust status for the campfire per §6.2.
	OverallStatus TrustStatus `json:"trust_status"`
	// FingerprintMatch is true when all campfire declarations match local policy fingerprints.
	FingerprintMatch bool `json:"fingerprint_match"`
	// Conventions is the per-convention breakdown.
	Conventions []ConventionCompatibility `json:"conventions"`
}

// CompareCampfireDeclarations evaluates a campfire's declarations against the local
// policy engine and returns a per-convention compatibility report. This is the
// fingerprint comparison logic run automatically on cf join per Trust v0.2 §5.3.
//
// The overall trust status follows EvaluateCampfire semantics:
//   - "adopted":    all declarations are adopted and fingerprints match
//   - "compatible": fingerprints match but not explicitly adopted
//   - "divergent":  any adopted convention has a fingerprint mismatch
//   - "unknown":    no declarations, or all conventions unknown to local policy
func (e *PolicyEngine) CompareCampfireDeclarations(decls []*convention.Declaration) *CampfireCompatibilityReport {
	report := &CampfireCompatibilityReport{
		Conventions: make([]ConventionCompatibility, 0, len(decls)),
	}

	if len(decls) == 0 {
		report.OverallStatus = TrustUnknown
		report.FingerprintMatch = false
		return report
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	allAdopted := true
	allMatch := true
	anyDivergent := false

	for _, decl := range decls {
		incoming := SemanticFingerprint(decl)
		adopted, isAdopted := e.adopted[adoptKey(decl.Convention, decl.Operation)]

		cc := ConventionCompatibility{
			Convention:        decl.Convention,
			Operation:         decl.Operation,
			RemoteFingerprint: incoming,
		}

		if !isAdopted {
			cc.Status = TrustUnknown
			cc.FingerprintMatch = false
			allAdopted = false
			allMatch = false
		} else {
			cc.LocalFingerprint = adopted.Fingerprint
			cc.FingerprintMatch = adopted.Fingerprint == incoming
			if cc.FingerprintMatch {
				cc.Status = TrustAdopted
			} else {
				cc.Status = TrustDivergent
				anyDivergent = true
				allAdopted = false
				allMatch = false
			}
		}
		report.Conventions = append(report.Conventions, cc)
	}

	if anyDivergent {
		report.OverallStatus = TrustDivergent
		report.FingerprintMatch = false
	} else if allAdopted && allMatch {
		report.OverallStatus = TrustAdopted
		report.FingerprintMatch = true
	} else if allMatch {
		report.OverallStatus = TrustCompatible
		report.FingerprintMatch = true
	} else {
		report.OverallStatus = TrustUnknown
		report.FingerprintMatch = false
	}

	return report
}
