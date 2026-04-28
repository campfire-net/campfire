# cf-authority 1.0 — Wire Schema Specification

**Status:** Frozen at cf-authority 1.0 (0.30 release).  
**Layer:** L3 — RFC convention (`cf-conventions/cf-authority/`).  
**Design references:** 0.30-design.md §2, §2.2–§2.5.1, §7; gate-evaluator-conformance-harness.md.

---

## Overview

`cf-authority` is the scoped-grant, bounded-transitivity, chain-evaluating authorization
system for the campfire protocol. It externalizes identity-and-delegation policy into a
layer-3 convention built on top of the signed-message substrate.

This document specifies:

1. **Grant CBOR layout** — the wire format for scoped capability grants (§1)
2. **Predicate AST schema** — the gate-predicate language (§2)
3. **`EvaluateRequest` / `EvaluateResult` Go types** — the evaluator interface (§3)
4. **`DenyReason` enum** — stable string codes (§3.4)
5. **Conformance contract** — determinism and canonicalization rules (§4)

All CBOR in this spec is deterministic per RFC 8949 §4.2.1 (definite-length, sorted map
keys, smallest-int encoding, no NaN/Inf floats). Implementations that re-encode to a
different canonical form lose signature validity.

---

## §0 — Delegation Extent Invariant

**Invariant:** An owner may delegate any action scope they hold. No one may delegate
identity. Reserved ops cap at depth 1.

This invariant establishes the outer boundary of what the delegation system can express.
Three sub-claims compose it, each independently enforced:

**Sub-claim (a) — scope delegation:** An owner holds a set of action scopes rooted in
their ownership relation to a campfire. They may delegate any action scope they hold
downward in the chain. The system places no floor on what an owner may share — only a
ceiling on what a delegate may claim (see §1.5 for scope intersection rules; see D6
clamping order in `0.30-deal-breakers.md`).

**Sub-claim (b) — identity is not delegable:** Identity — the binding between an Ed25519
key and the authorization flows anchored to it — is not a scope and cannot appear in any
grant payload. No one may delegate identity. An agent may receive action scopes, but the
chain root's identity cannot be laundered through a delegate. The `chain_to` predicate
enforces this structurally: it anchors to a 32-byte Ed25519 public key, not to a claimed
identity string; there is no wire-level mechanism for an intermediate to present itself as
the root. Attempts to construct a chain that re-roots authority in a delegated key are
defeated by the chain-walk verification (§4.2, §4.3; D3 in `0.30-deal-breakers.md`).

**Sub-claim (c) — reserved ops cap at depth 1:** The ten reserved operations
(`disband | evict | admit | grant | revoke | delegation-grant | delegation-revoke |
delegation-accept | member-roster | compaction`) carry a protocol-level depth floor of
1 — they may be held by an owner (depth 0) or delegated to a direct agent (depth 1), but
not delegated further. Reserved ops cap at depth 1. This is a protocol-level floor that no
convention declaration and no parent grant can lower (D5 in `0.30-deal-breakers.md`). The
depth cap is enforced by the reserved-op floor check before the convention declaration is
consulted.

**Design references:** OPEN-002; `0.30-design.md` §2; D5 (reserved-op floor,
`0.30-deal-breakers.md` §D5); D6 (clamping order, `0.30-deal-breakers.md` §D6).

---

## §1 — Grant CBOR Layout

### §1.1 Capability tuple

A **grant** carries a set of capabilities. Each capability is a 5-tuple plus a nonce.
Wire format is a CBOR map. Field keys are integers (compact encoding):

```
Capability = {
  1: convention   ; tstr — exact convention name; no wildcards, no cross-convention
  2: op_pattern   ; tstr — within-convention op pattern; exact | alternation ("|") | "*"
  3: where        ; [+ WhereMatcher]  — OR-composition; empty = "wherever agent is a member"
  4: bounds       ; {tstr => BoundValue}  — reserved keys only; see §1.3
  5: until        ; int64 — mandatory expiry, nanoseconds-since-Unix-epoch UTC
  6: nonce        ; bstr — 16 bytes, random; grant-id = SHA-256(canonical-CBOR(Capability))
}
```

