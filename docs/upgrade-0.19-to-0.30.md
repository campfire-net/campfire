# Upgrade Guide: cf 0.19 → 0.30

> **Audience:** Authors porting consumer applications (`rd`, `dontguess`,
> `social`, `the reach`, `freeso`) from cf 0.19.x to cf 0.30.0.
>
> **Cross-links:**
> - CHANGELOG.md v0.30.0 — full feature/fix list
> - Design v2 §7 — migration rationale and phased rollout model
> - `cf-conventions/COMPATIBILITY.md` — cross-module versioning policy
> - `cf-protocol/COMPATIBILITY.md` — wire-format freeze contract
>
> **Status:** Frozen at cf 0.30.0. Post-0.30 follow-ups tracked in OPEN items.

---

## Overview

cf 0.30.0 is a structural release. It reorganizes the substrate into a
layered module system (`cf-protocol` + `cf-conventions`), ships a complete
trust authority (`cf-authority`), and freezes the wire format. Five portfolio
consumers (`rd`, `dontguess`, `social`, `the reach`, `freeso`) remain on
0.19.x until their port windows open; this guide is the authoritative
reference for those port-authoring agents.

**Consumer summary:**

| Consumer | Current version | Key change count |
|----------|---------------|-----------------|
| `rd`      | 0.19.2 | 5 (imports, WithNoWalkUp, min\_operator\_level, convention API) |
| `dontguess` | 0.17.0 | 4 (imports, convention API) |
| `social`  | 0.19.2 | 3 (imports, convention API) |
| `the reach` (galtrader) | 0.13.1 | 5 (imports, session, WalkUp, convention API) |
| `freeso` | 0.13.0 | 4 (imports, session, convention API) |

---

## Part I — Breaking Changes Inventory

Each section: **what changed**, **before/after snippet**, **rationale**,
**demo pointer**.

---

### BC-1 — Substrate packages moved to `cf-protocol/`

**What changed:**  
All substrate packages previously under `pkg/` have moved to
`cf-protocol/internal/`. They are re-exported through
`cf-protocol/protocol/` as type aliases. Code that imported
`pkg/campfire`, `pkg/message`, `pkg/store`, `pkg/transport/fs`,
`pkg/transport/http`, `pkg/threshold`, `pkg/projection`,
`pkg/predicate`, `pkg/crypto`, `pkg/encoding`, or `pkg/admission`
directly will not compile — use `cf-protocol/protocol` or the
`pkg/protocol` forwarding shim.

**Import path table:**

| Before (0.19)                        | After (0.30)                          |
|--------------------------------------|---------------------------------------|
| `github.com/campfire-net/campfire/pkg/campfire` | moved to `cf-protocol/internal/campfire`; re-exported via `cf-protocol/protocol` |
| `github.com/campfire-net/campfire/pkg/message` | moved to `cf-protocol/internal/message`; re-exported via `cf-protocol/protocol` |
| `github.com/campfire-net/campfire/pkg/store` | moved to `cf-protocol/internal/store`; re-exported via `cf-protocol/protocol` |
| `github.com/campfire-net/campfire/pkg/transport/fs` | moved to `cf-protocol/internal/transport/fs` |
| `github.com/campfire-net/campfire/pkg/transport/http` | moved to `cf-protocol/internal/transport/http` |
| `github.com/campfire-net/campfire/pkg/threshold` | moved to `cf-protocol/internal/threshold` |
| `github.com/campfire-net/campfire/pkg/projection` | moved to `cf-protocol/internal/projection` |
| `github.com/campfire-net/campfire/pkg/predicate` | moved to `cf-protocol/internal/predicate` |
| `github.com/campfire-net/campfire/pkg/crypto` | moved to `cf-protocol/internal/crypto` |
| `github.com/campfire-net/campfire/pkg/encoding` | moved to `cf-protocol/internal/encoding` |
| `github.com/campfire-net/campfire/pkg/admission` | moved to `cf-protocol/internal/admission` |
| `github.com/campfire-net/campfire/pkg/durability` | moved to `cf-conventions/cf-durability` |
| `cf-conventions/cf-convention-extensions/connect/` | moved to `cf-conventions/cf-connect` |

**pkg/store/aztable remains at `pkg/store/aztable`** (it implements
`convention.DispatchStore` from `pkg/convention` — L2 dep).

**Before:**
```go
import (
    "github.com/campfire-net/campfire/pkg/campfire"
    "github.com/campfire-net/campfire/pkg/protocol"
)

// Direct substrate use:
cf := campfire.New("open", nil, 1)
```

**After:**
```go
import (
    "github.com/campfire-net/campfire/cf-protocol/protocol"
)

// Substrate types are re-exported:
cf := protocol.NewCampfire("open", nil, 1)

// For the common case (Init + client methods): pkg/protocol still compiles
// via the forwarding shim. The SDK-level API is unchanged.
import "github.com/campfire-net/campfire/pkg/protocol"
client, _, err := protocol.Init(cfHome)
```

**Rationale:** The substrate is now `cf-protocol/internal/` — an L1 module
with a stable, frozen public surface. Direct substrate imports from consumer
apps bypass the public surface contract. The forwarding shim in `pkg/protocol`
preserves backward compatibility for SDK-level callers (Init, Send, Read,
etc.) without exposing internals.

**Most consumers** (rd, dontguess, social, freeso) only use `pkg/protocol`
and `pkg/convention` — they are not affected by the substrate move. Only code
that imported internal packages directly (message types, store types, transport
types) needs updating.

**Demo:** `cf-conventions/demos/0.30-deal-breakers-walkthrough.sh`

---

### BC-2 — Center-finding removed (`recenter`, `walk_up`, `WalkUp*`)

**What changed:**  
The center-finding feature (automatic campfire locality at `Init` time via
`walkUpForCenter`) is removed from the substrate. All related APIs are gone:

- `protocol.WithWalkUp()` — deleted
- `protocol.WithNoWalkUp()` — deleted
- `protocol.WalkUpEnabled()` — deleted
- `InitResult.WalkUpPath` — field deleted
- `InitResult.Recentered` — field deleted
- `InitResult.DelegationIssued` — field deleted
- `RecenterClaim` type — deleted
- `RecenterCanonicalPayload` function — deleted
- Config key `behavior.walk_up` in `.cf/config.toml` — silently ignored

**Before (0.19):**
```go
import "github.com/campfire-net/campfire/pkg/protocol"

// Suppress walk-up during import:
client, result, err := protocol.Init(cfHomeDir, protocol.WithNoWalkUp())
if err != nil { ... }
if result.Recentered {
    log.Printf("recentered to %s via %v", result.WalkUpPath[0], result.WalkUpPath)
}

// Or enable walk-up explicitly:
client, result, err := protocol.Init(cfHomeDir, protocol.WithWalkUp())
```

