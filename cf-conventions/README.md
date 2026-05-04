# cf-conventions

> **Module:** `github.com/campfire-net/campfire/cf-conventions`
>
> **Cross-links:**
> - [docs/0.30-overview.md](../docs/0.30-overview.md) — architecture overview
> - Design v2 §5 (the surface) — `docs/design/v2/`
> - [cf-conventions/COMPATIBILITY.md](COMPATIBILITY.md) — versioning policy

`cf-conventions` is the L2/L3 layer of the campfire module system. It contains
the convention server SDK (L2) and seven L3 convention-implementation packages
that build on it.

**Layer rules (enforced by depguard CI):**
- L2 may import L1 (`cf-protocol/protocol`). L2 must not import L3.
- L3 may import L1 and L2. L3 packages must not import each other horizontally.
- L4 (CLI, MCP, hosted service) may import any layer.

---

## Packages

### L2 — Convention Machinery

#### `cf-convention/`

The convention server SDK. Provides `Server` (subscribe, dispatch, auto-thread
responses), `Executor` (send typed operations, await responses), and
`ConventionDispatcher` (the lower-level dispatch loop used by `cf-mcp` and the
hosted service). Declares the `GateEvaluator`, `ProvenanceCheckerV2`, and
`IdentityResolver` interfaces — all frozen at cf-conventions 1.0.

[README](cf-convention/README.md) · [Godoc](https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-convention)

---

### L3 — Convention Implementations

#### `cf-authority/`

Full trust chain evaluation. Implements the `GateEvaluator` interface with the
five-leaf gate predicate language (`level`, `grant`, `grant_in`, `grant_quota`,
`chain_to`, `chain_to_quorum`, `all_of`, `any_of`). Ships `DefaultGateEvaluator`,
`cf approve` scope auto-suggestion, `cf trust pin/unpin/list/prune` for TOFU
key management, and `cf init --policy <preset>` for identity-policy initialization.
All 10 D-class deal-breakers enforced: chain walk, revocation, scope ceiling,
reserved-op floor, depth limit, owner ceiling, and TTL. Wire-format freeze
verifier included.

[README](cf-authority/README.md) · [Godoc](https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-authority)

#### `cf-identity/`

Canonical identity ceremony package. Ships five declaration constructors:
`IntroduceMeDeclaration`, `DeclareHomeDeclaration`, `VerifyMeDeclaration`,
`ListHomesDeclaration`, `EchoDeclaration`. Handles `identity:revoked` tag and
`ProfileFile` load/save. Used by `cf-mcp` to expose identity ceremonies as MCP
tools.

[README](cf-identity/README.md) · [Godoc](https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-identity)

#### `cf-session/`

Session lifecycle and lazy-mint per-worker grants. Orchestrators open a session
with a `CapabilityTemplate`; workers receive unique Ed25519 keys and
parent-bounded grants (`IssueWorkerGrant`). `MaterializeWorkerIdentity` writes
worker keys to a jail directory (`0700`/`0600`). `cfs2_` token format (replaces
`cfs1_` shared-key tokens). `session:open` / `session:close` lifecycle events.

[README](cf-session/README.md) · [Godoc](https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-session)

#### `cf-discovery/`

Namespace discovery: beacon signing, snippet validation, and the 3-tier
discovery model (Tier 1 = snippet, Tier 2 = TOFU-pinned name resolution,
Tier 3 = post-join probe-write verification). `ResolveChain` for multi-level
namespace walks (`rd.ready.3dl`). Sentinel errors `ErrInviteOnly` and
`ErrPostJoinVerificationFailed`. Rate-limit declarations for level-0 ops
(OPEN-013). Config-over-beacon endpoint precedence.

[README](cf-discovery/README.md) · [Godoc](https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-discovery)

#### `cf-durability/`

Message lifecycle and TTL semantics. `CheckDurabilityTags` parses `durability:`
and `lifecycle:` message tags and returns a `DurabilityResult` (expired, live,
quota-eligible). `ParseMaxTTL`, `URICacheTTL`, `ParseLifecycle` for convention
authors that declare expiry-aware operations. Moved from `pkg/durability` in
0.30.

[README](cf-durability/README.md) · [Godoc](https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-durability)

#### `cf-connect/`

Social connection protocol. Declares `connect-request`, `accept-connection`, and
`reject-connection` operations under the `social` convention namespace.
`ConnectDeclarations()` returns all three for bulk registration. Moved from
`cf-convention-extensions/connect/` in 0.30.

[README](cf-connect/README.md) · [Godoc](https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-connect)

#### `cf-convention-extension/`

Convention lifecycle operations: `promote` and `supersede`. `ValidatePromote`
checks that a message carries a valid promote payload and a signer key that
matches the campfire operator. `ValidateSupersede` additionally verifies version
ordering against the existing declaration. Used by the convention registry to
manage live declaration upgrades.

[README](cf-convention-extension/README.md) · [Godoc](https://pkg.go.dev/github.com/campfire-net/campfire/cf-conventions/cf-convention-extension)

---

## Demos

`cf-conventions/demos/` contains shell scripts that demonstrate each package
against a real campfire. Notable demos:

| Script | What it shows |
|--------|---------------|
| `cf-authority/chain-walk.sh` | Full delegation chain walk |
| `cf-authority/grant-and-revoke.sh` | Grant issuance and revocation |
| `cf-session/session-lifecycle.sh` | Open → worker-grant → close flow |
| `cf-discovery/beacon-resolve.sh` | Beacon signing and multi-level resolve |
| `cf-identity/identity-flow.sh` | Full introduce → declare-home → verify flow |
| `cf-durability/durability-check.sh` | Expiry tag parsing |
| `cf-connect/peer-handshake.sh` | Social connection handshake |
| `parity-check.sh` | CLI/MCP parity across all L3 packages |

---

## Parity Testing

`parity/parity_test.go` runs 22 named-fixture parity cases across `cf-identity`
(6), `cf-authority` (10), and `cf-discovery` (6) plus 4 stress cases. Five parity
axes are verified: name (A1), argument schema (A2), return shape (A3), error
category (A4), executor-boundary args (A5). These tests run in CI on every PR.

---

*Module version: cf-conventions v1.0 — cf-protocol floor: >= v0.19.*
*See COMPATIBILITY.md for the cross-module versioning policy.*
