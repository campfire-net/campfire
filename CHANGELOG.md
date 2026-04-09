# Changelog

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