**After (0.30):**
```go
import "github.com/campfire-net/campfire/pkg/protocol"

// Walk-up option is gone — just call Init:
client, _, err := protocol.Init(cfHomeDir)
if err != nil { ... }
// result.Recentered, result.WalkUpPath — do not exist; delete references.
```

**.cf/config.toml before:**
```toml
[behavior]
walk_up = true
```

**.cf/config.toml after:**
```toml
# Remove the behavior.walk_up key entirely.
# (It is silently ignored in 0.30; clean it up to avoid confusion.)
```

**Rationale:** Center-finding was a substrate-level heuristic that created
a tight coupling between initialization and naming topology. In 0.30, locality
resolves at the discovery layer (L4, `cf-discovery`) — not in `Init()`. This
is a cleaner separation: the substrate handles identity and transport; the
discovery layer handles topology. See design v2 §3 and `cf-discovery-spec.md`.

**Demo:** `cf-conventions/demos/cf-discovery/` (post-0.30 port; for 0.30 itself,
center-finding simply compiles away — no replacement call needed at the Init site).

---

### BC-3 — GitHub transport removed

**What changed:**  
`pkg/transport/github` is deleted. The following no longer exist:

- `protocol.TypeGitHub` (transport type constant) — **retained as a tombstone**
  in `cf-protocol/internal/transport` so existing store rows do not panic, but
  no new campfires may use it
- CLI flags `--transport github`, `--github-repo`, `--github-token-env`,
  `--github-base-url` — removed; they return errors with migration guidance
- `cf join <github-url>` — returns error

**Before (0.19):**
```go
// Creating a GitHub-backed campfire:
client.Create(protocol.CreateRequest{
    Transport: protocol.TypeGitHub,
    GitHubRepo: "myorg/my-repo",
})

// CLI:
// cf create --transport github --github-repo myorg/my-repo
// cf join https://github.com/myorg/my-repo
```

**After (0.30):**
```go
// Migrate to filesystem or HTTP transport:
client.Create(protocol.CreateRequest{
    Transport: protocol.TypeFilesystem,
})
// or TypeHTTP for hosted/multi-machine scenarios
```

```bash
# CLI migration:
cf create --transport filesystem
# or for hosted:
cf create --transport http --url https://mcp.getcampfire.dev
```

**Existing store rows:** Any campfire row with `transport_type = "github"` will
return a `GitHubTransport` tombstone type — it does not panic, but operations
against the campfire will return a "transport removed" error. Migrate by
recreating the campfire on a supported transport.

**Rationale:** The GitHub transport introduced a hard dependency on GitHub's
REST API with no conforming transport interface and rate-limit exposure that
broke tests. The design goal (campfire-as-repository) is superseded by the
HTTP transport + hosted service model. The tombstone ensures backward
compatibility for database rows without resuming a broken transport.

**Demo:** `cf-conventions/demos/cf-discovery/` (shows creating campfires on
supported transports).

---

### BC-4 — Session shared-key form removed

**What changed:**  
The shared-key session token mechanism from `pkg/protocol/session.go` is removed.
Under the old model, `cf session create` generated a shared ephemeral private
key and embedded it in the `cfs1_` token, giving all workers the same identity.
This destroys per-worker attribution and is a security property violation.

Removed APIs:
- `JoinSession(token string)` — deleted
- Shared-key `NewSession` variant — replaced; new `NewSession` uses the
  creator's own identity key
- `cfs1_` token format — deprecated; `DecodeTokenV1` returns a migration error
  on `cfs1_` prefix

New APIs:
- `client.NewSession(ttl)` — creates a session campfire using the creator's
  identity key (not a shared ephemeral key)
- `cf-conventions/cf-session` package — lazy-mint per-worker grants

**Before (0.19):**
```go
import "github.com/campfire-net/campfire/pkg/protocol"

// Orchestrator creates a shared-key session:
sess, token, err := client.NewSession(2 * time.Hour)
if err != nil { ... }
// token is cfs1_... — shares a private key with all workers

// Worker joins using the shared key:
workerClient, workerSess, err := protocol.JoinSession(token, cfHomeDir)
```

```bash
# CLI (old):
TOKEN=$(cf session create --ttl 2h)
# Workers decode cfs1_ token to get private key
```

**After (0.30):**
```go
import (
    "github.com/campfire-net/campfire/pkg/protocol"
    cfsession "github.com/campfire-net/campfire/cf-conventions/cf-session"
)

// Orchestrator creates session campfire (uses own identity key):
sess, sessionID, err := client.NewSession(2 * time.Hour)
if err != nil { ... }
// sessionID is the campfire hex ID — workers join via admission, not shared key

// Emit session:open with capability template for lazy-mint issuance:
openMsg, err := protocol.EmitSessionOpen(client, sessionID, cfsession.CapabilityTemplate{
    Convention: "swarm-coordination",
    OpGlob:     "*",
    TTL:        2 * time.Hour,
})

// Workers: orchestrator issues per-worker grant on identify:
// IssueWorkerGrant(client, sessionID, workerPubKey, template)
// Worker key is written to jail dir; worker never holds orchestrator key.
```

```bash
# CLI (new):
TOKEN=$(cf session create --ttl 2h)
# Token is now cfs2_... with real transport config embedded
# cfs1_ tokens return migration error — regenerate with: cf session create
```

**Rationale:** Shared private keys destroy per-worker attribution, break
audit trails, and are incompatible with the delegation model (a shared key
cannot hold a scoped grant). The lazy-mint model (design v2 §2.9) gives each
worker a unique key and a parent-bounded grant with the session orchestrator
as trust anchor.

**Demo:** `cf-conventions/demos/cf-session/`

---

### BC-5 — `cfs1_` token format deprecated

**What changed:**  
Session tokens with prefix `cfs1_` are no longer decoded. `DecodeTokenV1`
returns a clear migration error. The new format is `cfs2_` — it embeds real
transport config (endpoint URL, transport type) instead of a raw private key.

**Before (0.19):**
```bash
TOKEN=$(cf session create --ttl 2h)
# Returns: cfs1_<base64-encoded-private-key>

cf session $TOKEN claim-item --item_id rd-abc --description "Fix auth"
```

**After (0.30):**
```bash
TOKEN=$(cf session create --ttl 2h)
# Returns: cfs2_<base64-encoded-transport-config>

cf session $TOKEN claim-item --item_id rd-abc --description "Fix auth"
```

