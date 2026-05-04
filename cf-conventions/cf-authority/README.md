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

The primary entry point is `trust.NewDefaultGateEvaluator(trustStore)`.

| Symbol | Description |
|--------|-------------|
| `trust.NewDefaultGateEvaluator` | Production `GateEvaluator` implementation |
| `trust.NewDefaultProvenanceChecker` | Production `ProvenanceCheckerV2` implementation |
| `trust.GrantPayload` | CBOR-serializable grant payload (field 5 = `GranterPubKey`) |
| `trust.Capability` | Scoped capability declaration |
| `trust.PredicateAST` | Gate predicate tree (max depth 3; no `not:`) |
| `trust.WhereMatcher` | Scope matcher for `grant_in` predicates |
| `trust.DenyReason` | Deny classification (`DenyReservedOpFloor`, etc.) |
| `trust.Decision` | `GateAllow` / `GateDeny` / `GateUnresolvable` |

Godoc: https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-authority/trust

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