Field 5 (`until`) is mandatory. There are no "forever" grants. Grants without `until`
MUST be rejected at parse time.

### §1.2 Grant envelope (wire message payload)

A `delegation:grant` message carries a CBOR-encoded payload:

```
GrantPayload = {
  1: parent_grant_id   ; bstr | null — null for owner-root grants
  2: child_pubkey      ; bstr — Ed25519 public key, 32 bytes raw
  3: capabilities      ; [+ Capability]
  4: depth             ; uint — hop depth of this grant (0 = owner-root)
}
```

The **grant-id** is `SHA-256(deterministic-CBOR(GrantPayload))`. It is the canonical
identifier for revocation (`delegation:revoke` references grant-id).

The grant message is signed by the granting party (the parent); the child's pubkey is
embedded in the payload. Signature verification is the dispatcher's responsibility before
invoking the evaluator.

### §1.3 WhereMatcher

Three matcher kinds compose as OR. An empty `where` list means "wherever the agent is a
member":

```
WhereMatcher =
  CampfireByID   ; {kind: 1, id: bstr}    — exact campfire hex-id
  | NamePrefix   ; {kind: 2, prefix: tstr} — campfire name prefix match
  | TagMatcher   ; {kind: 3, tag: tstr}    — campfire carries this tag
```

### §1.4 BoundValue

The `bounds` map uses reserved string keys only. Unknown keys MUST cause parse failure
(fail closed). The reserved keys are:

| Key      | Value type | Semantics |
|----------|-----------|-----------|
| `rate`   | `{per: tstr, count: uint, window: tstr}` | Op rate cap (e.g. `{per: "keypair", count: 10, window: "1m"}`) |
| `quota`  | `{unit: tstr, max: uint}` | Lifetime op count cap |
| `spend`  | `{unit: tstr, max: uint}` | Monetary/scrip spend cap |
| `ttl`    | `uint` | Seconds; lifetime bound on any derived grant |

Convention authors declare which bound keys they honor. Unknown keys FAIL CLOSED; a
grant carrying an unrecognized bound key is rejected.

### §1.5 Depth limit and transitivity rules

**Hard depth limit: 2.** The deployment shape is `human (depth 0) → agent (depth 1) →
ephemeral worker (depth 2)`. Reserved ops are capped at depth 1. Deeper chains require
explicit owner widening through the approval flow.

Sub-grant scope is set-intersection on each axis of `parent ⊓ proposed`:

- `convention` MUST equal parent's; no convention-jumping.
- `op_pattern` MUST be a within-convention subset of parent's pattern.
- `where` union MUST be a containment-subset of parent's union.
- `bounds` are `MIN(parent, child)` per axis; child MUST declare a bound on every axis
  the parent constrained.
- `until ≤ parent.until`.

A grant that violates any of these rules is a scope-widening attempt.
The evaluator returns `Deny / DenyReasonScopeWidening`.

---

## §2 — Predicate AST Schema

### §2.1 Leaf predicates

Five leaf predicate kinds. There is no `not:` predicate — complement predicates are
escalation primitives; banning them is a deliberate safety property of the language.

```
LeafPredicate =
  LevelPredicate
  | GrantPredicate
  | GrantInPredicate
  | GrantQuotaPredicate
  | ChainToPredicate
  | ChainToQuorumPredicate
```

#### `level: N` — Minimum provenance level

```
LevelPredicate = { kind: "level", n: uint }
```

The chain root's provenance level MUST be >= N. The four levels:

| Level | Meaning |
|-------|---------|
| 0 | Anonymous — valid keypair only |
| 1 | Claimed — self-asserted operator identity (tainted) |
| 2 | Contactable — verified by challenge/response or blind-relay hop |
| 3 | Present — level 2 within a freshness window |

#### `grant: <conv>:<op>` — Exact capability check