**Migration:** Regenerate all tokens. `cfs1_` tokens cannot be decoded in 0.30;
scripts that stored tokens in CI or config must be regenerated. Token TTL is
unchanged (2h default, 24h maximum).

**Demo:** `cf-conventions/demos/cf-session/`

---

### BC-6 — `present_as` field removed

**What changed:**  
The `identity.present_as` key in `.cf/config.toml` is no longer applied.
Super-identity rendering (appearing as a different display name) is now handled
by `cf-identity` ceremony declarations.

**Before (0.19) `.cf/config.toml`:**
```toml
[identity]
display_name = "Baron"
present_as = "Atlas"  # appear as Atlas when joining campfires
```

**After (0.30):**
```toml
[identity]
display_name = "Baron"
# Remove present_as — it is silently ignored.
# Super-identity is handled by cf-identity ceremony:
#   cf identity introduce --as Atlas
```

**Rationale:** `present_as` was a name-only alias with no cryptographic
binding — it let an agent claim any display name without a signed ceremony.
`cf-identity` ceremonies provide identity presentation with provenance
(the introduce-me declaration is signed and verifiable).

---

### BC-7 — `tagspec` and `reserved-ops` constants moved

**What changed:**  
Tag prefix constants and reserved operation codes moved from `pkg/` to
`cf-protocol/internal/`:

- `pkg/tagspec/` → `cf-protocol/internal/tagspec/`
- `pkg/reserved_ops/` → `cf-protocol/internal/reserved-ops/`

External callers: switch to `cf-protocol/protocol` re-exports or to the
`cf-conventions/cf-convention/` re-exports.

**Before (0.19):**
```go
import "github.com/campfire-net/campfire/pkg/tagspec"

if strings.HasPrefix(msg.Tag, tagspec.CampfirePrefix) { ... }
```

**After (0.30):**
```go
import "github.com/campfire-net/campfire/cf-protocol/protocol"

if strings.HasPrefix(msg.Tag, protocol.CampfireTagPrefix) { ... }
// or import "github.com/campfire-net/campfire/cf-conventions/cf-convention"
// for the L2 re-exports: convention.DefaultDeniedTagPrefixes
```

**Available re-exports (cf-protocol/protocol):**
```go
protocol.CampfireTagPrefix            // "campfire:"
protocol.CampfireTagCompact           // "campfire:compact"
protocol.CampfireTagMemberJoined      // "campfire:member-joined"
protocol.CampfireTagMemberLeft        // "campfire:member-left"
protocol.CampfireTagMemberRoleChanged // "campfire:member-role-changed"
protocol.CampfireTagJoinRequest       // "campfire:join-request"
protocol.CampfireTagKeyDelivery       // "campfire:key-delivery"
protocol.CampfireTagRekey             // "campfire:rekey"
protocol.CampfireTagVisibilityChanged // "campfire:visibility-changed" (NEW in 0.30)
protocol.TagSessionOpen               // "session:open" (NEW in 0.30)
protocol.TagSessionClose              // "session:close" (NEW in 0.30)
```

**Demo:** `cf-conventions/demos/reserved-op-enforcer.sh`

---

### BC-8 — Reserved tags added: `campfire:visibility-changed`, `session:open`, `session:close`

**What changed:**  
Three new reserved L1 system event tags are frozen in the wire format:

| Tag | Emitted by | Meaning |
|-----|-----------|---------|
| `campfire:visibility-changed` | `Client.Admit` / `Client.Evict` | Campfire visibility (open/invite-only) transitioned |
| `session:open` | `protocol.EmitSessionOpen` | Session campfire opened; capability template attached |
| `session:close` | `protocol.EmitSessionClose` | Session campfire closed; coordination log eligible for compaction |

**Impact on consumers:**  
These tags are **reserved**. Convention declarations MUST NOT use them as
operation tags. The L2 dispatcher enforces a `DefaultDeniedTagPrefixes` denylist
that blocks these tags at the dispatch boundary — any convention declaring an
operation with a `campfire:` or `session:` tag prefix will be rejected.

**Before (0.19):**
```go
// Convention declaration could freely use campfire: prefix (not enforced):
// { "tag": "campfire:my-op" }  — technically worked in 0.19
```

**After (0.30):**
```go
// Convention declarations with campfire: or session: prefix tags
// are rejected at parse time with "denied tag prefix" error.
// Use a convention-specific prefix instead:
// { "tag": "myconv:my-op" }
```

**Code that listens for these tags:**
```go
// Check for session:open event (Message.Tags is []string):
for _, msg := range messages {
    for _, tag := range msg.Tags {
        if tag == cfprotocol.TagSessionOpen {
            // Handle session open
        }
    }
}
```

**Rationale:** System event tags must be reserved at L1 to prevent convention
authors from accidentally or maliciously hijacking them. The visibility-changed
tag enables federation consumers to track campfire membership changes.
The session lifecycle tags enable the disposable session log compaction policy.

---

### BC-9 — `GateEvaluator` interface (L2 contract, new requirement)

**What changed:**  
`cf-conventions/cf-convention` now declares the `GateEvaluator` interface at
L2. The `ConventionDispatcher` holds a `GateEvaluator` field and calls
`Evaluate` before dispatching any convention operation that carries a grant
chain. The default stub (`AllowAllGateEvaluator`) passes all requests — for
production use, wire in `cf-authority/trust.DefaultGateEvaluator`.

**This is additive for most consumers** — existing `convention.Executor` and
`convention.NewServer` callers do not need to change unless they need real
gate evaluation. The stub default means 0.19 code that does not wire a gate
evaluator continues to compile and run.

**Before (0.19):**
```go
import "github.com/campfire-net/campfire/pkg/convention"

exec := convention.NewExecutor(client)
exec = exec.WithProvenance(checker)  // v1 interface
```

**After (0.30) — minimal (keep stub evaluator):**
```go
import "github.com/campfire-net/campfire/pkg/convention"

// Unchanged — AllowAllGateEvaluator is the default.
exec := convention.NewExecutor(client)
exec = exec.WithProvenance(checker)
```

**After (0.30) — production (wire real evaluator):**
```go
import (
    "github.com/campfire-net/campfire/pkg/convention"
    "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

evaluator := trust.NewDefaultGateEvaluator(trustStore)
exec := convention.NewExecutor(client)
exec = exec.WithGateEvaluator(evaluator)
exec = exec.WithProvenanceV2(trust.NewDefaultProvenanceChecker(store))
```

