# Reserved-Op Floor — L1 → L2 → L3 Canonical Placement

**Status:** Frozen at `cf-protocol` 1.0 / `cf-authority` 1.0 (0.30 release).  
**Closes:** OPEN-003.  
**Design references:** 0.30-design.md §2.4, §4.1, §4.2; protocol-spec.md §Reserved-Op Floor LIST;
cf-authority-spec.md §4.5; OPEN-ITEMS.md OPEN-003.

---

## Overview

The **reserved-op floor** is a protocol-level minimum: a set of ten operations that no
convention declaration and no parent grant can make less restrictive. The floor is enforced
by a three-layer chain. Each layer plays a distinct role; the chain is one-directional and
may not be inverted or bypassed by any party at L2 or L3.

```
L1 — cf-protocol        LIST lives here
     (authoritative)    cf-protocol/internal/reserved-ops.go

        │  consulted by
        ▼

L2 — cf-conventions     ENFORCER lives here
     (middleware)        dispatch interceptor checks every op against the L1 list
                         before invoking L3; reserved ops without a grant chain
                         are rejected immediately with DENY(reserved-op-floor)

        │  invokes (for ops NOT on the L1 list, or ops WITH a valid grant chain)
        ▼

L3 — cf-authority       EVALUATOR lives here
     (policy)            GateEvaluator applies grant-chain policy: expiry, revocation,
                         scope intersection, depth limit, predicate evaluation
                         Returns: Allow | Deny(reason) | Unresolvable
```

The clamping order is fixed and one-directional:

```
owner ceiling  >  parent grant  >  convention declaration
```

No convention author and no party at L3 (cf-authority) may lower the reserved-op floor
below the L1 list. Attempts to do so are rejected at L2 before the L3 evaluator is
invoked. This is the load-bearing safety property: convention authors are not in the
trusted computing base for reserved operations.

---

## Layer 1 — The LIST (cf-protocol)

**File:** `cf-protocol/internal/reserved-ops.go` (created in Stage 1 of the 0.30
implementation sequence; this chain doc is the Stage 0 spec artifact).

The authoritative list of ten reserved operations:

```
disband           | evict            | admit
grant             | revoke           | delegation-grant
delegation-revoke | delegation-accept | member-roster
compaction
```

These ten operations form the floor. The list is part of the `cf-protocol` 1.0 freeze.
**Adding ops to the list is a `cf-protocol` MAJOR version bump** — additions to a closed
list are breaking when consumers rely on the list being complete (see 0.30-design.md §4.5).

### What the LIST is not

