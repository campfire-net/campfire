# Changelog

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