**Interface contract (frozen at cf-authority 1.0):**
```go
type GateEvaluator interface {
    Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResult
}

type EvaluateResult struct {
    Decision Decision          // GateAllow | GateDeny | GateUnresolvable
    Reason   DenyReason        // set when GateDeny
    MissingMessageID string    // set when GateUnresolvable
}
```

**Rationale:** Moving gate evaluation into the L2 dispatcher rather than each
convention individually ensures that no convention can bypass trust evaluation.
The L2/L3 separation (interface at L2, implementation at L3) avoids import
cycles and allows alternative implementations via the conformance harness.

**Demo:** `cf-conventions/demos/gate-evaluator-stage2.sh`

---

### BC-10 — `ProvenanceCheckerV2` interface (new, supersedes v1)

**What changed:**  
A new `ProvenanceCheckerV2` interface is declared at L2. It supersedes the v1
`ProvenanceChecker` (which had a single `Level(key string) int` method) for
new callers.

```go
// v1 (still compiles, backward compat):
type ProvenanceChecker interface {
    Level(key string) int
}

// v2 (new — use for new code):
type ProvenanceCheckerV2 interface {
    CheckProvenance(ctx context.Context, req ProvenanceRequest) ProvenanceResult
}
```

`Executor.WithProvenance(v1)` and `Executor.WithProvenanceV2(v2)` both work;
v2 takes precedence when both are set.

**Before (0.19):**
```go
type myChecker struct{}
func (c *myChecker) Level(key string) int {
    return 1  // everyone is level 1
}
exec = exec.WithProvenance(&myChecker{})
```

**After (0.30) — v2 (recommended for new code):**
```go
type myCheckerV2 struct{}
func (c *myCheckerV2) CheckProvenance(ctx context.Context, req convention.ProvenanceRequest) convention.ProvenanceResult {
    return convention.ProvenanceResult{Level: convention.ProvenanceLevelContactable}
}
exec = exec.WithProvenanceV2(&myCheckerV2{})
```

**Level constants (L2-owned, frozen):**
```go
convention.ProvenanceLevelAnonymous    = 0  // valid keypair only
convention.ProvenanceLevelClaimed      = 1  // self-asserted identity
convention.ProvenanceLevelContactable  = 2  // challenge/response verified
convention.ProvenanceLevelPresent      = 3  // level 2 within freshness window
```

**Demo:** `cf-conventions/demos/provenance-checker-stage2.sh`

---

### BC-11 — `cf-authority` five-leaf gate predicate language (replaces `min_operator_level`)

**What changed:**  
Convention declarations in 0.19 used a simple `min_operator_level: N` field
to require a minimum provenance level from the sender. In 0.30, this field
still works at the `Executor` level (backward compatible), but the full
`cf-authority` predicate language is available for richer gate expressions.

**New predicate types:**
- `level: N` — minimum provenance level (replaces `min_operator_level`)
- `grant: <conv>:<op>` — exact capability check
- `grant_in: <conv>:<op-glob> at <where>` — scoped capability check
- `grant_quota: <axis> >= <bound>` — quota/rate bound check
- `chain_to: <pubkey>` — trust anchor check (by key, not name)
- `chain_to_quorum: M-of-N` — multi-sysop authority
- `all_of: [...]` — AND composition
- `any_of: [...]` — OR composition

**Before (0.19) — declaration JSON:**
```json
{
  "convention": "work",
  "operation": "close",
  "min_operator_level": 1,
  "tag": "work:close"
}
```

**After (0.30) — declaration JSON (backward compatible; min_operator_level still works):**
```json
{
  "convention": "work",
  "operation": "close",
  "min_operator_level": 1,
  "tag": "work:close"
}
```

**After (0.30) — declaration JSON (full predicate language):**
```json
{
  "convention": "work",
  "operation": "close",
  "gate": {
    "kind": "all_of",
    "children": [
      { "kind": "level", "n": 1 },
      { "kind": "grant", "convention": "work", "op": "close" }
    ]
  },
  "tag": "work:close"
}
```

**Key constraint: no `not:` predicate.** The predicate language deliberately
omits complement predicates. If you need to deny a class of senders, use
`OwnerPolicy.BlanketDeny` or the reserved-op floor enforcement.

**Maximum AST nesting depth: 3.** Predicates with depth > 3 are rejected at
parse time.

**Rationale:** `min_operator_level` addressed only one axis of authority
(provenance level). The five-leaf predicate language expresses grant-based
authority, quota limits, and multi-sysop trust without requiring separate
`GateEvaluator` implementations per convention. It is a declarative gate
language — not a full policy language.

**Demo:** `cf-conventions/demos/cf-authority/`

---

### BC-12 — `cf-discovery` 3-tier model + multi-level chain (replaces filesystem walk)

**What changed:**  
The discovery layer is now `cf-discovery`, a full L3 convention package. Key
concepts:

- **3-tier model:** Tier 1 (beacon/snippet), Tier 2 (name registration + TOFU
  pin), Tier 3 (full joining + post-join verification)
- **`ResolveChain`:** Multi-level chain walks (e.g. `baron.dontguess` →
  `dontguess` root → then `baron`) without needing to join intermediate
  namespaces
- **Snippet schema:** `naming:preview` messages carry signed snippets (name,
  description, member\_count\_bucket, freshness\_window, parent\_signature)
- **Post-join verification:** probe-write-then-observe confirms the campfire
  is joinable and writable before advertising it as a valid endpoint
- **Config-over-beacon endpoint precedence:** `~/.cf/apps/<appname>/config.toml`
  overrides beacon transport config (§12 of cf-discovery-spec.md)

**Before (0.19):**
```go
// No standardized discovery API — consumers either used
// hard-coded campfire IDs, manual beacon parsing, or
// protocol.Init walk-up.
client, _, err := protocol.Init(cfHomeDir, protocol.WithWalkUp())
```

**After (0.30):**
```go
import "github.com/campfire-net/campfire/cf-conventions/cf-discovery"

// Tier 1 — validate and sign a snippet:
snippet, err := cfdiscovery.NewWithExpiry("my-child", "Description", "1", 5*time.Minute)
signed, err := cfdiscovery.SignDeclarationWithExpiry(snippet, parentKey, expiry)

// Tier 2 — resolve a name with TOFU pinning:
resolver := cfdiscovery.NewResolver(client)
campfireID, err := resolver.Resolve("baron.dontguess")
// ErrInviteOnly — campfire requires admission (not a resolver error)
// ErrPostJoinVerificationFailed — campfire failed probe-write verification

// Multi-level chain:
chain, err := resolver.ResolveChain("rd.ready.3dl")
// chain contains per-hop snippets with composed freshness windows
```