```
GrantPredicate = { kind: "grant", convention: tstr, op: tstr }
```

The chain carries a capability matching `convention` exactly and `op` exactly.

#### `grant_in: <conv>:<op-glob> at <where-matcher>` — Scoped capability check

```
GrantInPredicate = {
  kind: "grant_in"
  convention: tstr
  op_glob: tstr          ; within-convention pattern
  where: WhereMatcher    ; the request target MUST be covered by this matcher
}
```

#### `grant_quota: <axis> >= <bound>` — Bound check

```
GrantQuotaPredicate = {
  kind: "grant_quota"
  axis: tstr           ; one of the reserved bound keys (rate | quota | spend | ttl)
  bound: uint          ; bound NOT yet exhausted check
}
```

#### `chain_to: <principal>` — Trust anchor check

```
ChainToPredicate = { kind: "chain_to", pubkey: bstr }  ; 32-byte Ed25519 key
```

Chain root is the named principal **by KEY, not by name**. Naming-as-authority is a
security vector.

#### `chain_to_quorum: M-of-N` — Multi-sysop authority

```
ChainToQuorumPredicate = {
  kind: "chain_to_quorum"
  m: uint           ; minimum signers required
  pubkeys: [+ bstr] ; N Ed25519 public keys (32 bytes each), sorted ascending
}
```

Composed via `all_of` for team-sysop authority patterns.

### §2.2 Composite predicates

```
CompositePredicate =
  AllOfPredicate
  | AnyOfPredicate

AllOfPredicate = { kind: "all_of", children: [+ PredicateAST] }
AnyOfPredicate = { kind: "any_of", children: [+ PredicateAST] }
```

**Maximum nesting depth: 3.** The evaluator is a finite tree-walk, O(predicate-nodes ×
chain-depth). Predicates with depth > 3 MUST be rejected at parse time.

**No `not:` predicate.** Complement predicates change the language's safety category.
Adding a content-equality primitive (`hash_equals`) is rejected on the same grounds; it
addresses keys by literal value, the same escalation-vector class as `not:`.

### §2.3 PredicateAST (complete type)

```
PredicateAST =
  LeafPredicate
  | CompositePredicate

; Go encoding (in cf-authority/trust/evaluator.go):
type PredicateAST struct {
    Kind     PredicateKind
    Leaf     *LeafPredicate    ; populated for leaf kinds
    Children []PredicateAST    ; populated for all_of / any_of
}
```

---

## §3 — Evaluator Interface (Go)

Location: `cf-conventions/cf-authority/trust/evaluator.go`

### §3.1 GateEvaluator interface

```go
// GateEvaluator decides whether a request is permitted, denied, or
// unresolvable given a delegation chain, root principal, current time,
// freshness-bounded revocation view, and owner policy.
//
// Frozen at cf-authority 1.0.
type GateEvaluator interface {
    Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResult
}
```

### §3.2 EvaluateRequest

```go
type EvaluateRequest struct {
    // Request describes the operation under gate.
    Request OpRequest

    // ChainMessages is the verified delegation chain, ordered sender→anchor.
    // May be empty (anchor-self case). Signature verification is the caller's
    // responsibility — the evaluator assumes pre-verified inputs.
    ChainMessages []message.Message

    // RootPrincipal is the trust anchor at which ChainMessages terminates.
    // Expressed as a key (chain_to predicate is by KEY, not name).
    RootPrincipal ed25519.PublicKey

    // CurrentTime is the wall-clock time used for expiry/freshness checks.
    // Passed as input (not read from a clock) to ensure determinism.
    CurrentTime time.Time

    // RevocationView is the local observation of revocation state.
    // Empty/missing entries are NOT "no revocations" — they are "no view,"
    // which the evaluator MAY surface as Unresolvable depending on
    // OwnerPolicy.MaxRevocationStaleness.
    RevocationView []RevocationViewEntry

    // OwnerPolicy is the owner-of-record's ceiling.
    // Composition: owner > parent > declaration (one-directional).
    OwnerPolicy OwnerPolicy
}

type OpRequest struct {
    Convention string            // exact convention name
    Operation  string            // operation name within convention
    CampfireID string            // target campfire (hex)
    Tags       []string          // request tags (for tag-matcher gates)
    Sender     ed25519.PublicKey // request sender
    Predicate  PredicateAST      // the gate's compiled predicate tree
}

type RevocationViewEntry struct {
    CampfireID          string
    LatestObservedMsgID string
    ObservedAt          time.Time
}

type OwnerPolicy struct {
    MaxRevocationStaleness time.Duration
    MinLevelOverride       int      // owner-imposed level floor (0 = no override)
    BlanketDeny            []string // convention:op patterns blanket-denied by owner
}
```

