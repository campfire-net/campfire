package discovery

// ratelimit.go — Rate-limit declarations for level:0 convention ops.
//
// Conventions that expose level:0 ops (accessible without membership, with only a valid
// keypair) MUST declare a bounds block per design v2 §3.3.1. This file defines the
// declaration vocabulary for those bounds.
//
// A level:0 op without a declared bounds block is a parser error — the convention
// does not load. This is "fail loud by design" per the spec.
//
// Spec citations: design v2 §3.3.1, OPEN-013.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RateLimitBounds is the rate-limit declaration for a level:0 convention op.
// At least one of PerKeypair, PerIP, PerResolved, or Global MUST be set.
// Missing axes are not enforced at that anchor.
//
// Enforcement layering (one-directional):
//   L4 deployment ceiling → convention declaration (L3) → operator override (L4 ops config).
// A deployment operator MAY tighten any axis below the declaration; never loosen above.
type RateLimitBounds struct {
	// PerKeypair is the rate limit per Ed25519 keypair identity.
	// Weakest anchor — bypassable by key rotation. Format: "N/unit" e.g. "10/min".
	PerKeypair string `yaml:"per_keypair" json:"per_keypair,omitempty"`

	// PerIP is the rate limit per source IP address.
	// Primary flood defense — forces botnet acquisition. Format: "N/unit" e.g. "120/min".
	PerIP string `yaml:"per_ip" json:"per_ip,omitempty"`

	// PerResolved is the rate limit per upstream-resolved identity.
	// Enforceable only when the deployment runs a hosted reader (Tier 3.5).
	// On raw self-host it degrades silently to per_ip after a startup warning.
	PerResolved string `yaml:"per_resolved" json:"per_resolved,omitempty"`

	// Global is the convention-wide damage cap.
	// Format: "N/unit" e.g. "5000/min".
	Global string `yaml:"global" json:"global,omitempty"`

	// CostUnit is the number of rate-units consumed per invocation (default: "1").
	CostUnit string `yaml:"cost_unit" json:"cost_unit,omitempty"`
}

// rateSpecRe matches "N/unit" where unit is min, s (second), h (hour).
var rateSpecRe = regexp.MustCompile(`^(\d+)/(min|s|h)$`)

// ValidateRateLimitBounds validates a RateLimitBounds declaration.
// Returns an error if the bounds are missing all axes or if any axis spec is malformed.
// A bounds with zero set axes is rejected ("fail loud" — design v2 §3.3.1).
func ValidateRateLimitBounds(b RateLimitBounds) error {
	setAxes := 0
	for _, axis := range []struct {
		name string
		val  string
	}{
		{"per_keypair", b.PerKeypair},
		{"per_ip", b.PerIP},
		{"per_resolved", b.PerResolved},
		{"global", b.Global},
	} {
		if axis.val == "" {
			continue
		}
		setAxes++
		if !rateSpecRe.MatchString(axis.val) {
			return fmt.Errorf("rate_limit_bounds: %s %q is not a valid rate spec (want N/min, N/s, or N/h)", axis.name, axis.val)
		}
	}
	if setAxes == 0 {
		return fmt.Errorf("rate_limit_bounds: a level:0 op must declare at least one rate axis (per_keypair, per_ip, per_resolved, or global)")
	}
	if b.CostUnit != "" {
		n, err := strconv.Atoi(strings.TrimSpace(b.CostUnit))
		if err != nil || n < 1 {
			return fmt.Errorf("rate_limit_bounds: cost_unit %q must be a positive integer", b.CostUnit)
		}
	}
	return nil
}

// ParseRateSpec parses a rate spec string "N/unit" into count and window string.
// Returns count and unit ("min", "s", or "h"). Returns an error for malformed specs.
func ParseRateSpec(spec string) (count int, unit string, err error) {
	m := rateSpecRe.FindStringSubmatch(spec)
	if m == nil {
		return 0, "", fmt.Errorf("invalid rate spec %q: want N/min, N/s, or N/h", spec)
	}
	count, _ = strconv.Atoi(m[1])
	unit = m[2]
	return count, unit, nil
}

// Level0OpDeclaration is the declaration block for a single level:0 convention op.
// A convention op with Gate.Level == 0 MUST have a non-nil Bounds.
// A convention that declares a level:0 op without Bounds fails validation and
// cannot be loaded by the dispatcher (design v2 §3.3.1 "fail loud").
type Level0OpDeclaration struct {
	// Name is the operation name within its convention.
	Name string `yaml:"name" json:"name"`
	// Gate specifies the minimum provenance level (must be 0 for this type).
	Gate Level0Gate `yaml:"gate" json:"gate"`
	// Bounds declares the rate-limit ceilings for this op. Required.
	Bounds RateLimitBounds `yaml:"bounds" json:"bounds"`
}

// Level0Gate is the gate declaration for a level:0 op.
type Level0Gate struct {
	Level int `yaml:"level" json:"level"`
}

// ValidateLevel0OpDeclaration validates a Level0OpDeclaration.
// Returns an error if the op gate is not level:0 or if bounds are invalid.
func ValidateLevel0OpDeclaration(d Level0OpDeclaration) error {
	if d.Name == "" {
		return fmt.Errorf("level0_op: name is required")
	}
	if d.Gate.Level != 0 {
		return fmt.Errorf("level0_op %q: gate.level must be 0", d.Name)
	}
	return ValidateRateLimitBounds(d.Bounds)
}