**Snippet freshness composition rule:** Multi-hop chains use the **minimum**
freshness window across all hops. A hostile intermediate cannot extend the
apparent freshness by declaring a long window.

**Sentinel errors:**
```go
cfdiscovery.ErrInviteOnly                // campfire is invite-only
cfdiscovery.ErrPostJoinVerificationFailed // probe write did not propagate
```

**Rationale:** Center-finding in `Init()` was fragile (depended on filesystem
layout), not network-aware, and had no snippet signing for tamper detection.
The 3-tier model explicitly separates browsing (Tier 1), resolving (Tier 2),
and joining (Tier 3) — matching how agents actually navigate namespaces.

**Demo:** `cf-conventions/demos/cf-discovery/`

---

### BC-13 — `cf-session` lazy-mint (replaces `cf session create --shared-key`)

See BC-4 for the full migration. Summary of new session orchestration API:

```go
import (
    "github.com/campfire-net/campfire/pkg/protocol"
    cfsession "github.com/campfire-net/campfire/cf-conventions/cf-session"
)

// Open session:
sess, sessionID, err := client.NewSession(2 * time.Hour)
capTemplate := cfsession.CapabilityTemplate{
    Convention: "swarm-coordination",
    OpGlob:     "*",
    TTL:        2 * time.Hour,
}
_, err = cfsession.OpenSession(client, sessionID, capTemplate)

// Issue per-worker grant when worker identifies itself:
workerPubKey := // worker's fresh Ed25519 public key
grant, err := cfsession.IssueWorkerGrant(client, sessionID, workerPubKey, capTemplate)

// Materialize worker identity (jail mode — default):
workerKeyPath, err := cfsession.MaterializeWorkerIdentity(jailDir, workerPubKey, grant)

// Close session:
_, err = cfsession.CloseSession(client, sessionID)
```

**Demo:** `cf-conventions/demos/cf-session/`

---

### BC-14 — `cf-primitives` binary (frozen surface, separate from `cf`)

**What changed:**  
A new `cf-primitives` binary exposes exactly the 12 frozen protocol commands:
`admit`, `await`, `create`, `disband`, `evict`, `init`, `join`, `leave`,
`members`, `read`, `send`, `subscribe`. This surface is ceiling-enforced by
`TestPrimitivesSurfaceCeiling` in CI — additions require adversary review.

The `cf` binary still exposes all commands but hides the protocol-primitive
commands from `cf --help`. Use `cf --help-primitives` to see them.

**Migration impact:** Scripts that used `cf send` / `cf read` directly continue
to work. If you need to guarantee you are using the frozen surface only (e.g.
for interop testing), use `cf-primitives` instead of `cf`.

```bash
# Before (0.19) — cf send is the only way:
cf send $CAMPFIRE_ID --tag work:create --body '{"title":"..."}'

# After (0.30) — both work; cf-primitives is the frozen surface:
cf-primitives send $CAMPFIRE_ID --tag work:create --body '{"title":"..."}'
cf send $CAMPFIRE_ID --tag work:create --body '{"title":"..."}'   # still works
```

---

### BC-15 — argv[0] dispatch + per-app config overlay

**What changed:**  
The `cf` binary detects invocation under a non-`cf` name (symlink e.g.
`social → cf`) and calls `Multicall(safeName, args)` — treating the binary
name as the campfire namespace prefix. Per-app config overlay:
`~/.cf/apps/<appname>/config.toml` is inserted as the highest-priority
global-tier config layer.

**Migration impact for consumer apps that distribute a `cf` binary wrapper:**
Consider distributing a symlink to `cf` instead of a wrapper script. Each
symlinked app gets its own identity, transport defaults, and naming seeds
without touching the global config.

```bash
# Before (0.19) — wrapper script:
#!/bin/bash
exec cf --config ~/.cf/apps/dontguess/config.toml "$@"

# After (0.30) — symlink:
ln -s $(which cf) ~/.local/bin/dontguess
# ~/.cf/apps/dontguess/config.toml is auto-loaded when invoked as "dontguess"
```

**Config cascade (all sources, highest to lowest priority):**
```
1. CLI flags
2. Project .cf/config.toml
3. ~/.cf/apps/<appname>/config.toml   ← NEW (app overlay, global-tier)
4. ~/.cf/config.toml
5. Compiled defaults
```

**Security:** `SanitizeBinaryName` strips shell metacharacters from the binary
name before it reaches dispatch. Path traversal and injection attempts are
rejected before any campfire name is reached.

**Demo:** `docs/demos/section12-config-over-beacon.sh`

---

### BC-16 — `BridgeOptions.Forward` (new field, pass-through mode)

**What changed:**  
`protocol.BridgeOptions` has a new `Forward bool` field. When `true`, the bridge
writes the original message envelope to the destination transport unchanged and
appends exactly one provenance hop signed by the bridging campfire with
`Role = campfire.RoleBlindRelay`. Original-author cryptographic attribution
survives end-to-end.

**Before (0.19):**
```go
// BridgeOptions had no Forward field.
// Bridge always re-published (fresh message ID, bridging agent as sender).
err := protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
    Bidirectional: true,
    TagFilter:     []string{"work:"},
})
```

**After (0.30) — re-publish (default, same behavior as 0.19):**
```go
err := protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
    Bidirectional: true,
    TagFilter:     []string{"work:"},
    // Forward: false (default) — re-publish mode, same as 0.19
})
```

**After (0.30) — pass-through (new, preserves original attribution):**
```go
err := protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
    Bidirectional: true,
    Forward: true,  // append blind-relay hop; preserve original Sender, Signature, Payload
})
```

**When to use `Forward: true`:**
- Hosted-reader (Tier 3.5) scenarios where readers need to verify original-author
  signatures
- Multi-region mirroring
- Any cf→cf relay where original-author trust must reach the receiver

**Rationale:** Without `Forward`, a bridge is an attribution sink — all
bridged messages appear to come from the bridge agent. For hosted scenarios
where reader clients need to verify original-author signatures (e.g. verifying
a `delegation:grant` from a known pubkey), the relay hop must preserve the
original envelope.

---

### BC-17 — TOFU pin HMAC derivation changed (sign-fixed-input)

**What changed:**  
The HMAC key used to integrity-protect the trust pin store (`~/.cf/trust/pins.json`)
is now derived via:

```
HMAC_KEY = SHA-256(ed25519.Sign(fullPrivKey, "campfire-trust-pins-v1"))
```

The previous derivation was:
```
HMAC_KEY = SHA-256("campfire-trust-pins" || privKey[:32])
```

