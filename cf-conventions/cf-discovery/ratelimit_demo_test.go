package discovery_test

import (
	"fmt"
	"sync"
	"testing"

	discovery "github.com/campfire-net/campfire/cf-conventions/cf-discovery"
)

// FixedWindowCounter is a minimal fixed-window counter for demo purposes.
type FixedWindowCounter struct {
	mu      sync.Mutex
	allowed int
	used    int
}

func (c *FixedWindowCounter) Allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used < c.allowed {
		c.used++
		return true
	}
	return false
}

// TestDemoRateLimit_Level0OpDeclaration_Valid validates a well-formed Level0OpDeclaration.
func TestDemoRateLimit_Level0OpDeclaration_Valid(t *testing.T) {
	decl := discovery.Level0OpDeclaration{
		Name: "preview",
		Gate: discovery.Level0Gate{Level: 0},
		Bounds: discovery.RateLimitBounds{
			PerKeypair: "3/min",
			PerIP:      "30/min",
			Global:     "5000/min",
		},
	}
	if err := discovery.ValidateLevel0OpDeclaration(decl); err != nil {
		t.Errorf("well-formed Level0OpDeclaration rejected: %v", err)
	}
}

// TestDemoRateLimit_FailLoud_MissingBounds verifies fail-loud per design v2 §3.3.1.
func TestDemoRateLimit_FailLoud_MissingBounds(t *testing.T) {
	decl := discovery.Level0OpDeclaration{
		Name:   "preview",
		Gate:   discovery.Level0Gate{Level: 0},
		Bounds: discovery.RateLimitBounds{},
	}
	if err := discovery.ValidateLevel0OpDeclaration(decl); err == nil {
		t.Error("level:0 op without bounds should be rejected (fail-loud per §3.3.1)")
	}
}

// TestDemoRateLimit_ParseRateSpec_UnitMatrix verifies all three units are accepted.
func TestDemoRateLimit_ParseRateSpec_UnitMatrix(t *testing.T) {
	cases := []struct {
		spec  string
		count int
		unit  string
		valid bool
	}{
		{"3/min", 3, "min", true},
		{"10/s", 10, "s", true},
		{"100/h", 100, "h", true},
		{"5000/min", 5000, "min", true},
		{"bad", 0, "", false},
		{"10/day", 0, "", false},
		{"", 0, "", false},
		{"/min", 0, "", false},
	}
	for _, tc := range cases {
		count, unit, err := discovery.ParseRateSpec(tc.spec)
		if tc.valid {
			if err != nil {
				t.Errorf("ParseRateSpec(%q) unexpected error: %v", tc.spec, err)
				continue
			}
			if count != tc.count || unit != tc.unit {
				t.Errorf("ParseRateSpec(%q) = (%d,%q), want (%d,%q)", tc.spec, count, unit, tc.count, tc.unit)
			}
		} else {
			if err == nil {
				t.Errorf("ParseRateSpec(%q) should fail, got (%d,%q)", tc.spec, count, unit)
			}
		}
	}
}

// TestDemoRateLimit_NAllowed_NPlus1Rejected demonstrates the N-allowed/N+1-rejected contract.
// Declares a level:0 op with per_keypair=3/min, parses N=3, issues 3 allowed requests,
// then asserts the 4th (N+1) is rejected by the operator enforcer.
func TestDemoRateLimit_NAllowed_NPlus1Rejected(t *testing.T) {
	// Step 1: declare and validate.
	decl := discovery.Level0OpDeclaration{
		Name: "preview",
		Gate: discovery.Level0Gate{Level: 0},
		Bounds: discovery.RateLimitBounds{
			PerKeypair: "3/min",
			Global:     "5000/min",
		},
	}
	if err := discovery.ValidateLevel0OpDeclaration(decl); err != nil {
		t.Fatalf("declaration invalid: %v", err)
	}

	// Step 2: parse ceiling N from the declared bound.
	n, unit, err := discovery.ParseRateSpec(decl.Bounds.PerKeypair)
	if err != nil {
		t.Fatalf("ParseRateSpec: %v", err)
	}
	t.Logf("declared ceiling: %d requests per %s (N=%d)", n, unit, n)

	// Step 3: operator enforcer seeded from N.
	bucket := &FixedWindowCounter{allowed: n}

	// Step 4: N requests must be allowed.
	for i := 1; i <= n; i++ {
		if !bucket.Allow() {
			t.Errorf("request %d/%d rejected — should be allowed within declared ceiling", i, n)
		} else {
			t.Logf("request %d/%d: ALLOWED", i, n)
		}
	}

	// Step 5: N+1th request must be rejected (rate-limited).
	if bucket.Allow() {
		t.Errorf("request %d (N+1) was ALLOWED — should be rate-limited (ceiling=%d)", n+1, n)
	} else {
		t.Logf("request %d (N+1): RATE-LIMITED — ceiling contract satisfied", n+1)
	}
}

// TestDemoRateLimit_MultiAxis_GlobalGoverns verifies that a tighter global ceiling
// blocks requests even when per_keypair still has headroom.
func TestDemoRateLimit_MultiAxis_GlobalGoverns(t *testing.T) {
	decl := discovery.Level0OpDeclaration{
		Name: "preview",
		Gate: discovery.Level0Gate{Level: 0},
		Bounds: discovery.RateLimitBounds{
			PerKeypair: "10/min",
			Global:     "2/min",
		},
	}
	if err := discovery.ValidateLevel0OpDeclaration(decl); err != nil {
		t.Fatalf("declaration invalid: %v", err)
	}

	pkN, _, err := discovery.ParseRateSpec(decl.Bounds.PerKeypair)
	if err != nil {
		t.Fatalf("parse per_keypair: %v", err)
	}
	gN, _, err := discovery.ParseRateSpec(decl.Bounds.Global)
	if err != nil {
		t.Fatalf("parse global: %v", err)
	}
	fmt.Printf("  per_keypair ceiling=%d, global ceiling=%d\n", pkN, gN)

	perKey := &FixedWindowCounter{allowed: pkN}
	global := &FixedWindowCounter{allowed: gN}

	// A request is allowed only when ALL active axes have headroom.
	allow := func() bool { return perKey.Allow() && global.Allow() }

	// Requests 1..gN: allowed.
	for i := 1; i <= gN; i++ {
		if !allow() {
			t.Errorf("request %d should be allowed (global has headroom)", i)
		} else {
			t.Logf("request %d: ALLOWED", i)
		}
	}

	// Request gN+1: global ceiling hit — rejected even though per_keypair has headroom.
	if allow() {
		t.Errorf("request %d should be rate-limited (global ceiling=%d reached)", gN+1, gN)
	} else {
		t.Logf("request %d: RATE-LIMITED (global ceiling reached; per_keypair headroom irrelevant)", gN+1)
	}
}
