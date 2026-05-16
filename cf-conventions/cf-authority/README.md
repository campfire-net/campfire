# cf-conventions/cf-authority

Package `cf-authority` provides the full trust chain evaluator for campfire —
implementing the L2 `GateEvaluator` interface with the five-leaf gate predicate
language, TOFU trust-pin management, and identity-policy presets.

## Purpose

Every convention operation dispatched through `ConventionDispatcher` passes
through a `GateEvaluator.Evaluate` call. `cf-authority` provides the production
implementation: `DefaultGateEvaluator`, which walks delegation chains, checks
revocation, enforces scope ceilings, and validates trust anchors.

It is the sole resolver of the 10 D-class deal-breakers from
[`docs/0.30-deal-breakers.md`](../../docs/0.30-deal-breakers.md).

## Public Surface

The L3 evaluator is `trust.DefaultGateEvaluator` — a pure, zero-value struct (no
TrustStore dependency; D1 purity invariant). Consumers do not call it directly;
they wire it into the L2 `ConventionDispatcher` through a thin adapter.

The wiring pattern:

```go
adapter := trust.NewConventionAdapter()
dispatcher := convention.NewConventionDispatcher(store, logger)
dispatcher.SetGateEvaluator(adapter)
```

| Symbol | Description |
|--------|-------------|
| `trust.DefaultGateEvaluator` | Reference `GateEvaluator` (L3). Zero-value struct, pure function. |
| `trust.NewConventionAdapter` | Returns `*ConventionAdapter` — satisfies `convention.GateEvaluator` (L2) by wrapping `DefaultGateEvaluator`. |
| `trust.ConventionAdapter` | L3→L2 adapter type. |
| `trust.GrantPayload` | CBOR-serializable grant payload (field 5 = `GranterPubKey`) |
| `trust.Capability` | Scoped capability declaration |
| `trust.PredicateAST` | Gate predicate tree (max depth 3; no `not:`) |
| `trust.WhereMatcher` | Scope matcher for `grant_in` predicates |
| `trust.DenyReason` | Deny classification (`DenyReservedOpFloor`, etc.) |
| `trust.Decision` | `Allow` / `Deny` / `Unresolvable` |

`ProvenanceCheckerV2` lives in `cf-conventions/cf-convention`, not in this
package. v0.31 ships only the stub `convention.NewAllowAllProvenanceChecker()`;
a production implementation is on the cf-authority roadmap (see Planned below).

Godoc: https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-authority/trust

## Planned (not in v0.31)

The following are referenced in source comments but **do not exist** as of
v0.31.0. They are tracked for a later release; do not write code against them
yet:

| Symbol | Status | Notes |
|--------|--------|-------|
| `Server.WithGateEvaluator(eval)` option | Planned | `cf-convention/gate_evaluator.go` carries a "Stage 3 transition" comment; the higher-level functional-option surface will land in a future 0.3x release. Until then, use `ConventionDispatcher.SetGateEvaluator(adapter)` directly. |
| Production `ProvenanceCheckerV2` | Planned | v0.31 ships only the allow-all stub in `cf-convention`. |

## Wire-Format Freeze

`cf-authority/` ships its own freeze verifier (separate from `cf-protocol`).
15 mutation-confirmed tests cover `Capability`, `GrantPayload`, `WhereMatcher`,
`PredicateAST`, and `DenyReason` CBOR types. CI runs these tests with
`-count=3` on every push touching this package.

## Gate Predicate Language

Predicates compose in a tree up to depth 3. Available leaf kinds:

| Kind | What it checks |
|------|---------------|
| `level: N` | Sender provenance level ≥ N |
| `grant: <conv>:<op>` | Exact capability in grant chain |
| `grant_in: <conv>:<op-glob> at <where>` | Scoped capability |
| `grant_quota: <axis> >= <bound>` | Quota bound on grant axis |
| `chain_to: <pubkey>` | Chain root = specific key |
| `chain_to_quorum: M-of-N` | Chain root = M of N listed keys |
| `all_of: [...]` | AND of child predicates |
| `any_of: [...]` | OR of child predicates |

No `not:` predicate exists by design. Use `OwnerPolicy.BlanketDeny` for denial.

## Demo Scripts

- `cf-conventions/demos/cf-authority/chain-walk.sh` — delegation chain walk
- `cf-conventions/demos/cf-authority/grant-and-revoke.sh` — grant and revocation
- `cf-conventions/demos/cf-authority/reserved-op-floor.sh` — D5 floor enforcement
- `cf-conventions/demos/cf-authority/stale-revocation-fail-closed.sh` — revocation edge cases
- `cf-conventions/demos/cf-authority/conformance-ci-check.sh` — 12-case conformance suite

## Design References

- `docs/cf-authority-spec.md` — full specification
- `docs/0.30-deal-breakers.md` — D1–D10 requirements
- `docs/upgrade-0.19-to-0.30.md` §BC-9, §BC-11, §BC-18 — migration details
- `docs/0.30-overview.md` §Trust Authority — architecture summary