The new derivation uses a **sign-fixed-input** pattern: the Ed25519 signature
of the fixed string `"campfire-trust-pins-v1"` is deterministic (Ed25519 is
RFC 8032 deterministic), non-guessable from the public key alone, and changes
if the private key changes. This eliminates the previous approach which used
only the first 32 bytes of the private key (the seed) without involving the
Ed25519 signing function.

**Migration:** The pin file format is unchanged (JSON with `pins` + `hmac` fields).
The HMAC itself changes. On first load with 0.30, HMAC verification will fail
if the file was written by 0.19.

```
# If you see "HMAC verification failed: pin file may be tampered"
# on startup after upgrading, the file was written by 0.19.
# Safe to remove and re-pin:
rm ~/.cf/trust/pins.json
# Re-run cf trust pin to re-establish your pins.
```

**New `cf trust pin/unpin/list/prune` commands:**
```bash
cf trust pin <campfire-id> <pubkey>      # pin Ed25519 key for a campfire
cf trust unpin <campfire-id>             # remove pin
cf trust list                            # list all pins with metadata
cf trust prune                           # remove pins for disbanded/left campfires
```

**Rationale:** Using `privKey[:32]` (the seed bytes) without involving the
signing function meant the HMAC key was derivable from any code that had the
seed — including code that only had read access to the key material. The
sign-fixed-input pattern requires calling the signing function, which is a
stronger binding to the actual signing identity and is the standard pattern
for deriving symmetric secrets from asymmetric keys.

**Demo:** `cf-conventions/demos/cf-authority-spec-walkthrough.sh` (covers
trust pin management).

---

### BC-18 — `GrantPayload` CBOR field 5 added (security critical)

**What changed:**  
`GrantPayload` has a new `omitempty` CBOR field 5: `GranterPubKey []byte`
(Ed25519, 32 bytes). The `DefaultGateEvaluator` asserts
`lastHop.GranterPubKey == RootPrincipal`, closing a trust-anchor bypass.

**Before (0.19):** `walkChain` never verified the chain terminates at
`RootPrincipal`. Any rogue self-signed chain could receive `Allow`.

**After (0.30):**
```go
// When building GrantPayload manually (rare — most callers use cf-authority APIs):
payload := trust.GrantPayload{
    ParentGrantID: parentID,
    ChildPubKey:   childKey,
    Capabilities:  caps,
    Depth:         1,
    GranterPubKey: granterPubKey,  // FIELD 5 — required at last hop
}
```

**Named struct literal callers are unaffected.** Only code that builds
`GrantPayload` by positional field (e.g. in a test fixture or a custom
chain-builder) must add the fifth field.

**Wire impact:** Grant messages written before 0.30 that lack field 5
will parse with `GranterPubKey = nil` (omitempty). The `DefaultGateEvaluator`
will return `Deny / DenyReservedOpFloor` for chains that lack `GranterPubKey`
at the last hop. This is intentional — field 5 is the security fix.

**Rationale:** CVE-class trust-anchor bypass (CRITICAL). Without verifying
the chain root matches `RootPrincipal`, an attacker could construct a
self-signed chain that passes all depth, scope, and expiry checks but is
not rooted in the expected owner key. Field 5 is the wire-level anchor
that makes chain-root verification possible.

---

## Part II — Per-Consumer Migration Plans

These are **plans for future port windows**, not patches applied today.
Each consumer remains on 0.19.x until its port window opens.

---

### Consumer: `rd` (0.19.2 → 0.30)

**Repository:** `~/projects/ready/`  
**Current version:** `github.com/campfire-net/campfire v0.19.2`

**go.mod change:**
```diff
-require github.com/campfire-net/campfire v0.19.2
+require github.com/campfire-net/campfire v0.30.0
```

**File-by-file diff plan:**

#### `cmd/migrate/ready-import/main.go`

```diff
-client, _, err := protocol.Init(cfHomeDir, protocol.WithNoWalkUp())
+client, _, err := protocol.Init(cfHomeDir)
+// WithNoWalkUp() is removed — Init no longer walks up.
```

#### `cmd/rd/root.go`

```diff
 import (
-    "github.com/campfire-net/campfire/pkg/convention"
-    "github.com/campfire-net/campfire/pkg/protocol"
+    "github.com/campfire-net/campfire/pkg/convention"   // unchanged path
+    "github.com/campfire-net/campfire/pkg/protocol"     // unchanged path (forwarding shim)
 )
```

The `pkg/protocol` and `pkg/convention` import paths are unchanged — they
are forwarding shims. No import rewrite needed for rd.

**ProvenanceChecker wiring (root.go):**
```go
// BEFORE: v1 ProvenanceChecker — still works in 0.30 (backward compat)
checker, err := provenance.NewStoreChecker(s, campfireID, creatorKey)
exec = exec.WithProvenance(checker)

// RECOMMENDED (0.30): upgrade to v2 when rd's port opens
// (no functional change required for 0.30 — v1 still works)
```

**min\_operator\_level declarations:**  
rd's convention declarations use `min_operator_level` — this still works in 0.30
(backward compat). No changes needed in declaration JSON files.

**Optional upgrade — add GateEvaluator for real delegation support:**
```go
// Add to requireExecutor() when port window opens:
import (
    "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)
evaluator := trust.NewDefaultGateEvaluator(trustStore)
exec = exec.WithGateEvaluator(evaluator)
```

**Change count: 1 required** (`WithNoWalkUp()` removal) **+ 1 optional**
(v2 ProvenanceChecker) **+ 1 optional** (GateEvaluator).

**Test strategy:**
1. Remove `WithNoWalkUp()` call, confirm `go build ./...` passes
2. Run `go test ./...` — baseline must be green before and after
3. Smoke test: `rd ready` on a test campfire

---

### Consumer: `dontguess` (0.17.0 → 0.30)

**Repository:** `~/projects/dontguess/`  
**Current version:** `github.com/campfire-net/campfire v0.17.0`

**go.mod change:**
```diff
-require github.com/campfire-net/campfire v0.17.0
+require github.com/campfire-net/campfire v0.30.0
```

**Import paths used by dontguess:**
- `pkg/protocol` — unchanged (forwarding shim)
- `pkg/convention` — unchanged (forwarding shim)

No import rewrites needed.

**Convention API migration:**
```go
// BEFORE (0.17): pkg/convention.NewExecutor
exec := convention.NewExecutor(client)

// AFTER (0.30): unchanged — pkg/convention.NewExecutor is the forwarding shim
exec := convention.NewExecutor(client)
```

