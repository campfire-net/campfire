package protocol

import (
	reservedops "github.com/campfire-net/campfire/cf-protocol/internal/reserved-ops"
)

// ── Reserved tags (wire-level primitives, frozen at cf-protocol 1.0) ─────────

// TagFuture is the reserved tag that marks a message as a future (an
// unfulfilled request awaiting a response). Any sender may attach this tag.
// When a future is posted, Await waits for a message carrying TagFulfills
// with the future's ID in its antecedents.
//
// Wire value: "future" — frozen at cf-protocol 1.0.
const TagFuture = "future"

// TagFulfills is the reserved tag that marks a message as a fulfillment.
// A fulfilling message must list the target future's ID in its antecedents.
// Await returns the first message that satisfies both conditions
// (TagFulfills + target ID in antecedents); ties are broken by
// (earliest timestamp, lexicographically smaller ID).
//
// Wire value: "fulfills" — frozen at cf-protocol 1.0.
const TagFulfills = "fulfills"

// ── Reserved-op floor (L1 freeze, campfireagent-935) ──────────────────────────

// ReservedOps is the authoritative list of the ten reserved operations frozen
// at cf-protocol 1.0. No convention declaration and no parent grant can lower
// the gate on any of these ops; they require owner-level authority.
//
// This is the public re-export of cf-protocol/internal/reserved-ops.ReservedOps.
// L2 enforcement (pkg/convention dispatcher) MUST reference this list so that
// the single source of truth stays at L1.
//
// Wire value: sorted for stable iteration. Additions are MAJOR bumps
// (the F6 Commitment — see cf-protocol/COMPATIBILITY.md).
var ReservedOps = reservedops.ReservedOps

// IsReservedOp reports whether op is one of the ten reserved operations.
// L2 enforcement code (convention dispatcher) calls this before registering or
// dispatching any convention operation. Returns true for all ops in ReservedOps.
//
// This is the public re-export of cf-protocol/internal/reserved-ops.IsReserved.
var IsReservedOp = reservedops.IsReserved
