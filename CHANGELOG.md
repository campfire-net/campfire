# Changelog

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
