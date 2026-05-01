// Package conventionextension provides L3 behavioral operations for the
// convention-extension convention (AIETF convention-extension convention §18).
//
// This package is the L3 implementation of the convention-extension convention.
// It absorbs the seed declarations from L2 (via the seed sub-package) and adds
// the behavioral promote and supersede operations — validation logic for publishing
// and replacing convention declarations.
//
// Layer boundary: L3 (cf-convention-extension) may import L2 (cf-convention).
// The seed sub-package contains the built-in declarations; this package contains
// the behavioral validators.
//
// Path reconciliation (campfireagent-a40): renamed from cf-convention-extensions/
// (plural) to cf-convention-extension/ (singular) to match design v2 §4.3.
package conventionextension

import (
	"errors"
	"fmt"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// PromoteResult holds the outcome of a ValidatePromote call.
type PromoteResult struct {
	// Declaration is the validated declaration, populated on success.
	Declaration *convention.Declaration
	// Warnings are non-fatal issues found during validation.
	Warnings []string
}

// SupersedeResult holds the outcome of a ValidateSupersede call.
type SupersedeResult struct {
	// NewDeclaration is the validated incoming declaration.
	NewDeclaration *convention.Declaration
	// SupersededID is the message ID of the declaration being replaced.
	SupersededID string
	// Warnings are non-fatal issues found during validation.
	Warnings []string
}

// ErrPromoteDenied is returned when a declaration fails promote validation.
var ErrPromoteDenied = errors.New("promote denied")

// ErrSupersedeDenied is returned when a supersede fails validation.
var ErrSupersedeDenied = errors.New("supersede denied")

// ValidatePromote validates that a raw declaration payload is suitable for
// promotion to a convention registry campfire.
//
// Promote validation rules (convention-extension convention §10.1, §17):
//  1. The payload must parse as a valid convention:operation declaration.
//  2. For campfire_key operations, the declaration MUST be campfire-key-signed
//     (SignerType must be convention.SignerCampfireKey).
//  3. The declaration must not already have a supersedes field — promote is for
//     initial publication; supersede handles replacement.
//  4. The declaring campfire and signer campfire must match.
//
// The campfireKey and signerKey parameters are the campfire's public key and
// the message signer's key respectively (both hex-encoded). These are used by
// the parser's campfire-key trust escalation check (§10.1, §10.3).
func ValidatePromote(payload []byte, msgTags []string, signerKey, campfireKey string) (*PromoteResult, error) {
	decl, result, err := convention.Parse(msgTags, payload, signerKey, campfireKey, convention.DefaultDeniedTagPrefixes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse failed: %v", ErrPromoteDenied, err)
	}
	if !result.Valid {
		return nil, fmt.Errorf("%w: conformance check failed: %v", ErrPromoteDenied, result.Warnings)
	}

	// Rule 3: promote is for initial publication; the payload must not reference
	// a supersedes field (that would be a supersede operation, not a promote).
	if decl.Supersedes != "" {
		return nil, fmt.Errorf("%w: payload has supersedes=%q; use supersede operation to replace existing declarations", ErrPromoteDenied, decl.Supersedes)
	}

	// Rule 2: campfire_key operations must be campfire-key-signed.
	if decl.Signing == "campfire_key" && decl.SignerType != convention.SignerCampfireKey {
		return nil, fmt.Errorf("%w: campfire_key operation declaration requires campfire key signature (signer_type=%s)", ErrPromoteDenied, decl.SignerType)
	}

	return &PromoteResult{
		Declaration: decl,
		Warnings:    result.Warnings,
	}, nil
}

// ValidateSupersede validates that a new declaration can supersede an existing one.
//
// Supersede validation rules (convention-extension convention §10.2, §17):
//  1. The new payload must parse as a valid convention:operation declaration.
//  2. The supersedes field must be non-empty and must match the provided existingMsgID.
//  3. Both declarations must be for the same convention slug and operation name.
//  4. The new declaration's version must be strictly greater than the existing one.
//     (Version comparison is lexicographic; use semver-like dotted strings for correctness.)
//  5. For campfire_key operations, the new declaration must be campfire-key-signed.
//
// existingDecl is the parsed form of the declaration being superseded.
// existingMsgID is the campfire message ID of the existing declaration.
func ValidateSupersede(payload []byte, msgTags []string, signerKey, campfireKey string, existingDecl *convention.Declaration, existingMsgID string) (*SupersedeResult, error) {
	if existingDecl == nil {
		return nil, fmt.Errorf("%w: existingDecl must not be nil", ErrSupersedeDenied)
	}
	if existingMsgID == "" {
		return nil, fmt.Errorf("%w: existingMsgID must not be empty", ErrSupersedeDenied)
	}

	newDecl, result, err := convention.Parse(msgTags, payload, signerKey, campfireKey, convention.DefaultDeniedTagPrefixes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse failed: %v", ErrSupersedeDenied, err)
	}
	if !result.Valid {
		return nil, fmt.Errorf("%w: conformance check failed: %v", ErrSupersedeDenied, result.Warnings)
	}

	// Rule 2: the supersedes field must be present and match the existing message ID.
	if newDecl.Supersedes == "" {
		return nil, fmt.Errorf("%w: new declaration has no supersedes field; set supersedes=%q", ErrSupersedeDenied, existingMsgID)
	}
	if newDecl.Supersedes != existingMsgID {
		return nil, fmt.Errorf("%w: supersedes=%q does not match existing message ID %q", ErrSupersedeDenied, newDecl.Supersedes, existingMsgID)
	}

	// Rule 3: same convention and operation.
	if newDecl.Convention != existingDecl.Convention {
		return nil, fmt.Errorf("%w: convention mismatch: new=%q existing=%q", ErrSupersedeDenied, newDecl.Convention, existingDecl.Convention)
	}
	if newDecl.Operation != existingDecl.Operation {
		return nil, fmt.Errorf("%w: operation mismatch: new=%q existing=%q", ErrSupersedeDenied, newDecl.Operation, existingDecl.Operation)
	}

	// Rule 4: version must be strictly greater.
	if !isVersionGreater(newDecl.Version, existingDecl.Version) {
		return nil, fmt.Errorf("%w: new version %q is not greater than existing version %q", ErrSupersedeDenied, newDecl.Version, existingDecl.Version)
	}

	// Rule 5: campfire_key operations must be campfire-key-signed.
	if newDecl.Signing == "campfire_key" && newDecl.SignerType != convention.SignerCampfireKey {
		return nil, fmt.Errorf("%w: campfire_key operation declaration requires campfire key signature", ErrSupersedeDenied)
	}

	return &SupersedeResult{
		NewDeclaration: newDecl,
		SupersededID:   existingMsgID,
		Warnings:       result.Warnings,
	}, nil
}

// isVersionGreater returns true if version a is strictly greater than version b.
// Both versions must be non-empty. Comparison is lexicographic on dotted segments.
// "0.2" > "0.1", "1.0" > "0.9", "0.10" > "0.9" (numeric segment comparison).
func isVersionGreater(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	// Compare segment by segment as integers; fall back to string if not numeric.
	segsA := splitVersion(a)
	segsB := splitVersion(b)
	maxLen := len(segsA)
	if len(segsB) > maxLen {
		maxLen = len(segsB)
	}
	for i := 0; i < maxLen; i++ {
		var va, vb int
		if i < len(segsA) {
			va = segsA[i]
		}
		if i < len(segsB) {
			vb = segsB[i]
		}
		if va > vb {
			return true
		}
		if va < vb {
			return false
		}
	}
	// All segments equal — not strictly greater.
	return false
}

// splitVersion splits a dotted version string into integer segments.
// Non-numeric segments are treated as 0.
func splitVersion(v string) []int {
	var segs []int
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == '.' {
			seg := v[start:i]
			n := 0
			for _, c := range seg {
				if c >= '0' && c <= '9' {
					n = n*10 + int(c-'0')
				} else {
					n = 0
					break
				}
			}
			segs = append(segs, n)
			start = i + 1
		}
	}
	return segs
}