**Optional: wire GateEvaluator for delegation support:**
```go
// dontguess/pkg/exchange/init.go — add when port opens:
import "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"

evaluator := trust.NewDefaultGateEvaluator(trustStore)
exec = exec.WithGateEvaluator(evaluator)
exec = exec.WithProvenanceV2(trust.NewDefaultProvenanceChecker(store))
```

**Change count: 0 required** (just the go.mod bump) **+ 2 optional**
(GateEvaluator + ProvenanceCheckerV2).

**Note for dontguess federation work (post-0.30.x):** The 3-tier discovery
model (`cf-discovery`) is the integration test for the topography bet. When
the dontguess federation port opens, the Resolver and ResolveChain APIs will
be the namespace walk mechanism. See `cf-discovery-spec.md §3` for the
multi-level chain composition rules.

---

### Consumer: `social` (0.19.2 → 0.30)

**Repository:** `~/projects/social/`  
**Current version:** `github.com/campfire-net/campfire v0.19.2`

**go.mod change:**
```diff
-require github.com/campfire-net/campfire v0.19.2
+require github.com/campfire-net/campfire v0.30.0
```

**Files using protocol/convention:**
- `cmd/index-agent/convention_handler.go` — `pkg/convention`
- `cmd/index-agent/multi_subscriber.go` — `pkg/protocol`
- `cmd/index-agent/subscriber.go` — `pkg/protocol`
- `cmd/index-agent/rebuild.go` — `pkg/protocol`
- `cmd/social/init.go` — `pkg/protocol`
- `pkg/cfadapter/adapter.go` — `pkg/protocol`
- `cmd/index-agent/main.go` — `pkg/protocol`

All imports are `pkg/protocol` / `pkg/convention` — unchanged forwarding shim
paths. No import rewrites needed.

**Check for `WithWalkUp` / `WithNoWalkUp` usage:**
```bash
grep -rn "WithWalkUp\|WithNoWalkUp\|present_as" ~/projects/social/ --include="*.go"
```
If none found, the go.mod bump is the only required change.

**Social connect convention migration:**  
`cf-conventions/cf-convention-extensions/connect/` has moved to
`cf-conventions/cf-connect/`. If social imports this package directly:
```diff
-import "github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/connect"
+import "github.com/campfire-net/campfire/cf-conventions/cf-connect"
```

**Change count: 1 required** (go.mod bump) **+ 1 conditional** (connect
package import if used directly).

---

### Consumer: `the reach` / galtrader (0.13.1 → 0.30)

**Repository:** `~/projects/galtrader/`  
**Current version:** `github.com/campfire-net/campfire v0.13.1`

**Note:** galtrader is on 0.13.1 — a larger version gap than the other consumers.
This means it will also pick up 0.14–0.19 changes. Review the CHANGELOG for
those versions as well. Key changes from 0.13 onward are included below for
completeness.

**go.mod change:**
```diff
-require github.com/campfire-net/campfire v0.13.1
+require github.com/campfire-net/campfire v0.30.0
```

**Primary campfire usage:**
- `pkg/server/campfire_sdk.go` — `pkg/protocol`
- Internal `thereach/pkg/protocol` import suggests galtrader has its own
  protocol abstraction layer — the campfire SDK is used only in campfire\_sdk.go

**Session migration (if galtrader uses sessions):**  
Check `pkg/server/session.go` — if it uses `JoinSession` or `cfs1_` tokens:
```diff
-workerClient, workerSess, err := protocol.JoinSession(token, cfHomeDir)
+// Use cf-session lazy-mint (BC-4 / BC-13 above)
+sess, sessionID, err := client.NewSession(2 * time.Hour)
+_, err = cfsession.OpenSession(client, sessionID, template)
```

**Walk-up removal:**  
```diff
-client, _, err := protocol.Init(cfHomeDir, protocol.WithWalkUp())
-if result.Recentered { ... }
+client, _, err := protocol.Init(cfHomeDir)
```

**Change count: 2–4 required** (go.mod + session migration + WalkUp removal +
any direct substrate imports if present).

**Recommended approach:** Run `go build ./...` after the go.mod bump to surface
all compilation errors, then fix each one using the snippets above.

---

### Consumer: `freeso` (0.13.0 → 0.30)

**Repository:** `~/projects/freeso-experiment/FreeSO/TSOClient/FSO.Bot.Sidecar/`  
**Current version:** `github.com/campfire-net/campfire v0.13.0`

**Note:** freeso is a sidecar — it bridges the FreeSO game server to a campfire
namespace. It uses campfire primarily for coordination message passing, not
convention dispatch.

**go.mod change:**
```diff
-require github.com/campfire-net/campfire v0.13.0
+require github.com/campfire-net/campfire v0.30.0
```

**Likely usage pattern (coordination only — no convention dispatch):**
```go
// init:
client, _, err := protocol.Init(cfHomeDir)
// send:
_, err = client.Send(protocol.SendRequest{...})
// read:
result, err = client.Read(protocol.ReadRequest{...})
```

This usage is unchanged in 0.30. The SDK-level `Client` API (Init, Send, Read,
Await, Subscribe, Members) is stable across 0.19 → 0.30.

**Session check:**  
If freeso uses `JoinSession` for worker dispatch (game bot workers), migrate
to the `cf-session` lazy-mint pattern (BC-13).

**GitHub transport check:**  
If freeso's campfire was created with `--transport github`, it must be
migrated to a filesystem or HTTP campfire (BC-3).

**Change count: 1 required** (go.mod bump) **+ 1–2 conditional** (session
migration, GitHub transport migration if applicable).

---

## Part III — Decision Tree

Use this decision tree to determine the required changes for your consumer:

```
1. Does your consumer import any of these directly?
   pkg/campfire, pkg/message, pkg/store, pkg/transport/fs,
   pkg/transport/http, pkg/threshold, pkg/projection,
   pkg/predicate, pkg/crypto, pkg/encoding, pkg/admission
   → YES: Replace with cf-protocol/protocol equivalents (BC-1)
   → NO: skip

2. Does your consumer call WithWalkUp(), WithNoWalkUp(), or
   read InitResult.WalkUpPath / .Recentered / .DelegationIssued?
   → YES: Remove the call; call protocol.Init() directly (BC-2)
   → NO: skip

3. Does your consumer create campfires with transport = github?
   → YES: Migrate to filesystem or HTTP transport (BC-3)
   → NO: skip

4. Does your consumer call JoinSession() or decode cfs1_ tokens?
   → YES: Migrate to cf-session lazy-mint (BC-4 + BC-13)
   → NO: skip

5. Does your consumer use present_as in .cf/config.toml?
   → YES: Remove the key; use cf-identity ceremony (BC-6)
   → NO: skip

6. Does your consumer import pkg/tagspec or pkg/reserved_ops directly?
   → YES: Switch to cf-protocol/protocol or cf-convention re-exports (BC-7)
   → NO: skip

7. Does your convention declaration use campfire: or session: prefix tags?
   → YES: Rename to a convention-specific prefix (BC-8)
   → NO: skip

8. Does your consumer need per-delegation trust evaluation?
   → YES: Wire DefaultGateEvaluator (BC-9)
   → NO: AllowAllGateEvaluator default is fine; skip

9. Does your consumer import cf-convention-extensions/connect/?
   → YES: Update import to cf-conventions/cf-connect (BC-1 package table)
   → NO: skip
```

