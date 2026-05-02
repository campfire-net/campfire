# Gate Predicates — cf-authority 1.0

Gate predicates express the authorization condition a request must satisfy. They appear in convention declarations (`min_operator_level` is the simple form) and in grant payloads (the full AST form). The evaluator returns `Allow`, `Deny(reason)`, or `Unresolvable`.

## Six leaf predicates

### `level: N` — Minimum provenance level

```json
{ "kind": "level", "n": 2 }
```

The chain root's provenance level must be >= N.

| Level | Meaning |
|---|---|
| 0 | Anonymous — valid keypair only |
| 1 | Claimed — self-asserted identity (tainted) |
| 2 | Contactable — verified by challenge/response |
| 3 | Present — level 2 within a freshness window |

Use `level: 2` for operations that require a verified human operator.

### `grant: <convention>:<op>` — Exact capability

```json
{ "kind": "grant", "convention": "swarm-coordination", "op": "claim-item" }
```

The delegation chain must carry a capability matching this convention and op exactly. No wildcards. Use this to gate an operation on the caller holding a specific named capability.

### `grant_in: <convention>:<op-glob> at <where>` — Scoped capability

```json
{
  "kind": "grant_in",
  "convention": "swarm-coordination",
  "op_glob": "claim-item|complete",
  "where": { "kind": 3, "tag": "session:active" }
}
```

Like `grant` but also checks that the request targets a campfire covered by the `where` matcher. Three `where` kinds compose as OR:

| Where kind | Code | Field |
|---|---|---|
| Exact campfire ID | 1 | `"id": "<hex>"` |
| Name prefix | 2 | `"prefix": "project."` |
| Campfire tag | 3 | `"tag": "session:active"` |

An empty `where` list means "wherever the agent is a member."

### `grant_quota: <axis> >= <bound>` — Bound check

```json
{ "kind": "grant_quota", "axis": "quota", "bound": 5 }
```

The grant's bound on `axis` must not be exhausted. Reserved axes: `rate`, `quota`, `spend`, `ttl`. Convention authors declare which axes they honor; unknown axes fail closed.

### `chain_to: <pubkey>` — Trust anchor

```json
{ "kind": "chain_to", "pubkey": "<32-byte Ed25519 key as hex>" }
```

The delegation chain must terminate at the named principal **by key, not by name**. Naming-as-authority is a security vector — the predicate anchors to the 32-byte Ed25519 public key only. Use `cf id` to get your own key.

### `chain_to_quorum: M-of-N` — Multi-sysop authority

```json
{
  "kind": "chain_to_quorum",
  "m": 2,
  "pubkeys": ["<key1-hex>", "<key2-hex>", "<key3-hex>"]
}
```

At least M of the N listed principals must appear in the delegation chain. Keys must be sorted ascending. Use this for team-operated campfires that require multiple approvals.

## Composite predicates

Two composites, max depth 3:

```json
{ "kind": "all_of", "children": [ <predicate>, <predicate>, ... ] }
{ "kind": "any_of", "children": [ <predicate>, <predicate>, ... ] }
```

`all_of` is AND; `any_of` is OR. There is no `not:` — complement predicates are banned as a deliberate safety property (§2.2 of the authority spec). Use explicit allow-list predicates with `any_of` instead.

Example — require level 2 AND a specific grant:

```json
{
  "kind": "all_of",
  "children": [
    { "kind": "level", "n": 2 },
    { "kind": "grant", "convention": "my-service", "op": "admin-reset" }
  ]
}
```

## `chain_to_quorum` — when to use it

`chain_to_quorum` is for campfires with multiple operators where you need M-of-N agreement before a high-trust operation proceeds. Example: a 2-of-3 policy means any two of three named principals in the chain satisfies the gate. Combined with `all_of`:

```json
{
  "kind": "all_of",
  "children": [
    { "kind": "level", "n": 3 },
    { "kind": "chain_to_quorum", "m": 2, "pubkeys": ["<k1>","<k2>","<k3>"] }
  ]
}
```

## Bounds — reserved keys

Bounds constrain what a child grant may claim. The reserved bound keys (unknown keys fail closed):

| Key | Value | Semantics |
|---|---|---|
| `rate` | `{per, count, window}` | Op rate cap per keypair |
| `quota` | `{unit, max}` | Lifetime op count cap |
| `spend` | `{unit, max}` | Monetary/scrip spend cap |
| `ttl` | uint (seconds) | Lifetime bound on any derived grant |

## Evaluator outputs

| Decision | Meaning | What to do |
|---|---|---|
| `Allow` | Request permitted | Proceed |
| `Deny(reason)` | Request denied; `reason` is a stable code | Reject; log the reason code |
| `Unresolvable` | Chain is truncated; cannot reach a decision | Treat as Deny (fail-closed); optionally synthesize a `delegation:request` future |

Stable deny reason codes: `expired`, `revoked`, `depth_exceeded`, `scope_mismatch`, `scope_widening`, `stale_revocation`, `reserved_op_floor`, `owner_ceiling`, `predicate_unsatisfied`, `store_read_error`.

## See also

- `docs/cf-authority-spec.md` — full wire schema for grants and predicate AST
- `docs/agent/convention-authoring.md` — where predicates appear in declarations
- `docs/agent/failure-modes.md` — UNRESOLVABLE handling, revocation freshness
