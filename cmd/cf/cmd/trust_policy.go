package cmd

import (
	"context"
	"fmt"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/trust"
)

// compareJoinedCampfire reads declarations from a newly joined campfire and
// compares them against the local policy engine. Returns the compatibility
// report. Non-fatal: returns a no-declarations report on error.
func compareJoinedCampfire(s store.Store, campfireID string) *trust.CampfireCompatibilityReport {
	engine := loadLocalPolicyEngine(s)

	decls, err := listConventionOperations(context.Background(), s, campfireID)
	if err != nil || len(decls) == 0 {
		// No declarations — return empty report.
		empty := make([]*convention.Declaration, 0)
		return engine.CompareCampfireDeclarations(empty)
	}

	return engine.CompareCampfireDeclarations(decls)
}

// printCompatibilityReport prints the per-convention compatibility report to stdout.
// Called after a successful cf join. Silenced for no-declaration campfires.
//
// Output format:
//   trust: adopted  (2 conventions match)
//   trust: unknown  (no convention declarations)
//   trust: divergent
//     [divergent] social:post  local=sha256:abc123  incoming=sha256:def456
//     [adopted]   trust:verify
func printCompatibilityReport(r *trust.CampfireCompatibilityReport) {
	if len(r.Conventions) == 0 {
		// Silent: no convention declarations means trust_status=unknown but it's
		// not an error — many campfires don't publish declarations.
		return
	}

	statusLabel := statusIcon(r.OverallStatus)
	n := len(r.Conventions)
	switch r.OverallStatus {
	case trust.TrustAdopted:
		fmt.Printf("trust: %s  (%d convention%s match)\n", statusLabel, n, plural(n))
	case trust.TrustCompatible:
		fmt.Printf("trust: %s  (%d convention%s compatible)\n", statusLabel, n, plural(n))
	case trust.TrustDivergent:
		fmt.Printf("trust: %s  — fingerprint mismatch detected\n", statusLabel)
	default:
		fmt.Printf("trust: %s\n", statusLabel)
	}

	// Only show per-convention detail when there's something interesting to report.
	showDetail := r.OverallStatus == trust.TrustDivergent || r.OverallStatus == trust.TrustUnknown
	if !showDetail {
		return
	}

	for _, c := range r.Conventions {
		icon := statusIcon(c.Status)
		switch c.Status {
		case trust.TrustDivergent:
			fmt.Printf("  [%s] %s:%s\n", icon, c.Convention, c.Operation)
			fmt.Printf("       local    = %s\n", c.LocalFingerprint)
			fmt.Printf("       incoming = %s\n", c.RemoteFingerprint)
		default:
			fmt.Printf("  [%s] %s:%s\n", icon, c.Convention, c.Operation)
		}
	}
}

// statusIcon returns a short label for a trust status.
func statusIcon(s trust.TrustStatus) string {
	switch s {
	case trust.TrustAdopted:
		return "adopted"
	case trust.TrustCompatible:
		return "compatible"
	case trust.TrustDivergent:
		return "divergent"
	default:
		return "unknown"
	}
}

// plural returns "s" when n != 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