---

## Part IV — Common Patterns After Migration

### Initializing a client (unchanged)

```go
import "github.com/campfire-net/campfire/pkg/protocol"

client, _, err := protocol.Init(cfHomeDir)
if err != nil {
    return fmt.Errorf("protocol.Init: %w", err)
}
defer client.Close()
```

### Creating a convention executor with provenance (v1 — unchanged)

```go
import (
    "github.com/campfire-net/campfire/pkg/convention"
    "github.com/campfire-net/campfire/pkg/protocol"
)

exec := convention.NewExecutor(client)
exec = exec.WithProvenance(myProvenanceChecker) // v1 still works
```

### Creating a convention executor with full gate evaluation (v2 — new)

```go
import (
    "github.com/campfire-net/campfire/pkg/convention"
    "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

evaluator := trust.NewDefaultGateEvaluator(trustStore)
provChecker := trust.NewDefaultProvenanceChecker(store)

exec := convention.NewExecutor(client)
exec = exec.WithGateEvaluator(evaluator)
exec = exec.WithProvenanceV2(provChecker)
```

### Listening for system events

```go
import "github.com/campfire-net/campfire/cf-protocol/protocol"

sub, err := client.Subscribe(ctx, protocol.SubscribeRequest{CampfireID: id})
for msg := range sub.Messages() {
    // Message.Tags is []string — check each tag.
    for _, tag := range msg.Tags {
        switch tag {
        case protocol.TagSessionOpen:
            // new session opened
        case protocol.TagSessionClose:
            // session closed; coordination log eligible for compaction
        case protocol.CampfireTagVisibilityChanged:
            // campfire visibility transitioned (open ↔ invite-only)
        }
    }
}
```

### Session management (new pattern)

```go
import (
    "github.com/campfire-net/campfire/pkg/protocol"
    cfsession "github.com/campfire-net/campfire/cf-conventions/cf-session"
)

// Orchestrator:
sess, sessionID, err := client.NewSession(2 * time.Hour)
template := cfsession.CapabilityTemplate{
    Convention: "swarm-coordination",
    OpGlob: "*",
    TTL: 2 * time.Hour,
}
_, err = cfsession.OpenSession(client, sessionID, template)

// Per-worker grant (when worker identifies):
grant, err := cfsession.IssueWorkerGrant(client, sessionID, workerPubKey, template)
workerKeyPath, err := cfsession.MaterializeWorkerIdentity(jailDir, workerPubKey, grant)

// Close:
_, err = cfsession.CloseSession(client, sessionID)
```

### Trust pin management (new CLI)

```bash
# Pin the current key for a campfire:
cf trust pin $CAMPFIRE_ID $PUBKEY_HEX

# List all pins:
cf trust list

# Prune pins for disbanded/left campfires:
cf trust prune

# Wire-based: cf trust pin/unpin/list/prune
```

---

## Part V — Wire-Format Compatibility Notes

The 0.30 wire format is frozen. Key compatibility invariants:

1. **`GrantPayload` CBOR field 5 (`GranterPubKey`):** New `omitempty` field.
   Messages without field 5 parse cleanly (omitempty). Evaluators that enforce
   field 5 will deny chains that lack it — this is intentional for the security fix.

2. **`cfs1_` tokens:** Not decodable in 0.30. Regenerate with `cf session create`.

3. **`TypeGitHub` transport:** Retained as tombstone in store rows. Operations
   against GitHub-transport campfires return errors, not panics.

4. **Reserved tags:** `campfire:visibility-changed`, `session:open`,
   `session:close` are now frozen L1 system events. Do not use these tag
   strings in convention declarations.

5. **Wire-format freeze verifier:** `wireverify_test.go` in `cf-protocol/`
   asserts CBOR field IDs, mandatory fields, and enum values for all L1 types
   in CI on every PR. A failing verifier test blocks merge.

6. **`cf-authority` wire freeze:** The `Capability`, `GrantPayload`,
   `WhereMatcher`, `PredicateAST`, and `DenyReason` CBOR types are frozen at
   cf-authority 1.0. Separate freeze verifier in `cf-conventions/cf-authority/`.

---

## Part VI — Compatibility Floor Summary

| Module | Floor | What it means |
|--------|-------|---------------|
| `cf-protocol` v0.19 → v0.30 | cf-protocol v0.30 | Substrate reorganized; wire format frozen |
| `cf-conventions` v1.0 | cf-protocol >= v0.19 (floor.txt) | L2/L3 convention machinery; cf-authority 1.0 |

**Consumers pin both at major; minor floats.** Within a major, all minors are
backward-compatible. MVS picks the highest-minimum across a multi-consumer
binary; that selection is always safe.

---

## Quick Reference — Command-Line Changes

```bash
# Session tokens — regenerate (cfs1_ → cfs2_):
cf session create --ttl 2h

# Trust pins — new commands:
cf trust pin <campfire-id> <pubkey>
cf trust unpin <campfire-id>
cf trust list
cf trust prune

# Approval flow — new:
cf approve <grant-request-msg-id> [--persist 7d]

# Init policy presets — new:
cf init --policy personal-developer    # solo, depth 1, 7d TTL
cf init --policy team-member           # multi-owner, depth 2, 24h auto-grants
cf init --policy public-agent          # hosted MCP posture, depth 1, 24h TTL

# Primitive surface (frozen):
cf-primitives send|read|await|...      # frozen 12-command surface only

# Legacy flag behavior:
cf --help-primitives                   # show hidden primitives
# cf --help no longer shows: send, read, await, inspect, compact, dm,
# bridge, filter, sync, nat-poll, serve, dag, provenance
```

---

*Document version: cf 0.30.0 — frozen.*  
*Source: CHANGELOG.md v0.30.0; design v2 §7, §10.5; cf-authority-spec.md;*  
*cf-discovery-spec.md; cf-conventions/COMPATIBILITY.md.*
