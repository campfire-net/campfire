// Package reservedops defines the reserved-op LIST for the cf-protocol substrate
// (campfireagent-753: Stage 1, design v2 §2.4 and OPEN-003).
//
// The ten reserved ops carry a protocol-level minimum gate that no convention
// declaration and no parent grant can lower. Convention authors are NOT in the
// trusted computing base — these ops are frozen at L1.
//
// Clamping order: owner ceiling > parent grant > convention declaration.
// The enforcer lives at L2 (the dispatch interceptor).
// The evaluator lives at L3 (cf-authority).
//
// Reserved-op LIST additions are MAJOR bumps, not minor — additions to a
// closed list ARE breaking when consumers rely on the list being complete.
// (cf-protocol COMPATIBILITY.md §The F6 Commitment)
package reservedops

// ReservedOps is the authoritative list of reserved operations.
// No convention declaration may lower the gate on any of these ops.
// List is sorted for stable iteration and binary search.
var ReservedOps = []string{
	"admit",
	"compaction",
	"delegation-accept",
	"delegation-grant",
	"delegation-revoke",
	"disband",
	"evict",
	"grant",
	"member-roster",
	"revoke",
}

// IsReserved reports whether op is a reserved operation.
// O(n) linear scan — the list is intentionally small and stable.
func IsReserved(op string) bool {
	for _, r := range ReservedOps {
		if r == op {
			return true
		}
	}
	return false
}