### §3.3 EvaluateResult

```go
type EvaluateResult struct {
    Decision Decision

    // Reason is set when Decision == Deny. Required, non-empty.
    // Stable codes are part of the conformance contract.
    Reason DenyReason

    // MissingMessageID is set when Decision == Unresolvable.
    // Identifies the chain message the evaluator could not resolve.
    MissingMessageID string
}

type Decision uint8

const (
    Allow        Decision = iota + 1
    Deny
    Unresolvable
)
```

### §3.4 DenyReason enum (frozen)

Stable string codes. Part of the cf-authority 1.0 wire freeze. Two implementations that
agree on `Deny` MUST agree on the reason code for any canonical input; the conformance
harness asserts this.

```go
type DenyReason string

const (
    DenyExpired         DenyReason = "expired"           // grant.until < CurrentTime
    DenyRevoked         DenyReason = "revoked"           // active revocation in view
    DenyDepthExceeded   DenyReason = "depth_exceeded"    // chain depth > 2
    DenyScopeMismatch   DenyReason = "scope_mismatch"    // grant scope does not cover request
    DenyScopeWidening   DenyReason = "scope_widening"    // child claimed wider than parent
    DenyStaleRevocation DenyReason = "stale_revocation"  // view stale beyond max staleness
    DenyReservedOpFloor DenyReason = "reserved_op_floor" // reserved-op depth/level floor
    DenyOwnerCeiling    DenyReason = "owner_ceiling"     // owner policy blanket denies
    DenyPredicate       DenyReason = "predicate_unsatisfied" // gate predicate not satisfied
    DenyStoreRead       DenyReason = "store_read_error"  // chain truncated; fail-closed
)
```

---

## §4 — Conformance Contract

### §4.1 Determinism (deal-breaker D1)

An implementation of `GateEvaluator` MUST be a **pure function of its inputs**:

- Same inputs → same output, byte-for-byte.
- No hidden global state.
- No clocks read inside the call (`CurrentTime` is an input).
- No store reads inside the call (`ChainMessages` is an input).

The conformance harness runs each test case **three times** with identical inputs and
asserts byte-equal results. Non-determinism is its own failure category.

### §4.2 Fail-closed (deal-breaker D2)

When an implementation cannot resolve an introducer locally (chain truncated, missing
grant message), it MUST return `Unresolvable` with `MissingMessageID` populated.

An implementation MUST NEVER return `Allow` on inputs that cannot be resolved.

Dispatchers treat `Unresolvable` as `Deny` (fail closed) but MAY synthesize a
`delegation:request` future to populate the local view on the upgrade path to the
approval flow.

### §4.3 Predicate-AST canonicalization

Children of `all_of` and `any_of` nodes MUST be sorted in canonical order:

1. Primary sort: `PredicateKind` (numeric enum order).
2. Secondary sort: operand (lexicographic on string representation of the leaf's
   primary operand — `convention:op` for grant predicates, pubkey hex for `chain_to`,
   level N for `level`).

Implementations that disagree on canonical child ordering disagree on outputs under
short-circuit evaluation. **AST canonicalization is part of the conformance contract**,
not an implementation detail.

### §4.4 CBOR canonicalization