The `"future"` and `"fulfills"` tags are **reserved tags** — not reserved-op floor entries.
They are member-signed (the sender's key covers them) and live in the reserved-tag table
alongside the `campfire:*` namespace (protocol-spec.md §Ratified L1 Reserved Tags). They
are Layer-1 primitives of the antecedent/fulfillment substrate, not operations that require
a grant chain. This distinction matters: the floor controls authority elevation; the
reserved-tag table controls namespace collision.

---

## Layer 2 — The ENFORCER (cf-conventions)

**Location:** dispatch interceptor in the `cf-conventions` middleware layer.

When a caller dispatches a convention operation, the L2 interceptor executes before
invoking the L3 evaluator:

1. **Look up the op name in the L1 LIST** (reads from `cf-protocol/internal/reserved-ops.go`).
2. **If the op is NOT on the LIST:** proceed to L3 evaluation as normal.
3. **If the op IS on the LIST:**
   a. Require the caller to present a grant chain whose root reaches an owner-level
      principal in the target campfire.
   b. Require the chain depth ≤ 1 (reserved ops are capped at depth 1; see
      0.30-design.md §2.3 "Hard depth limit: 2" — reserved ops are the stricter case
      within that limit).
   c. If no valid chain is present: return `DENY(reserved_op_floor)` immediately,
      **without invoking the L3 evaluator**.
   d. If a valid chain IS present: invoke L3 to apply full policy (expiry, revocation,
      scope intersection, predicate evaluation).

**Why the enforcer lives at L2, not L3.**  
The L3 evaluator is a pluggable interface (`GateEvaluator`). Third-party implementations
of `GateEvaluator` MUST pass the conformance harness, but the harness cannot guarantee
that every possible implementation enforces the floor. Placing the floor check at L2 —
in the middleware layer that wraps all evaluators — means the floor is enforced regardless
of which `GateEvaluator` implementation is in use. No convention author can write a
declaration that routes around L2.

---

## Layer 3 — The EVALUATOR (cf-authority)

**Location:** `cf-conventions/cf-authority/trust/` (see cf-authority-spec.md §3–§5).

The `GateEvaluator` interface applies grant-chain policy for operations that pass the L2
floor check:

```go
type GateEvaluator interface {
    Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResult
}
```

For reserved ops that arrive at L3 (because the caller presented a grant chain that
passed the L2 floor check), the evaluator performs the full chain walk:

- **Expiry check:** each grant's `until` field against current time.
- **Revocation check:** revocation view freshness and membership.
- **Depth limit:** grant chain depth ≤ 2 (hard limit); reserved ops ≤ 1 at L2.
- **Scope intersection:** each hop narrows scope; scope widening → `DENY(scope_widening)`.
- **Predicate evaluation:** convention's gate predicate tree (max nesting depth 3).
- **Owner ceiling:** `MinLevelOverride` in `OwnerPolicy` — owner may require a higher
  provenance level floor than the convention declared.

### DenyReason for reserved ops

When a reserved op is denied at L2 before reaching L3:
- `DenyReason = "reserved_op_floor"` (stable string code, wire-frozen at cf-authority 1.0)

When a reserved op reaches L3 and is denied there:
- Any of the other stable deny codes: `expired`, `revoked`, `depth_exceeded`,
  `scope_mismatch`, `scope_widening`, `stale_revocation`, `owner_ceiling`,
  `predicate_unsatisfied`, `store_read_error`.

### What L3 cannot do

**No implementation of `GateEvaluator` may lower the reserved-op floor below the L1
list.** The floor is not an L3 policy parameter; it is a wire-protocol constant. A
convention that declares `level: 0` on `delegation-grant` is rejected at L2 before L3
is invoked. A `GateEvaluator` that returns `Allow` for a caller presenting no grant chain
on a reserved op violates the conformance harness (case 11: `reserved-op-floor`). This is
tested deterministically — three runs with identical inputs must produce byte-equal
`Deny / reserved_op_floor` results.

The conformance harness case 11 (`reserved-op-floor`) is the machine-checkable expression
of this invariant: a convention declaring `level: 0` on `delegation-grant` must produce
`Deny / reserved_op_floor`, never `Allow`. Any `GateEvaluator` implementation that
produces a different result is non-conforming and may not back a cf-authority dispatcher.

---

## Enforcement Flow — End-to-End

A caller dispatches `delegation-grant` (a reserved op) with no grant chain:

```
1. Caller → cf-conventions dispatcher
2. Dispatcher → L2 interceptor
3. L2: "delegation-grant" IN reserved-ops.go LIST? YES
4. L2: Caller has owner-level grant chain? NO
5. L2: return DENY(reserved_op_floor) ← dispatcher returns this to caller
   (L3 evaluator is never invoked)
```

A caller dispatches `delegation-grant` with a valid owner-level depth-1 grant chain:

```
1. Caller → cf-conventions dispatcher
2. Dispatcher → L2 interceptor
3. L2: "delegation-grant" IN reserved-ops.go LIST? YES
4. L2: Caller has owner-level grant chain at depth ≤ 1? YES
5. L2 → L3 (GateEvaluator.Evaluate)
6. L3: expiry OK, revocation OK, scope matches, predicate satisfied
7. L3: return Allow ← dispatcher proceeds with the operation
```

A caller dispatches a non-reserved op (e.g., `ready:claim`):

```
1. Caller → cf-conventions dispatcher
2. Dispatcher → L2 interceptor
3. L2: "claim" IN reserved-ops.go LIST? NO
4. L2 → L3 (GateEvaluator.Evaluate) directly
5. L3: evaluates convention's gate predicate
6. L3: return Allow or Deny(reason)
```

---

## Versioning and Module Placement

| Layer | Go module | Package | Status |
|-------|-----------|---------|--------|
| L1 LIST | `cf-protocol` | `cf-protocol/internal/reserved-ops.go` | Stage 1 |
| L2 ENFORCER | `cf-conventions` | dispatch interceptor (Stage 2) | Stage 2 |
| L3 EVALUATOR | `cf-conventions` | `cf-authority/trust/evaluator.go` | Stage 3 |

The L1 list is internal to `cf-protocol` — external consumers read it via an exported
accessor function, not by importing the internal package. The L2 enforcer depends on the
L1 accessor. The L3 evaluator is invoked by the L2 enforcer after the floor check passes.

`cf-protocol` and `cf-conventions` version independently. A minor bump in either module
never requires a coordinated bump in the other. Additions to the L1 LIST require a
**major** bump of `cf-protocol` (per 0.30-design.md §4.5).

---

## Relationship to Other Specs

- **protocol-spec.md §Reserved-Op Floor LIST** — the canonical LIST and one-sentence
  architecture summary. This document is the extended chain explanation cited from that
  addendum.
- **cf-authority-spec.md §4.5** — the L3 evaluator's view of the reserved-op floor;
  `DenyReason` codes; conformance harness case 11.
- **0.30-design.md §2.4** — gate predicate language; clamping order.
- **0.30-design.md §4.1** — L1 freeze contents (reserved-op LIST included).
- **0.30-design.md §4.2** — L3 freeze contents (GateEvaluator interface, DenyReason).
- **OPEN-ITEMS.md OPEN-003** — the open item this artifact closes.
