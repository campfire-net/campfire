package discovery_test

// ratelimit_test.go — Tests for rate-limit declaration vocabulary (OPEN-013).
// Per design v2 §3.3.1: a level:0 op without a bounds block is a parser error.

import (
	"testing"

	discovery "github.com/campfire-net/campfire/cf-conventions/cf-discovery"
)

func TestValidateRateLimitBounds_NoBounds(t *testing.T) {
	err := discovery.ValidateRateLimitBounds(discovery.RateLimitBounds{})
	if err == nil {
		t.Error("empty RateLimitBounds should be invalid (no axes declared)")
	}
}

func TestValidateRateLimitBounds_ValidGlobal(t *testing.T) {
	b := discovery.RateLimitBounds{Global: "5000/min"}
	if err := discovery.ValidateRateLimitBounds(b); err != nil {
		t.Errorf("valid global rate limit rejected: %v", err)
	}
}

func TestValidateRateLimitBounds_ValidAllAxes(t *testing.T) {
	b := discovery.RateLimitBounds{
		PerKeypair:  "10/min",
		PerIP:       "120/min",
		PerResolved: "30/min",
		Global:      "5000/min",
		CostUnit:    "1",
	}
	if err := discovery.ValidateRateLimitBounds(b); err != nil {
		t.Errorf("valid multi-axis bounds rejected: %v", err)
	}
}

func TestValidateRateLimitBounds_InvalidRateSpec(t *testing.T) {
	cases := []struct {
		name  string
		bounds discovery.RateLimitBounds
	}{
		{"bad per_keypair", discovery.RateLimitBounds{PerKeypair: "bad"}},
		{"bad per_ip", discovery.RateLimitBounds{PerIP: "120/day"}},
		{"bad global", discovery.RateLimitBounds{Global: "5000"}},
	}
	for _, tc := range cases {
		if err := discovery.ValidateRateLimitBounds(tc.bounds); err == nil {
			t.Errorf("%s: should be invalid rate spec, but passed", tc.name)
		}
	}
}

func TestValidateRateLimitBounds_RateUnits(t *testing.T) {
	// All three units (min, s, h) should be accepted
	for _, spec := range []string{"100/min", "10/s", "1000/h"} {
		b := discovery.RateLimitBounds{Global: spec}
		if err := discovery.ValidateRateLimitBounds(b); err != nil {
			t.Errorf("rate spec %q rejected: %v", spec, err)
		}
	}
}

func TestValidateRateLimitBounds_InvalidCostUnit(t *testing.T) {
	b := discovery.RateLimitBounds{Global: "100/min", CostUnit: "bad"}
	if err := discovery.ValidateRateLimitBounds(b); err == nil {
		t.Error("non-integer cost_unit should be rejected")
	}
}

func TestValidateLevel0OpDeclaration_Valid(t *testing.T) {
	d := discovery.Level0OpDeclaration{
		Name: "preview",
		Gate: discovery.Level0Gate{Level: 0},
		Bounds: discovery.RateLimitBounds{
			PerIP:  "120/min",
			Global: "5000/min",
		},
	}
	if err := discovery.ValidateLevel0OpDeclaration(d); err != nil {
		t.Errorf("valid Level0OpDeclaration rejected: %v", err)
	}
}

func TestValidateLevel0OpDeclaration_NonZeroGate(t *testing.T) {
	d := discovery.Level0OpDeclaration{
		Name: "preview",
		Gate: discovery.Level0Gate{Level: 1}, // wrong level
		Bounds: discovery.RateLimitBounds{
			Global: "5000/min",
		},
	}
	if err := discovery.ValidateLevel0OpDeclaration(d); err == nil {
		t.Error("level:1 gate should be rejected for Level0OpDeclaration")
	}
}

func TestValidateLevel0OpDeclaration_MissingBounds(t *testing.T) {
	d := discovery.Level0OpDeclaration{
		Name:   "preview",
		Gate:   discovery.Level0Gate{Level: 0},
		Bounds: discovery.RateLimitBounds{}, // no axes — fail loud
	}
	if err := discovery.ValidateLevel0OpDeclaration(d); err == nil {
		t.Error("level:0 op without bounds should fail validation (fail-loud design)")
	}
}

func TestParseRateSpec(t *testing.T) {
	cases := []struct {
		spec  string
		count int
		unit  string
		valid bool
	}{
		{"100/min", 100, "min", true},
		{"50/s", 50, "s", true},
		{"1000/h", 1000, "h", true},
		{"bad", 0, "", false},
		{"100/day", 0, "", false},
		{"", 0, "", false},
	}
	for _, tc := range cases {
		count, unit, err := discovery.ParseRateSpec(tc.spec)
		if tc.valid {
			if err != nil {
				t.Errorf("ParseRateSpec(%q) error: %v", tc.spec, err)
				continue
			}
			if count != tc.count || unit != tc.unit {
				t.Errorf("ParseRateSpec(%q) = (%d, %q), want (%d, %q)", tc.spec, count, unit, tc.count, tc.unit)
			}
		} else {
			if err == nil {
				t.Errorf("ParseRateSpec(%q) should return error for invalid spec", tc.spec)
			}
		}
	}
}