All grant CBOR is deterministic per RFC 8949 §4.2.1:

- Definite-length encoding for all maps and arrays.
- Map keys sorted by RFC 8949 §4.2.1 (length-then-bytes for text keys; numeric keys
  sorted by integer value).
- Smallest integer encoding (no leading zero bytes).
- No NaN or Infinity float values.

An implementation that re-encodes grant CBOR to a different canonical form will fail
signature verification on all test cases that include signed grants.

### §4.5 Reserved-op floor

The ten reserved ops carry a protocol-level minimum that no convention declaration and
no parent grant can lower:

```
disband | evict | admit | grant | revoke | delegation-grant |
delegation-revoke | delegation-accept | member-roster | compaction
```

The enforcement chain is fixed and one-directional:

```
owner ceiling > parent grant > convention declaration
```

The LIST lives at L1 (`cf-protocol/internal/reserved-ops.go`). The ENFORCER (intercepts
dispatch, consults L1 list) lives at L2. The EVALUATOR (applies actual policy) is L3
`cf-authority`. Convention authors are not in the trusted computing base; a convention
that declares `level: 0` on `delegation-grant` is rejected with `DenyReservedOpFloor`.

### §4.6 UNRESOLVABLE handling

`UNRESOLVABLE` is a first-class third decision. It differs from gnupg-style designs that
assume a global keyring; AIETF does not have one:

- Evaluator CANNOT resolve an introducer → MUST return `Unresolvable`.
- Dispatcher MUST treat `Unresolvable` as `Deny` (fail closed).
- Dispatcher MAY synthesize `delegation:request` future to populate the local view.
- Bounded fetch attempt, then closed. No silent degradation.

---

## §5 — Conformance Harness

Location: `cf-conventions/cf-authority/trust/conformance/`

The `GateEvaluatorConformance(t, factory)` harness ships twelve canonical test cases.
Third-party implementations of `GateEvaluator` MUST pass all twelve before being eligible
to back a cf-authority-conforming dispatcher.

### §5.1 Twelve canonical test cases

| # | Case | Expected | Falsifies |
|---|------|----------|-----------|
| 1 | `anchor-self` — sender == root_principal; empty chain; `level: 0` | Allow | Implementations requiring non-empty chain when sender is anchor |
| 2 | `valid-1-hop` — one grant, unexpired, unrevoked; `grant: ready:claim` | Allow | Implementations that miscount depth or skip leaf-grant signature check |
| 3 | `valid-2-hop` — two grants; `grant_in: ready:claim\|done at name-prefix:rd-` | Allow | Implementations failing to intersect scope across two hops |
| 4 | `expired-mid-chain` — hop-2 `until` < CurrentTime by 1ns | Deny / `expired` | Off-by-one on expiry boundary |
| 5 | `revoked-mid-chain` — fresh revocation view names hop-2 child key | Deny / `revoked` | Implementations that only revoke on chain-root, not mid-chain |
| 6 | `depth-exceeded` — three-hop chain against depth limit 2 | Deny / `depth_exceeded` | Implementations allowing depth-3 by default |
| 7 | `scope-narrowing` — hop-2 scope is subset of hop-1; request matches hop-2 | Allow | Implementations that intersect wrong (union instead of intersection) |
| 8 | `scope-widening-rejected` — hop-2 claims wider than hop-1 | Deny / `scope_widening` | Implementations taking child-scope at face value |
| 9 | `store-read-error-fail-closed` — chain truncated; missing-sentinel for hop-2 | Unresolvable / MissingMessageID set | Implementations that Deny (lose upgrade path) OR Allow (catastrophic) |
| 10 | `stale-revocation-window` — view ObservedAt + MaxRevocationStaleness < CurrentTime | Deny / `stale_revocation` | Implementations ignoring revocation view staleness |
| 11 | `reserved-op-floor` — convention declares `level: 0` on `delegation-grant` | Deny / `reserved_op_floor` | Implementations honoring convention-author overrides on reserved ops |
| 12 | `await-fulfillment-ordering` — two `fulfills`-tagged messages with identical timestamps; lexicographically distinct IDs | Allow + deterministic winner | Implementations ordering on insertion/wall-time/store-iteration order |

