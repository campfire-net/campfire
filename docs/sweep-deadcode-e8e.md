# Dead Code Sweep: Post-Path-Vector Addition (e8e)

Sweep of `pkg/transport/http/router.go`, `handler_message.go`, `pkg/beacon/campfire.go` after path-vector routing replaced flood-and-dedup forwarding.

## Findings

### DEAD: `RoutingTable.NodeID` loop detection block (router.go:147-153)

`rt.NodeID` is only populated via `newRoutingTableWithNodeID()`, which is **test-only**. Production transport construction uses `newRoutingTable()` (transport.go:142), which leaves `NodeID` empty. The guard `if rt.NodeID != ""` never fires at runtime.

Fix: wire `selfPubKeyHex` (set via `SetSelfInfo`) into `routingTable.NodeID` on transport construction, making the in-router beacon loop detection actually active.

### DEAD: `RouteEntry.Verified` field (router.go:41)

Always written as `true` (only entries that pass `VerifyDeclaration` are inserted). Never read in any production forwarding path. Checked in two tests only. Redundant — the invariant is enforced by design, not by checking the field at use time.

### DEAD: `RouteEntry.Gateway` field (router.go:36-37)

Written on every insert but never read in forwarding decisions. Path-vector forwarding uses `NextHop` and `Endpoint`. `Gateway` tracked which campfire advertised a route — useful for flood-era attribution, vestigial post-path-vector. Checked in one test only.

### DEAD: `RouteEntry.Transport` field (router.go:34-35)

Populated from beacon payload but never consulted when building the forwarding target set. Forwarding selects by `Endpoint` and `NextHop` only. Marked TAINTED in the struct comment (operator-asserted). Could be useful for future multi-transport support.

## Confirmed Live (Not Dead)

- **Flood fallback** (handler_message.go:317-337): fires when all routing table entries have empty `Path` fields (legacy pre-v0.5.0 beacons). Intentional per spec. Covered by `TestForwardMessageFloodFallbackLegacyBeacons`.
- **DedupTable** (dedup.go): active — `handleDeliver` calls `dedup.See(msg.ID)` before storing or forwarding. Complements path-vector loop prevention (path check prevents beacon loops; dedup prevents duplicate message delivery from multiple simultaneous next-hops).
- **`reAdvertiseBeacon`** and **`propagateWithdraw`**: fully live, called from `handleDeliver` for `routing:beacon` and `routing:withdraw` tags respectively.

## Test-Only Dead Code

None found. All test helper functions in `*_test.go` are referenced by test cases.
