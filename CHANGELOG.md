# Changelog

## v0.31.1 — storage-scaling sweep cleanup + race-clean test suite (2026-05-17)

Patch release. Closes 7 sweep findings filed against v0.31.0 storage-scaling
work plus 3 pre-existing data races surfaced by the integration gate's
`go test ./... -race` run. Wire format unchanged, public API unchanged.

### Bug fixes

- **`verify()` rng injection** (`campfireagent-01f`, #586): The migration
  spot-check at `fs/migrate_store.go:479` called the global `rand.Shuffle`
  instead of the injected `*rand.Rand` when `rng != nil`. Seeded
  reproducibility was silently broken. Tests now assert sampled indices
  match a known-seed deterministic shuffle.

- **`cf migrate-store` membership check** (`campfireagent-6a9`, #587,
  security MEDIUM): `migrateStoreCmd` performed no `store.GetMembership`
  check before mutating on-disk layout. Mirrors the `checkRoleCanSend`
  pattern from `cf compact`. New `--force` flag for disaster-recovery
  bypass.

- **Compact orphan-files durability** (`campfireagent-6d27`, #588): When
  `os.RemoveAll` failed for a fully-covered bucket, the per-file fallback
  skipped that bucket, leaving superseded `.cbor` files on disk
  indefinitely. Now tracks `successfullyRemovedBuckets` separately;
  per-file deletes run for any failed RemoveAll. Matters on Windows/NFS
  where RemoveAll can abort before touching individual files.

### Windows

- **`cf migrate-store` startup warning on Windows** (`campfireagent-22d`,
  #592 + #593): The migration lock is a documented no-op on Windows
  (`lock_windows.go`). `cf migrate-store` now prints a stderr warning
  on Windows so operators know to stop other cf processes first. See
  `docs/design/0.31-storage-scaling.md` Appendix C. LockFileEx deferred
  pending a named Windows consumer.

### Refactoring (no behavior change)

- **`execCompactPersistent` split** (`campfireagent-016`, #590): 300-LOC
  god function in `cmd/cf/cmd/compact.go` decomposed into 5 named helpers
  (`selectMessagesToSupersede`, `writeCompactAuditEvent`, `buildBucketIndex`,
  `classifyFullyCoveredBuckets`, `deleteSupersededFiles`). Single
  authoritative bucket walk. `successfullyRemovedBuckets` semantics
  preserved byte-identically.

- **Named constants** (`campfireagent-95965`, #589): magic numbers `19`
  (nanos timestamp width) and `64` (Ed25519 hex chars) replaced with
  `NanosWidth` / `CampfireIDHexLen` in `cf-protocol/internal/transport/fs/`.

- **Dead-code removal** (`campfireagent-96a`, #591): unused `nanosRE`
  (regexp) in `fs/migrate_store.go` and `messageRecordFromWire` in
  `cmd/cf/cmd/compact.go`.

### Test-suite race fixes (`campfireagent-3f1e`, #594)

The v0.31.0 release shipped with three pre-existing data races (confirmed
at baseline `93640a1d`, before any v0.31 work) that the integration gate's
`-race` run surfaced. Fixed together — all three are test-helper issues
that block `-race`-mode CI:

- `pkg/ratelimit.mockBalanceChecker.err` — `sync.Mutex` + `setErr()`
- `pkg/metering.newForgeServer` — `eventCollector{Mutex; events}` type
  replaces the `&[]UsageEvent` pattern
- `cf-protocol/internal/transport/http.{httpClient, pollTransport}` —
  `sync.RWMutex` + `getHTTPClient()`/`getPollTransport()` hot-path getters;
  prevents racing `OverrideHTTPClientForTest` against in-flight
  `forwardMessage` goroutines from prior tests

`go test ./... -count=1 -race` now green on all 63 packages.

## v0.31.0 — fs storage scaling: bucketed messages + persistent-campfire compact (2026-05-16)

Fixes the latency wall named consumers hit when a single campfire accumulates
tens of thousands of messages in a flat `messages/` directory (ext4 directory
operations degrade past ~10k entries). Reported by `freeso-experiment` after
24h soak runs: ~20k flat CBOR files → 8–10s round-trip on every
`cf <cf-id> <op>` call, with server-side work <100ms.

**Wire format unchanged.** CBOR field IDs remain frozen at v0.30. Public API
(`Client.Send`, `Client.Read`, `Client.Subscribe`, `Client.Await`) unchanged
in shape and semantics.

### What v0.31 ships

- **Bucketed on-disk message storage** in `cf-protocol/internal/transport/fs/`
  (`campfireagent-4fa`): `WriteMessage` writes to
  `messages/<YYYY-MM>/<DD>/<19-nanos>-<message-id>.cbor`. `ListMessages`
  walks the bucketed layout (and dual-reads the legacy flat layout for one
  transitional release). `LOCK_SH` per write coexists with the migration
  tool's `LOCK_EX`. See `docs/design/0.31-storage-scaling.md` §1 + §3.4.

- **`cf migrate-store <campfire-id>` CLI** (`campfireagent-54c`): one-shot
  live-safe migration from flat v0.19/v0.30 layout to bucketed v0.31 layout.
  Lockfile-coordinated, two-rename atomic swap, byte-identical copy with
  count + spot-check verification, 5-state crash recovery. `members/`,
  `push-subscribers/`, `campfire.cbor` explicitly untouched. See §3.

- **`cf compact <campfire-id> --before <RFC3339> | --keep-last <N>`** for
  persistent campfires (`campfireagent-7d3a`): operator-driven compaction
  that emits the existing signed `campfire:compact` event (no new event
  type) and removes superseded `.cbor` files under `retention=discard`.
  Whole-bucket `os.RemoveAll` for fully-covered day buckets makes
  `--before <day-midnight>` O(buckets) instead of O(messages). Audit event
  is itself never compacted (behavioral invariant; no special flag). See §2.

### Migration guide (for v0.19.x → v0.31)

1. Build/install the v0.31 `cf` binary.
2. Per campfire dir on disk:
   ```
   cf migrate-store <campfire-id>
   ```
   - Idempotent (re-runs are no-ops once layout is bucketed).
   - `--dry-run` prints the plan without mutating.
   - Default keeps `messages.old/` as a backup until you run `--finalize`.
3. (Recommended for long-running campfires) trim history:
   ```
   cf compact <campfire-id> --keep-last 1000
   ```
   The `campfire:compact` event becomes the signed, append-only audit
   record of what was superseded.

### Security fix bundled (campfireagent-3d0, CRITICAL)

`msg.ID` is now validated as an RFC 4122 UUID at both the fs transport
(`WriteMessage`) and the protocol layer (`forwardMessage` ingress). Prior
behavior accepted arbitrary `msg.ID` strings and constructed file paths via
`filepath.Join` — a signed remote message with `ID="../../tmp/x"` could
write CBOR outside the campfire's bucket directory. This was a pre-existing
flaw in the flat layout; v0.31 closes it as part of the fs-transport
overhaul. `cf migrate-store <arg>` argument is similarly validated.

### Durability

`cf migrate-store` now `Sync`s every copied file, fsyncs the staged
`messages.new/` bucket dirs, and fsyncs the campfire directory after the
atomic-swap rename so the new layout survives a power loss between copy and
swap.

### Verified end-to-end

`scripts/demo-0.31-storage-scaling.sh` generates 50,000 messages, runs
`cf migrate-store`, validates 100/100 byte-identical CBOR samples
(`messages.old/` vs bucketed), runs `cf compact --keep-last 1000`, and
measures p50 latency at each stage. On HDD-class storage: pre-compact
8.7s p50 (matches freeso's reported pain), post-compact 2.2s p50 (4x
improvement, hard-deadline pass). The latency win is bucketing + compact
together; freeso's pattern of sustained 1Hz writes will concentrate in a
small number of day buckets that compact can `os.RemoveAll`.

### Forward-going

- The dual-read flat-layout branch in `ListMessages` is transitional;
  scheduled for removal in v0.32 once all known stores have migrated.
- `cf health` (a bloat indicator that surfaces compact recommendations) and
  filesystem fast-wake for `Await` are deferred from this release.

### Documentation correction (cf-authority adoption)

`cf-conventions/cf-authority/README.md` previously referenced
`trust.NewDefaultGateEvaluator(trustStore)` and
`trust.NewDefaultProvenanceChecker` as the primary adoption surface. Neither
symbol exists in v0.31 — they were aspirational. The README has been corrected
to document the actual wiring:

```go
adapter := trust.NewConventionAdapter()
dispatcher := convention.NewConventionDispatcher(store, logger)
dispatcher.SetGateEvaluator(adapter)
```

Consumers on the v0.19 → v0.31 cutover should construct the adapter manually
until the higher-level `Server.WithGateEvaluator` option ships (planned for a
future 0.3x release). `ProvenanceCheckerV2` in v0.31 is the allow-all stub in
`cf-convention`; a production implementation is on the cf-authority roadmap.

---

## v0.30.0 — Protocol freeze, layered architecture, and authority system (2026-04-30)

This is the largest release since v0.16. It restructures campfire into a
layered module system, ships a complete trust authority (cf-authority), and
freezes the wire format as a stable foundation for portfolio consumers.

**See [docs/upgrade-0.19-to-0.30.md](docs/upgrade-0.19-to-0.30.md) for step-by-step migration guidance.**
**See [docs/0.30-overview.md](docs/0.30-overview.md) for a one-page architecture overview.**

### New features

#### Protocol layer (cf-protocol)

- **Substrate moved to `internal/`** (`campfireagent-401d`): All substrate
  packages (`campfire`, `message`, `store`, `transport/fs`, `transport/http`,
  `threshold`, `projection`, `predicate`, `crypto`, `encoding`, `admission`)
  moved from `pkg/` to the new `cf-protocol/` Go module. `pkg/protocol` is
  now a forwarding surface — real type definitions live in
  `cf-protocol/protocol/`. Callers importing `pkg/protocol` continue to
  compile via type aliases.
- **`cf-protocol/protocol` public surface**: Type aliases for all `Client`,
  `Message`, `MemberRecord`, `Transport`, and all request/result types.
  `Init`, `InitWithConfig`, and `New` constructors re-exported.
- **Wire-format freeze snapshot** (`campfireagent-3a8`): Real-reflection
  verifier (`wireverify_test.go`) asserts CBOR field IDs, mandatory fields,
  and enum values for all L1 types. Any accidental wire-incompatible change
  fails CI before merge.
- **`campfire:visibility-changed` reserved tag** (`campfireagent-c66`):
  Emitted by `Client.Admit` and `Client.Evict` when campfire visibility
  transitions. Stable L1 system event for federation consumers.
- **`session:open` / `session:close` L1 system event tags** (`campfireagent-647`):
  Emitted at session lifecycle boundaries. Eligible for compaction after close.
- **`tagspec` and `reserved-ops` constants** (`campfireagent-753`): Moved to
  `cf-protocol/internal/tagspec/` and `cf-protocol/internal/reserved-ops/`.
  `CampfirePrefix`, `ConventionPrefix`, `SessionPrefix` tag constants, and
  10 reserved operation codes with `IsReserved()`.
- **`cf-primitives` binary** (`campfireagent-cbd`): New binary exposing exactly
  the frozen 12-command protocol surface (`admit`, `await`, `create`, `disband`,
  `evict`, `init`, `join`, `leave`, `members`, `read`, `send`, `subscribe`).
  `TestPrimitivesSurfaceCeiling` enforces the frozen command set — additions
  require adversary review before merge.
- **`Await` earliest-timestamp-wins** (`campfireagent-5bb`): Documented and
  tested. When multiple fulfillments race, the earliest-timestamp message wins.
  Deterministic tiebreaker for distributed scenarios.

#### Convention layer (cf-conventions)

- **`cf-conventions` Go module with L2/L3 layer separation** (`campfireagent-4d7`):
  New `cf-conventions/` module with strict depguard-enforced layer boundaries.
  L2 (`cf-convention/`) contains the core dispatcher and interfaces. L3
  packages (`cf-authority`, `cf-identity`, `cf-session`, `cf-discovery`,
  `cf-durability`, `cf-connect`) hold convention implementations.
- **`GateEvaluator` interface declared at L2** (`campfireagent-2b9`): Stable
  interface in `cf-convention/gate.go`. L3 implementations (DefaultGateEvaluator)
  depend on L2, never the reverse.
- **`ProvenanceCheckerV2` interface declared at L2** (`campfireagent-2ac`):
  Stable interface for message provenance checking.
- **L2 reserved-op D5 enforcement** (`campfireagent-c85`): Dispatcher
  hard-denies messages carrying reserved operation codes at the L2 boundary.
  No L3 convention can override this floor.
- **Tag-prefix denylist parameterized** (`campfireagent-28f`): `Parse()` now
  accepts `deniedPrefixes []string`. L2 no longer imports L1 tag constants.
  `DefaultDeniedTagPrefixes` covers all reserved campfire/session/naming prefixes.
- **`seed.go` moved to L3** (`campfireagent-c72`): OPEN-018 carveout closed.
  Seed generation is a convention operation, not a protocol primitive.

#### Trust authority (cf-authority)

- **`DefaultGateEvaluator`** (`campfireagent-8d4`): Full L3 trust authority
  satisfying the L2 `GateEvaluator` interface. Implements all 10 D-class
  deal-breakers: chain walk, revocation, scope ceiling, reserved-op floor,
  depth limit, owner ceiling, and TTL enforcement. Wired into
  `ConventionDispatcher` so every convention operation is gate-evaluated.
- **`cf-authority` wire-format freeze verifier** (`campfireagent-301`):
  Separate freeze verifier for `Capability`, `GrantPayload`, `WhereMatcher`,
  `PredicateAST`, and `DenyReason` CBOR types. 15 tests, mutation-confirmed.
- **GateEvaluator conformance harness** (Phase 8 Gate 1): 12-case conformance
  suite runs with `-count=3` in CI on every push touching `cf-authority/`.
- **`GrantPayload.GranterPubKey` (CBOR field 5)** (`campfireagent-171`):
  New `omitempty` field carries the granter's Ed25519 pubkey at the last chain
  hop. `DefaultGateEvaluator` asserts `lastHop.GranterPubKey == RootPrincipal`,
  closing a trust-anchor bypass where any rogue self-signed chain could receive
  `Allow` (CRITICAL security fix).
- **`cf approve` with scope auto-suggestion** (`campfireagent-88d`):
  `cf approve <grant-request-msg-id>` reviews a pending delegation request
  and posts a grant. Scope auto-suggestion walks the failed predicate AST and
  computes the minimum covering scope, with a diff display. Default `--persist 7d`,
  max 30d cap.
- **`cf trust pin/unpin/list/prune`** (`campfireagent-6e2`): TOFU key pin
  management — pin an Ed25519 key for a campfire, remove pins, list with
  metadata, prune pins for left/disbanded campfires. HMAC-integrity protected.
- **`cf init --policy <preset>`** (`campfireagent-e28`): Three identity-policy
  presets write `grant-template.json` to the identity home: `personal-developer`
  (solo, depth 1, 7d TTL), `team-member` (multi-owner, depth 2, 24h auto-grants),
  `public-agent` (hosted MCP posture, depth 1, 24h TTL ceiling).

#### Identity (cf-identity)

- **`cf-identity` package** (`campfireagent-902`): Canonical L3 identity
  convention package. Ships `introduce-me`, `declare-home`, `verify-me`,
  `list-homes`, and `echo` ceremony declarations. `identity:revoked` tag
  support. `ProfileFile`, `LoadProfile`, `SaveProfile` absorbed from
  `cf-protocol/protocol/profile.go` (TRANSITIONAL marker removed). 12 tests,
  5 declarations, full ceremony flow against real `DefaultGateEvaluator`.

#### Session management (cf-session)

- **`cf-session` package** (`campfireagent-c3a`): Full L3 session convention
  package per design v2 §2.9. Lazy-mint per-worker grants: `session:open`
  emitted with `CapabilityTemplate`; workers get one grant tied to a fresh key
  (`IssueWorkerGrant`, depth=1, scoped as `parent_grant ⊓ template`).
  Jail-write backend: `MaterializeWorkerIdentity` writes worker key to
  `0700` dir / `0600` file. Signing-proxy adapter enforces campfire allowlist.
  Disposable session log eligible for compaction after `CloseSession`.
- **`cfs2_` token format** (`campfireagent-d77`): New session token format
  (prefix `cfs2_`) with real transport config embedded. `cfs1_` tokens are
  deprecated — decode returns a clear migration error. `cf session create`
  emits `cfs2_` by default. `swarm-coordination` convention ported to
  `cfs2_` with lazy-mint.

#### Discovery (cf-discovery)

- **`cf-discovery` package** (`campfireagent-550`): L3 discovery convention
  package. Beacon type aliases (`NewWithExpiry`, `ScanFresh`,
  `SignDeclarationWithExpiry`), Tier 1 snippet validation/signing/verification
  per `cf-discovery-spec.md §1-§7`, 3-tier discovery interfaces, sentinel
  errors (`ErrInviteOnly`, `ErrPostJoinVerificationFailed`), `ResolveChain`
  for multi-level chain walks. Rate-limit declarations for level:0 ops
  (OPEN-013). Post-join probe-write-then-observe verification (§11).
  Config-over-beacon endpoint precedence (§12).
- **`center-finding` removed from substrate** (`campfireagent-db1`): Locality
  resolves at the discovery layer (L4, cf-discovery) — not in `Init()`.
  `RecenterClaim`, `RecenterCanonicalPayload`, `walkUpForCenter`,
  `WithWalkUp()`, `WithNoWalkUp()`, `WalkUpEnabled()`, `InitResult.WalkUpPath`,
  `InitResult.Recentered`, and `InitResult.DelegationIssued` are all removed.

#### Convention extensions

- **`cf-convention-extension` path reconciliation** (`campfireagent-a40`):
  Stage 3 path reconciliation and `promote`/`supersede` behavioral ops.
- **`cf-durability` package** (`campfireagent-122`): `pkg/durability` moved
  to `cf-conventions/cf-durability` per design v2 §4.3.
- **`cf-connect` package** (`campfireagent-3a7`): Social connect convention
  moved from `cf-convention-extensions/connect/` to `cf-conventions/cf-connect/`.
- **Snippet schema symbols production-promoted** (`campfireagent-219`): Moved
  from test-only to the `cf-discovery` production package.

#### CLI / UX (cf / argv0 dispatch)

- **Convention surface only**: Protocol-primitive commands (`send`, `read`,
  `await`, `inspect`, `compact`, `dm`, `bridge`, `filter`, `sync`, `nat-poll`,
  `serve`, `dag`, `provenance`) hidden from `cf --help`. Use `--help-primitives`
  to show them. (`campfireagent-09a`)
- **argv[0] dispatch**: `main.go` detects invocation under a non-`cf` name
  (symlink e.g. `social` → `cf`) and calls `Multicall(safeName, args)`. Path
  traversal and shell-injection attempts rejected before any campfire name
  reaches dispatch.
- **Per-app config overlay**: `loadConfigWithApp` inserts
  `~/.cf/apps/<appname>/config.toml` as an "app" layer in the config cascade.
  Each symlinked app gets its own identity, transport defaults, or naming
  seeds without touching the global config.

#### MCP / hosted service (cf-mcp, cf-functions)

- **MCP tools generated from declarations** (`campfireagent-097`): `cf-mcp`
  now generates MCP tools from convention declarations with active gate
  evaluation. `DefaultGateEvaluator` fires in all modes — dev, test, and
  hosted. `NewLocalEmitter()` non-production emitter activates the dispatcher
  without Forge credentials.
- **Azure Functions adapter 0.30 port** (`campfireagent-339`): `cf-functions`
  audited and updated against the 0.30 surface. FIX-4: `handleHealth` now
  probes the child `cf-mcp` `/health` endpoint — a deadlocked child that is
  alive but unresponsive returns 503 (`child_unresponsive`) instead of a
  false-positive 200.

#### Quality / tooling

- **MCP/CLI parity test suite** (F3-INV, Phase 8 Gate 3): 22 named-fixture
  cases across `cf-identity` (6), `cf-authority` (10), `cf-discovery` (6)
  plus 4 stress cases. 5 parity axes: name (A1), argument schema (A2), return
  shape (A3), error category (A4), executor-boundary args (A5).
- **UX measurement harness** (Phase 8 Gate 2): `//go:build uxmeas` harness for
  the approval-flow Budget A (agent→inbox latency). N=100 delegation cycles,
  p95 ≤ 5000 ms, p99 ≤ 8000 ms with bootstrap 95% CI.
- **Compatibility floor**: `COMPATIBILITY.md` + `check-floor.sh` + minor-compat
  CI step (`campfireagent-9be`).
- **Depguard layer enforcement**: `.golangci.yml` with `L1-narrow`,
  `L2-no-extensions`, `no-plural-extensions`, and per-package boundary rules.
  CI hook runs on every PR (`campfireagent-231`).
- **Monotonic nanosecond clock** for message timestamps (`campfireagent-1b7`):
  Eliminates same-timestamp collisions that caused non-deterministic Await
  behavior under high concurrency.
- **OS-assigned ports in all tests**: Eliminated all hardcoded test ports
  (`42800` etc.) to remove TOCTOU races in parallel test runs
  (`campfireagent-fed`, `campfireagent-286`, `campfireagent-b82`).
- **In-memory SQLite for delegation tests** (`campfireagent-90f`): Eliminates
  test hang at high `-race -count` by removing filesystem contention.

### Breaking changes

#### Wire format

- **`GrantPayload` CBOR field 5 added** (`campfireagent-171`, security critical):
  `GranterPubKey []byte` (Ed25519, `omitempty`). Wire-format freeze verifier
  updated; any code building `GrantPayload` by position must add the fifth
  field. Callers using `GrantPayload{Capability: ..., ChildPubKey: ...}` are
  unaffected (named struct literal).

#### Removed packages / binaries

- **`pkg/transport/github` deleted** (`campfireagent-964`): The GitHub
  transport is gone. `cf create --transport github`, `cf join <github-url>`,
  and CLI flags `--github-repo`, `--github-token-env`, `--github-base-url` all
  return errors with migration guidance. `TypeGitHub` sentinel retained in
  `cf-protocol/internal/transport` so existing store rows do not panic.
  `GitHubTransport` tombstone type retained in `cf-protocol/protocol`.
- **`pkg/protocol` is now a forwarding surface**: Real type definitions live in
  `cf-protocol/protocol/`. Direct imports of internal substrate packages under
  the old `pkg/` paths will not compile — use `cf-protocol/` equivalents.

#### Removed primitives / APIs

- **Center-finding removed** (`campfireagent-db1`): `RecenterClaim`,
  `RecenterCanonicalPayload`, `maybeRecenter`, `walkUpForCenter`,
  `WithWalkUp()`, `WithNoWalkUp()`, `WalkUpEnabled()` deleted.
  `InitResult.WalkUpPath`, `InitResult.Recentered`, `InitResult.DelegationIssued`
  removed. `Config.Behavior.WalkUp` (`behavior.walk_up` in config) silently
  ignored.
- **`cf-protocol/protocol/session.go` shared-key form removed** (`campfireagent-c3a`):
  `NewSession` now uses the creator's own identity key. `JoinSession` removed.
  CLI updated. Callers must migrate to `cf-session` lazy-mint pattern.
- **`cfs1_` session tokens deprecated** (`campfireagent-d77`): `DecodeTokenV1`
  returns a migration error on `cfs1_` prefix. Existing `cfs1_` tokens must
  be regenerated with `cf session create`.
- **`present_as` field removed** from production code: `identity.present_as`
  key in `.cf/config.toml` no longer applied. Super-identity rendering is
  handled by `cf-identity` ceremony declarations. Remove `present_as` from
  config files.
- **`tagspec` and `reserved-ops` constants moved** (`campfireagent-753`): From
  `pkg/` to `cf-protocol/internal/tagspec/` and `cf-protocol/internal/reserved-ops/`.
  External callers using these constants must switch to the `cf-protocol/`
  paths or to the `cf-conventions/cf-convention/` re-exports.

#### Renamed / moved packages

- `pkg/campfire` → `cf-protocol/campfire/`
- `pkg/message` → `cf-protocol/message/`
- `pkg/store` (SQLite) → `cf-protocol/store/`
- `pkg/transport/fs` → `cf-protocol/transport/fs/`
- `pkg/transport/http` → `cf-protocol/transport/http/`
- `pkg/threshold` → `cf-protocol/threshold/`
- `pkg/projection` → `cf-protocol/projection/`
- `pkg/predicate` → `cf-protocol/predicate/`
- `pkg/crypto` → `cf-protocol/crypto/`
- `pkg/encoding` → `cf-protocol/encoding/`
- `pkg/admission` → `cf-protocol/admission/`
- `pkg/durability` → `cf-conventions/cf-durability/`
- `cf-conventions/cf-convention-extensions/connect/` → `cf-conventions/cf-connect/`

`pkg/store/aztable` remains at `pkg/store/aztable` (implements
`convention.DispatchStore` from `pkg/convention` — L2 dep).

### Security fixes

- **CRITICAL: Trust-anchor bypass closed** (`campfireagent-171`, `#484`):
  `walkChain` never verified the chain terminates at `RootPrincipal`.
  Any rogue self-signed chain could receive `Allow`. Fixed via
  `GrantPayload.GranterPubKey` (CBOR field 5) and evaluator assertion
  `lastHop.GranterPubKey == req.RootPrincipal`.
- **MEDIUM: CAS generation bug in gate-deny path** (`#484`): Dispatcher
  used `gen=0` hardcoded in `MarkFailedCAS`, silently failing for
  re-dispatched messages. Now reads `GetRedispatchCount` before CAS.
- **`DefaultGateEvaluator` wired in production** (`campfireagent-861`):
  Gate evaluation now active in all dispatch paths, not just tests.
- **FIX-1/MB1: Evict rekey race** (`#436`): `rekeyAfterEvict` serialized
  via per-campfire write lock.
- **FIX-2/MB2: Evict pre-rekey guard** (`#437`): `Client.Evict` errors
  if `epoch_secrets` are absent before attempting rekey on encrypted campfires.

### Migration

See [UPGRADE.md](docs/upgrade-0.19-to-0.30.md) for the full upgrade guide (`campfireagent-901`).

**Quick reference:**

```bash
# Replace pkg/ substrate imports with cf-protocol/ equivalents
# e.g.: pkg/campfire → cf-protocol/campfire

# Remove center-finding call sites
# Delete: WithWalkUp(), WithNoWalkUp(), WalkUpEnabled()
# Delete: InitResult.WalkUpPath, .Recentered, .DelegationIssued
# Delete: behavior.walk_up from .cf/config.toml

# Migrate GitHub campfires
cf create --transport filesystem   # or --transport p2p-http

# Regenerate session tokens (cfs1_ → cfs2_)
cf session create

# Remove present_as from .cf/config.toml

# Update GrantPayload construction if building by position (add field 5)
```

## v0.19.3 — InvalidInput Outcome variant in trust resolution (2026-04-27)

Resolves a long-standing type confusion in `delegation.Resolve`: malformed
`campfireID` input (empty or wrong length) was returned as `InvalidGrant`,
which semantically means "a grant message was found but failed validation."
Callers switching on `Outcome` to diagnose failures could surface "invalid
grant" messages for what is really a caller programming error. (campfireagent-8a7)

### API

- **New `Outcome` variant `InvalidInput`** in `pkg/convention/delegation`.
  Returned by `Resolve` when `campfireID` is empty or not exactly
  `ed25519.PublicKeySize` bytes. Carries the same descriptive `Err` as
  before — only the wrapping type changed.
- **`InvalidGrant` is no longer returned for input errors.** Existing
  `case delegation.InvalidGrant:` arms continue to handle real grant
  validation failures (signature, campfire_id mismatch, expired, ceiling
  violated, malformed payload) — those paths are unchanged.
- **CLI: `cf trust resolve` distinguishes the two cases.** Both the human
  and JSON output paths emit a separate `InvalidInput` status with a
  "malformed campfire ID" message, distinct from the `InvalidGrant`
  message about a bad grant in the chain.

### Compatibility

Adding a variant to the sealed `Outcome` interface is additive for callers
using non-exhaustive type switches with a `default` clause. Callers using
exhaustive switches (the only one in-tree was `cmd/cf/cmd/trust_resolve.go`,
updated in this release) will see a missing-case warning and should add an
`InvalidInput` arm.

## v0.19.2 — membership enforcement on Read/Send/Subscribe (2026-04-15)

Closes the silent-failure surface where non-members could call `Client.Read`
and receive an empty `ReadResult` with no error signal. Source: adversarial
veracity test in the legion repo (`cmd/we/membership_boundary_e2e_test.go`
commit `7323fc4`) documenting DEFECT-3.

### Security

- **`Client.Read` enforces membership**: a caller with no membership record
  for the target campfire now gets `*ErrNotMember` instead of a silent empty
  result. The gate runs after the scope/operation checks and before any
  sync or store query. (campfire-2fc)
- **`Client.Send` uses typed `*ErrNotMember`**: replaces the stringly-typed
  `fmt.Errorf("not a member of campfire %s", …)`. `IsNotMemberError` now
  returns true for Send non-member errors, matching `Members`, `Leave`, and
  the new `Read` behavior. (campfire-2fc)
- **`Client.Subscribe` surfaces `*ErrNotMember`**: the subscription goroutine
  polls via `Read`, so the new Read error propagates through
  `Subscription.Err()` and closes the channel instead of polling forever
  against an empty local store. (campfire-2fc)

### Known limitations carried forward

- The filesystem transport at `pkg/transport/fs` still allows any process
  with read access to the campfire directory to call `fs.ForDir(...)` and
  bypass the SDK entirely. This is the same-host trust assumption
  documented in legion's `docs/design/constellation-private-tools-convention.md`
  §Risks #7. The architectural question — whether campfire should offer a
  mutually-untrusted-same-host transport — is tracked in campfire-894 and
  is out of scope for this patch release.

## v0.19.1 — ssh-agent signing completeness + signature verification (2026-04-14)

Follow-up security hardening from v0.19.0 review/sweep findings. All signing paths now route through the backend when configured. Signature verification added to previously unverified read paths.

### Security

- **Signature verification on revocation read path**: `buildRevokedSet` now calls `VerifySignature()` on each revocation message before trusting `msg.Sender`. Forged revocations injected via filesystem transport are silently skipped. (campfire-c62)
- **Signature verification on recenter claims**: `isAlreadyLinked` now verifies both `CenterSig` and `NewKeySig` on `RecenterClaim` payloads. Forged `delegation-cert` messages can no longer suppress the authorize hook. (campfire-8dd)
- **RegisterOnRelay routes through backend**: Added `Signer` func field to `AgentDescriptor`. Relay registration now uses `NewSigner().Sign` instead of extracting raw `PrivateKey`. (campfire-fc9)
- **Identity.Sign() delegates to backend**: When a `SigningBackend` is configured, `Sign()` now routes through it. All callers get backend signing automatically — no more silent fallback to in-memory keys. (campfire-fc9)
- **Init key mismatch check**: `protocol.Init` now verifies `sshBackend.PublicKey() == id.PublicKey` before proceeding. Returns a clear error on mismatch and closes the socket. (campfire-ca5)

### Bug Fixes

- **SSH-agent signing completeness**: `signRequest` in HTTP transport now calls `SignWithBackend` instead of `Sign`. Session token `EncodeToken` accepts a `CreatorSigner` interface (backward-compatible fallback to `CreatorPriv`). Five CLI call sites migrated from `NewEd25519Signer(PrivateKey)` to `NewSigner()`. (campfire-997, campfire-f32, campfire-f25)
- **Socket leak in Init**: `NewSSHAgentBackend()` socket is now closed on `store.Open` failure and key mismatch. (campfire-76b)
- **InitWithConfig test isolation**: All `InitWithConfig` tests now use `t.Chdir()` to isolate from ancestor `.cf/config.toml` auto-join beacons that caused 600s test timeouts.

### Cleanup

- **Deleted `SSHAgentBackendFromKeyring`**: Panic-prone exported function with nil `agentClient` removed. Superseded by `NewSSHAgentBackendFromKeyring`. (campfire-2a4)
- **Deleted `resolveRelayFromConfig`**: Defined and tested but never called in production. (campfire-64c)
- **Unexported `Identity.Backend`**: Changed to `backend` with `SetBackend()`/`HasBackend()` accessors. Prevents direct mutation of the signing backend. (campfire-803)

## v0.19.0 — trust delegation hardening (2026-04-13)

Security hardening, ssh-agent signing backend, and code quality fixes for the identity delegation system. 7 PRs, 48 demo assertions passing.

### Features

- **SSH-agent signing backend**: `Identity.NewSigner()` routes all root-scoped signing through `SigningBackend` when configured. Five call sites updated: `sendFilesystem`, `sendGitHub`, `sendP2PHTTP`, `postRecenterClaim`, `sendFuture`. Config: `[identity] backend = "ssh-agent"` + `fingerprint = "SHA256:..."` in `.cf/config.toml`. Private key never enters the process — signing delegated to ssh-agent via `SSH_AUTH_SOCK`.
- **`cf trust revoke`**: New CLI command — `cf trust revoke <campfire-id> <child-pubkey> [--json]`. Posts a signed `identity:revoked` message. Human and JSON output modes.
- **Config cascade split-layer**: `backend = "ssh-agent"` in global config + `fingerprint` in project config now works correctly. Validation moved from per-layer to post-cascade.

### Security

- **Hex case normalization**: `findValidGrant` and `buildRevokedSet` normalize `child_pubkey` to lowercase before comparison. An uppercase-hex revocation now correctly matches a lowercase-hex grant.
- **campfireID validation**: `Resolve` rejects empty and wrong-length campfire IDs at entry, before any store reads.
- **O(N) revocation lookup**: Replaced per-candidate `isRevoked()` (O(N*M) store reads) with `buildRevokedSet` — a single pre-fetch pass over revocation messages. Eliminates DoS via grant flood.
- **Truncation safety**: Grant and revocation reads use `Reverse: true` so `MaxGrantReadLimit` truncation preserves the newest messages. Implemented in both SQLite and Azure Table Storage stores.
- **Signature verification on all signing paths**: `agentBackendDirect.Sign` (in-process test path) now verifies the returned signature, matching `SSHAgentBackend.Sign`.

### Bug Fixes

- **aztable `Reverse` flag**: `aztable.ListMessages` now respects `MessageFilter.Reverse` — sorts DESC when true, matching the SQLite store. Previously ignored, breaking truncation safety in production.
- **Revoke read ordering**: The revocation pre-fetch in `findValidGrant` now uses `Reverse: true`, preventing truncation from hiding recent revocations under flood.
- **`protoMsgToRaw` error handling**: Malformed sender hex in grant messages is now surfaced as an error and the grant is skipped, instead of proceeding with nil/empty sender bytes.
- **`mergeLayer` warnings**: Invalid trust anchors are returned as warnings in the `([]string, []string, error)` return instead of calling `log.Printf`. All three callers propagate warnings.
- **CompositeResolver atomic merge**: Chain and Anchor fields are adopted atomically from the first resolver that sets `TrustResolved=true`, preventing attribution of chain evidence from a non-trust-resolved resolver.
- **`ErrStoreRead` wrapping**: Inner store errors now wrapped with `%w` (not `%v`), making them inspectable via `errors.Is`.
- **`TestInitWithConfig_GlobalConfig` hang**: Test now isolates from ancestor config walk to prevent hanging on `.cf/config.toml` files with `auto_join` beacons.

### Tests

- 12 new delegation regression tests (hex normalization, O(N) revocation, campfireID validation, truncation safety, store error wrapping)
- 5 new ssh-agent config tests (backend construction, fingerprint validation, sign verification, split-layer cascade)
- 4 new NewSigner integration tests (file key, ssh-agent backend, public key mismatch, full send path)
- 12 new coverage tests (config security paths, mergeLayer errors, ancestor walk-up, delegation gaps)
- Demo 15 updated: uses `cf trust grant` / `cf trust revoke` CLI instead of raw `cf send`
- Demo 16 updated: uses `cf trust grant` / `cf trust revoke` CLI
- Demo 17 updated: uses `cf trust grant` CLI for ssh-agent grant
- Demo 18 (NEW): v0.19 hardening — 19 assertions covering hex case normalization, campfireID validation, TTL enforcement, JSON output, split-layer config cascade
- Full `go test ./...` green (43 packages)

## v0.18.1 — delegation write path (2026-04-11)

Finishes the v0.18 identity delegation feature. v0.18.0 shipped grant validation and trust resolution (the read half) but left issuance as a hand-marshal operation; v0.18.1 adds the SDK helper and CLI command so callers can write grants with one function call.

### Features

- **`delegation.PostGrant`**: New SDK helper in `pkg/convention/delegation/grant.go`. Constructs, signs, and posts an `identity:granted` message from the client's identity (the parent) to a child ed25519 public key, valid in the given campfire for a caller-supplied TTL. Zero/negative TTLs return `ErrGrantTTLInvalid`; TTLs over 7 days return `ErrGrantCeilingExceeded` — the hard ceiling from identity-delegation-v0.1.md §4 rule 4 is enforced at issuance rather than silently clamped.
- **`cf trust grant`**: New CLI command — `cf trust grant <campfire-id> <child-pubkey> [--ttl 24h] [--json]`. Positional args mirror `cf trust resolve` for consistency. Human output is a compact single-line summary; `--json` emits a structured object with `msg_id`, `parent`, `child`, `campfire_id`, `expires_at`, and `ttl_seconds`.

### Bug Fixes

- **`TestValidateGrant_CeilingExceeded` wall-clock rot**: The test computed `expires_at` relative to a fixture date but compared against `message.NewMessage`'s real `time.Now()` timestamp. On the day it was written the two dates aligned and the assertion passed by coincidence; the test started failing on 2026-04-11 once real time drifted past the fixture by more than a day. The fix computes the 8-day expiry from real wall-clock now so the rule 4 ceiling comparison is consistent with the message timestamp it is compared against.

### Tests

- 4 new `PostGrant` tests (RoundTrip, ZeroTTL, ExceedsCeiling, EndToEnd) in `pkg/convention/delegation`
- 5 new `cf trust grant` CLI tests (Success, MissingArgs, MalformedHex, TTLExceedsCeiling, JSONOutput) in `cmd/cf/cmd`
- Full `go test ./...` green

## v0.18.0 — identity delegation (2026-04-10)

Delegated trust for campfire identities. A trust anchor can grant authority to delegates, who can further delegate — creating verifiable trust chains up to 10 hops deep. Grants are revocable with immediate cascade.

### Features

- **Identity delegation convention**: New `pkg/convention/delegation/` package implements the three approved specs — grant (`identity-delegation-v0.1.md`), trust resolution (`identity-v0.2-trust-resolution.md`), and revocation (`identity-delegation-revocation.md`). Convention-layer only — zero protocol changes.
- **Trust-anchor config**: `[identity.trust] anchors` in `.cf/config.toml` declares ed25519 public keys as trust anchors. Project configs extend (not override) the global anchor list. Supports both hex and base64 encoding.
- **Grant validation**: `ValidateGrant` enforces 5 rules from the spec — signature verification, campfire binding (anti-replay), expiry with 60s clock-skew slack, 7-day hard ceiling, and revocation check.
- **Trust resolution**: `Resolve` walks the local campfire log to determine if a sender is trusted. Four typed outcomes: `Resolved`, `DeadEnd`, `InvalidGrant`, `DepthExceeded`. MAX_CHAIN_DEPTH=10.
- **GrantChainResolver**: Convention handlers access the delegation trust chain via `req.Identity.Chain`, `req.Identity.Anchor`, and `req.Identity.TrustResolved`. Plugs into the existing `IdentityResolver` interface. Optional — servers that don't install it behave exactly as before.
- **`cf trust resolve`**: New CLI command checks delegation trust for a sender in a campfire. Human-readable and `--json` output. Exit code 0 for Resolved, 1 otherwise.

### Bug Fixes

- **`findValidGrant` skip-on-invalid**: Invalid grants (expired, wrong campfire) are now skipped per spec instead of aborting the search. If the newest grant is expired but an older valid one exists, resolution correctly finds it.

### Tests

- 22 delegation tests (grant validation, trust resolution, resolver integration)
- 2 E2E lifecycle tests (full grant→resolve→revoke→re-grant + subtree cascade)
- Demo 15: identity delegation lifecycle — 16 assertions, filesystem transport

### Security

Adversarial security review (Opus) evaluated 10 attack vectors. 0 critical, 0 high findings. The implementation is secure: complete signature verification chain, campfire binding prevents replay, depth cap prevents cycles/DoS, parent-only revocation enforced by sender filtering.

## v0.17.4 — relay end-to-end: create, join, admit, send, read (2026-04-10)

Complete relay campfire lifecycle. All 12 demo scripts pass — filesystem, local relay, and hosted relay (mcp.getcampfire.dev).

### Features

- **`cf admit` on relay campfires**: `protocol.Client.Admit` resolves transport from membership — CLI has no transport awareness. New `POST /campfire/{id}/admit` endpoint on relay adds members to invite-only peer list.
- **Direct relay reads**: `cf read` for p2p-http campfires fetches directly from the relay instead of syncing to a local copy. Eliminates redundant storage and clock-domain cursor mismatches.

### Bug Fixes

- **Azure Table `Timestamp` collision**: Azure Table Storage's reserved `Timestamp` property overwrote message creation timestamps with entity last-modified time (~seconds). Renamed to `MsgTimestamp`; read path falls back to old property for pre-migration rows.
- **Relay send path**: `registerOnRelay` now stores campfire state locally via `fs.Transport.Init` so `cf send` can sign provenance hops. Previously empty TransportDir caused "transport dir is empty."
- **Join state layout**: `joinP2PHTTP` stores state in fs.Transport layout (campfire.cbor) matching creator path. Fixes `cf leave` and `cf read` on relay-joined campfires.
- **Beacon publishing**: `createAndRegisterOnRelay` publishes a local p2p-http beacon so `cf share` works.

### Tests

- All 12 demo scripts pass (50 assertions across filesystem, local relay, and hosted relay)
- Full test suite green

## v0.17.3 — relay campfire creation from CLI (2026-04-09)

CLI agents can now create campfires on hosted relays via `cf create --relay URL`. The relay handles ECDH key exchange, stores the campfire, and returns a beacon. Other agents join with `cf join <id> --via URL` as before.

### Features

- **`cf create --relay URL`**: Registers a new campfire on an HTTP relay via POST /campfire/create. The relay's static X25519 key encrypts the campfire private key during transfer. (campfireagent-b9d)
- **POST /campfire/create endpoint**: Relay-side handler decrypts the campfire private key, registers the campfire, and returns a beacon + endpoint. Includes nonce replay protection. (campfireagent-99ea, campfireagent-67f)
- **`transport.relay` config field**: Set a default relay URL in config.toml so `cf create` auto-registers without `--relay` flag. (campfireagent-081)
- **Auto-generated X25519 key**: cf-mcp generates a static X25519 keypair on startup for ECDH relay registration.

### Bug Fixes

- **Relay send path**: `cf send` now works for relay-created campfires. Campfire state is stored locally during `registerOnRelay` so provenance signing works. (campfireagent-0b0)
- **State file layout**: `sendP2PHTTP` reads `campfire.cbor` (fs layout) before falling back to flat `{id}.cbor` layout.
- **SSRF validation layer**: Moved SSRF protection from HTTP transport to endpoint acceptance layer. Operator-configured endpoints (`--via`, `--relay`) including loopback now work. Peer-supplied endpoints are still validated at acceptance time.
- **Nonce pruner leak**: `Unregister()` now calls `StopNoncePruner()` on the transport. (campfireagent-0b0)
- **Store error handling**: `AddMembership`, `UpsertPeerEndpoint`, `CreateInvite` in global store paths now check errors. (campfireagent-0b0)
- **Beacon config key**: Unified to "endpoint" across all beacon transport configs. (campfireagent-792)

### Tests

- Relay E2E round-trip test (campfireagent-4d2)
- Nonce pruner goroutine leak test
- Demo 09 (local relay) updated and passing

## v0.17.1 — cross-transport CLI fixes (2026-04-09)

Fixes four root causes that prevented `cf join --via` (relay-only, no endpoint) from working correctly. Agents joining via relay are now fully recognized members with working reads, syncs, and member listing.

### Bug Fixes

- **Endpointless members recognized**: `UpsertPeerEndpoint` is now called unconditionally for admitted joiners, even when `JoinerEndpoint` is empty. Previously, `checkMembership` returned 403 for relay-only members. (campfireagent-373)

- **Transport-agnostic sync**: New `Syncer` interface in `pkg/protocol/` replaces `syncIfFilesystem`. `protocol.Client.Read`, `Subscribe`, and `Await` now sync from the relay before returning results for all transport types. (campfireagent-eac)

- **Store-based member enumeration**: `cf ls` and `cf members` dispatch on transport type — filesystem members use existing path, HTTP/GitHub members query `peer_endpoints` via the store. Also fixed `dm.go` which was missed in the initial pass. (campfireagent-968)

- **JoinedAt timestamp units**: `admission.go` now writes `time.Now().UnixNano()` to match the display code in `ls.go` that reads nanoseconds. New members show correct timestamps instead of 1970-01-01. (campfireagent-a7a)

### Tests

- Integration test: endpointless member join + poll/sync/deliver (3 tests)
- Syncer interface unit + integration tests (10 tests)
- Store-based member enumeration tests (5 tests)
- JoinedAt nanosecond unit tests (2 tests)
- E2E cross-transport test proving all 4 fixes work together (1 test)

## v0.17.0 — session durability (2026-04-09)

Azure Table Storage is now the source of truth for all session state. Every durability-critical path (campfire keys, audit campfire IDs, attestations, DM campfires, remote joins, convention sends) falls back to the global store after a cold start. Operator sessions survive indefinitely across instance restarts.

### Features

- **Durable operator sessions**: Operator sessions (forge-tk- auth, TTL=0) are marked `durable` and survive the idle reaper indefinitely. Only explicit revocation or shutdown closes them.

- **Audit campfire persistence**: Audit campfire ID and CBOR persisted to Azure Table Storage. `loadOrCreateAuditCampfire` uses 3-level lookup (local file → cloud → create new). `postMessage` falls back to global store for campfire state.

- **Attestation dual-write**: Attestations written to both local file and Azure Table Storage. Cold start recovers from cloud when local file is absent. `min_operator_level` gates work on any instance.

- **DM campfire durability**: DM campfires written to global store. Cross-instance DM sends and discovery via global store fallback.

- **Remote join durability**: Remote-joined CBOR persisted to global store. `resolveBeaconEndpoint` queries global store when local scan returns empty.

- **Convention send fallback**: Convention sends fall back to global store for `ReadState` after cold start, matching the session KeyProvider pattern.

- **Operator token persistence**: Operator flag persisted in token registry (JSON + Azure Table Storage). Operator tokens skip TTL check on cold start without re-authentication.

### Bug Fixes

- **handleAudit reads from store**: In HTTP mode, `handleAudit` now reads audit messages from the store instead of the filesystem transport. Fixes zero-action audit results after cold start.

- **Revoked durable session cleanup**: `refreshRevocationsFromCloud` now closes and removes revoked operator sessions from the session map, preventing un-reapable zombie sessions.

- **CampfireID validation**: All global store fallback paths validate that the returned membership record's CampfireID matches the requested ID before using the key material.

- **syncToCloud TOCTOU fix**: Added mutex to `dualWriteProvenanceStore` to prevent concurrent mutations from reverting cloud attestation state.

- **Audit campfire creation race**: `SaveAuditCampfireID` uses insert-if-not-exists semantics. Concurrent cold starts converge on the same audit campfire ID via check-after-write.

- **Invite revocation cross-instance**: `handleRevokeInvite` now propagates revocation to the global store so revoked invite codes are rejected on all instances.

### Tests

- 8 durability regression tests (`TestDurability_*`) with `newTestServerWithGlobalStore` and `simulateColdStart` helpers.
- End-to-end cold start cycle test (`TestE2E_DurableSession_FullColdStartCycle`) verifying identity, campfire, convention, DM, and audit survival.
- Attestation cold-start test with real SQLite session store backend.
- Concurrent audit campfire creation test.
- Operator token cold-start survival test.
- Invite revocation global store test.

---

## v0.16.6 — multi-instance hardening and convention adoption (2026-04-09)

### Bug Fixes

- **Discover tenant isolation**: `campfire_discover` global store fallback now filters by the session's agent pubkey, preventing cross-tenant campfire enumeration.

- **Multi-instance beacon discovery**: `campfire_discover` supplements local beacon scan with global Azure Table Storage memberships so campfires created on any instance are discoverable from any other.

- **Per-instance rate limit**: `campfire_init` rate limit reduced from 10 to 3 per instance to keep fleet-wide ceiling near the original 10/min target across ~3 instances.

- **Convention cache TTL**: Convention server registration cache now uses a 60-second TTL instead of caching forever. Handler changes propagate across instances within the TTL window.

- **Audit sequence numbers**: Instance-prefixed sequences (16-bit seed in bits 48–63) prevent false gap anomalies across Azure Functions instances. `detectSequenceGaps` groups by instance seed.

- **Audit sequence parsing**: `handleAudit` uses `strconv.ParseUint` instead of `json.Number.Int64()`, which silently dropped sequences with instance seed ≥ 32768.

- **Per-session KeyProvider**: Falls back to global Azure Table Storage store when local CBOR state is absent (cross-instance key resolution). Returns specific error messages for malformed keys.

### Features

- **Convention adoption UX**: `cf convention install` command for installing convention declarations. Send-time validation warns when a message doesn't match the convention schema. `cf convention adopt` for adopting conventions on a campfire.

---

## v0.16.5 — invite code support for p2p-http (2026-04-09)

### Features

- **Invite codes for p2p-http join**: Invite-only campfires now return invite codes from `campfire_create` and support joining via invite code over p2p-http transport.

---

## v0.16.4 — cross-instance shared state for Azure Functions (2026-04-08)

### Bug Fixes

- **Cross-instance p2p-http**: Campfires created via MCP on one Azure Functions instance were invisible to p2p-http join/deliver/sync on other instances (404 "campfire not found"). TransportRouter now falls back to a shared (non-namespaced) Azure Table Storage store and reconstructs the transport on demand.

- **Auto-provisioned campfires**: `campfire_init` auto-provision path did not write to the global store, making those campfires also invisible cross-instance. Fixed.

- **Invite-only join cross-instance**: `LookupInviteAcrossAllStores` only searched locally registered transports. Invite codes created on another instance returned "not found". Now falls back to the global store.

- **Convention dispatch dedup**: `MemoryDispatchStore` was per-process, allowing double-dispatch and double-billing across instances. Now uses `aztable.TableDispatchStore` when Azure Storage is configured.

- **Token revocation propagation**: Cross-instance token revocations were invisible until restart. Reaper cycle now refreshes revocation status from Azure Table Storage (~15 min propagation).

- **`cf read` silent empty**: `cf read <campfire-id>` on a non-member campfire returned empty results without error. Now returns "not a member of campfire ..." error when an explicit ID is given.

---

## v0.16.3 — invite-only campfires actually work (2026-04-08)

### Bug Fixes

- **`join_protocol` parameter ignored**: `campfire_create` schema used `"protocol"` as the parameter name but all response fields use `"join_protocol"`. Agents passing `"join_protocol"` had the value silently ignored, so all campfires were created as `"open"` regardless of intent. Schema renamed to `"join_protocol"`; `"protocol"` still accepted as a fallback for backward compat.

---

## v0.16.2 — Hosted MCP & Invite Code Fixes (2026-04-08)

### Bug Fixes

- **SSE endpoint path**: The `/sse` endpoint was advertising `/mcp` as the POST target, but Azure Functions exposes it at `/api/mcp`. MCP clients connecting to `mcp.getcampfire.dev` were being told the wrong path. Fixed via `CF_MCP_ENDPOINT_PATH` env var set by `cf-functions`.

- **Invite code gap**: `campfire_create` via MCP returned an `invite_code`; `cf create` (CLI) and `protocol.Client.Create()` (SDK) did not. Agents using the CLI or SDK to create invite-only campfires had no way to get the code without a separate roundtrip. Now returned everywhere: `CreateResult.InviteCode` in the SDK, printed to stderr and included in `--json` output in the CLI.

---

## v0.16.1 — Documentation Overhaul & Domain-Based Naming (2026-04-07)

### Naming

- **Domain-based naming resolution**: `naming.root` and `naming.seeds` in config now accept domain names (e.g., `getcampfire.dev`) in addition to raw campfire IDs. Domain names are resolved via `.well-known/campfire` discovery at runtime.

### Documentation

- **Homepage rewrite**: Rewrote the campfire homepage for clarity and accuracy.
- **New pages**: Created Conventions reference page and Naming/Trust/Federation architecture page.
- **Updated references**: Rewrote SDK, CLI, and MCP reference docs to reflect current API surfaces.
- **README refresh**: Updated README with current install paths, commands, and architecture overview.
- **Agent onboarding**: Added `llms.txt` and `AGENTS.md` to the site root for LLM/agent discovery and onboarding.
- **Hallucination fixes**: Corrected fabricated app operations, wrong provenance model descriptions, fake `cf init` output, and stale path references (`~/.campfire` → `~/.cf`).
- **Versioned snapshot**: Added v0.16 documentation snapshot to `site/docs/v0.16/`.
- **Navigation**: Added Reference sidebar links across case study and reference pages.

---

## v0.14.0 — Identity as Infrastructure (2026-04-01)

v0.14 introduces the operator identity model: every operator has a **center campfire** that anchors their identity and authority. Delegation flows outward from the center. The SDK handles everything — apps register one hook and move on.

### Identity Model

- **Center campfire creation**: `cf init` creates a center campfire with quorum threshold 1 and a passphrase-protected Ed25519 key. The center ID is written to `.campfire/center`. Supports `--remote <url>` for HTTP-transport centers.

- **Functional options on `protocol.Init()`**: `WithAuthorizeFunc(fn)`, `WithRemote(url)`, and `WithNoWalkUp()`. Zero-option calls are backward compatible — no breaking change for existing callers.

- **Walk-up resolver**: `naming.ResolveContext()` performs a single-pass walk up the directory tree, collecting `.campfire/root` sentinels, the center campfire ID, and the context key path. Used internally by `Init()` for center discovery.

- **Context key delegation**: When `Init()` finds a center campfire, it auto-generates an Ed25519 context key and issues a delegation cert signed by the center key. Files written: `.campfire/context-key.pub`, `.campfire/context-key.json`, `.campfire/delegation.cert`. The cert is also posted to the center campfire.

- **Recentering (slide-in)**: When `Init()` detects a center campfire and the current identity isn't already linked, the `WithAuthorizeFunc` hook fires once — "Link this identity to your existing account?" If approved, a two-signature claim (center key + context key) is posted to the center campfire. The hook never fires again for the same center.

### Provenance Tiers

- **`Message.IsBridged()`**: Returns true when a message traversed a blind-relay hop (bridge transport). `Bridge()` now sets `RoleOverride: "blind-relay"` on forwarded messages.

- **`provenance.LevelFromMessage()`**: Computes operator provenance level from message properties — Level 3 (root-key sender), Level 2 (blind-relay hop), Level 0 (default).

- **Convention executor gate**: Declarations can specify `min_operator_level`. The executor rejects messages below that level with a structured error before dispatch.

### Naming

- **`cf name register/unregister/list/lookup`**: CLI subcommands for campfire name management.
- **Join policies**: `cf join-policy set/show` for configuring how campfires admit members. `JoinPolicy` type with persistence.
- **`cf init --name`** inherits join-policy, operator-root, and aliases from parent. `--session` inherits join-policy and operator-root.
- **Consult roots**: `FSWalkRoots` for filesystem-walk consult sentinel. Auto-join open-protocol campfires during name resolution.
- **Configurable consult timeout** via `CF_CONSULT_TIMEOUT` environment variable.

### Security

- **FED-1**: HTTP transport path in `handleSend` now enforces `campfire:*` tag restrictions — writer role cannot inject system tags. Fail-closed on role lookup errors.
- **FED-2**: `handleDeliver` validates `routing:beacon` payload structure before storage, preventing beacon poisoning via malformed messages.
- **Input validation**: Campfire IDs read from `.campfire/center` and `.campfire/root` sentinels are validated against 64-character hex format. `--from` path validated before config inheritance. `JoinRoot` and `ConsultCampfire` validated in `LoadJoinPolicy`. Root campfire IDs validated before use in name resolution.

### Fixes

- `transport.ResolveType()` now correctly handles `p2p-http` transport type for HTTP center campfires.
- `protocol.New()` applies `defaultOptions()` so direct callers get correct `walkUp=true` default.
- `delegation.cert` written with `0600` permissions (was `0644`).
- `--from` without `--name` now returns a clear error.
- Malformed `join-policy.json` errors surfaced in `resolveByName`.

### Testing

- E2E integration test (`TestSDK014_IdentityAsInfrastructure`) exercises all 6 identity outcomes in a single sequence.
- 29 packages, full suite green.

---

## v0.13.4 (2026-03-30)

Previous release. See git history for details.