### §5.2 Determinism check

Each case is run **three times** with identical inputs. Results MUST be byte-equal.
Non-determinism is a conformance failure independent of the case outcome.

### §5.3 Cross-implementer differential check

When run against ≥2 implementations, the harness emits a differential report: for each
case, the `(Decision, Reason, MissingMessageID)` triple from each implementation.
Disagreement on any case is a conformance failure.

### §5.4 Fixture data

Canonical fixture data lives in `cf-authority/trust/conformance/testdata/`:

```
testdata/
  keys/
    root.pub, root.priv           # trust anchor keypair
    intermediate.pub, intermediate.priv
    sender.pub, sender.priv
    rogue.pub, rogue.priv         # for widening/widening-reject tests
  campfires/
    rd-baron.id, other.id
  chains/
    1-anchor-self.cbor
    2-valid-1-hop.cbor
    3-valid-2-hop.cbor
    4-expired-mid-chain.cbor
    5-revoked-mid-chain.cbor
    6-depth-exceeded.cbor
    7-scope-narrowing.cbor
    8-scope-widening.cbor
    9-store-read-error.cbor
    10-stale-revocation.cbor
    11-reserved-op-floor.cbor
    12-await-fulfillment.cbor
  predicates/
    p-level-0.json
    p-grant-ready-claim.json
    p-grant_in-ready-name-prefix.json
    p-all_of-level-grant.json
    p-await-chain_to.json
  expected/
    1.json, 2.json, ..., 12.json
  tampered/               # bit-flipped variants for negative tests
```

**Fixture format constraints (frozen at cf-authority 1.0):**

- All CBOR: deterministic per RFC 8949 §4.2.1 (see §4.4).
- All timestamps: int64 nanoseconds-since-Unix-epoch UTC.
  Canonical `t0 = 2026-01-01T00:00:00Z = 1767225600000000000 ns`.
- All keys: raw Ed25519 (32-byte public, 64-byte expanded private). No x509, no PKCS.
- Predicate ASTs: JSON in fixture files, parsed to `PredicateAST` by the fixture loader.
- The fixture generator at `conformance/gen/main.go` regenerates all fixtures from
  source-of-truth YAML. CI asserts byte-identical regeneration.

---

## §6 — Phase 8 Ship Gates

Gate 1 (GateEvaluator conformance): all twelve cases pass on the default implementation;
determinism re-run (3× byte-equal) verified; CI runs the harness on every PR touching
`cf-authority/`, `pkg/convention/delegation/`, `pkg/protocol/await.go`, or fixture files.

Gate 4 (wire-format freeze): this spec document is published; `COMPATIBILITY.md` shipped;
minor-compat CI workflow active. No PR may modify frozen surfaces after 0.30 tag without
a major-bump declaration.

---

## §7 — Open Follow-ups (not on Phase 8 gate)

1. **Predicate-AST fuzzer extension** — random `PredicateAST` trees (depth 1–3, all leaf
   kinds) exercised against two implementations; ships as `conformance/fuzz` target gated
   on `-tags fuzz`. ~120 LOC.
2. **Non-UTC timezone case** — thirteenth case where `CurrentTime` carries non-UTC offset.
   Deferred pending operator decision on whether UTC-only is a spec constraint.
3. **CBOR tamper negative tests** — enumerate negative cases consuming `testdata/tampered/`;
   current position is wire-layer's job (evaluator assumes pre-verified inputs).
4. **Reserved-op floor list freshness** — case 11 fixture must update if OPEN-003 lands
   a different list. Cross-reference: OPEN-003.

---

*Generated: 2026-04-28. Specification status: FROZEN at cf-authority 1.0.*
*Source: 0.30-design.md §2, §2.2–§2.5.1, §7; gate-evaluator-conformance-harness.md.*
