# Design: Named Filter Projections

## Summary

Named filters (`cf view create` / `cf view read`) exist since v0.3 and are in production use across ready, legion, and other projects. The interface is frozen. Today, every `cf view read` performs an O(n) full log scan: it loads all messages in the campfire, builds a fulfillment index (a second full scan), evaluates the predicate per message in Go, then sorts and limits. There is no projection state anywhere in the store.

This design introduces **maintained projections** -- incrementally updated result sets that make `cf view read` O(result set) instead of O(n) for qualifying filter expressions. The interface (`cf view create` / `cf view read`) is unchanged. The correctness criterion is absolute: `cf view read` with a maintained projection must return the **exact same result** as a full scan with the same filter applied to the current message set. Any deviation is a bug, not acceptable staleness.

The approach has two layers:

**Layer 1 — Lazy delta (universal default, all views).** Every view maintains a `high_water_mark` — the `received_at` of the last message processed into its result set. On read, only messages since the mark are evaluated and merged in. This is O(delta) instead of O(n). For a frequently-read view, delta approaches zero — reads approach O(result set). No write-path cost. No new behavior at creation time. Works for all expression classes.

**Layer 2 — Eager projection (opt-in, Class 1 views only).** For views that need guaranteed O(result set) reads regardless of read frequency, opt in via `refresh: on-write` on the view definition. The write path evaluates the predicate against each incoming message and updates the projection synchronously. Upgrade at any time by updating the view definition. Downgrade back to lazy delta by setting `refresh: on-read`.

The `refresh` field already exists in the wire format. `on-read` (current) becomes lazy delta. `on-write` becomes eager projection. No new flags, no new interface. Expression classification determines eligibility for `on-write` — only Class 1 expressions can use eager projection. A Class 2 or 3 expression with `refresh: on-write` is silently downgraded to lazy delta at runtime.

## Filter Expression Classifier

Every predicate AST is walked at view creation time and assigned an incrementalizability class. The classifier is a recursive walk of the `predicate.Node` tree. A view's class is the **worst class** among all nodes in its expression.

### Class 1: O(1)-Incrementalizable (Eager Projection)

A new message can be evaluated in isolation -- no knowledge of prior messages required. The predicate is a pure boolean function of the incoming message's fields.

Node types in this class:

| Node Type | Reason |
|-----------|--------|
| `NodeTag` | Tests only the incoming message's tags |
| `NodeSender` | Tests only the incoming message's sender field |
| `NodeField` + comparison (`NodeGt`, `NodeLt`, `NodeGte`, `NodeLte`, `NodeEq`) against `NodeLiteral` | Tests only the incoming message's payload |
| `NodeTimestamp` + comparison against `NodeLiteral` | Tests only the incoming message's timestamp |
| `NodePayloadSize` + comparison against `NodeLiteral` | Tests only the incoming message's payload length |
| `NodeMul`, `NodePow` | Arithmetic on the above -- still per-message |
| `NodeAnd`, `NodeOr` | Closed under boolean composition of Class 1 children |
| `NodeNot` | Closed under negation of Class 1 children **only** |

`NodeNot` is safe for Class 1 children because negating a per-message boolean does not require backward scanning. `(not (tag "done"))` evaluates to true/false for the incoming message alone. The concern raised in adversary A2 about `(not (has-fulfillment))` is real but applies to `NodeHasFulfillment`, not to `NodeNot` in general.

### Class 2: Bounded History Required (Deferred -- Always Scan for v1)

Maintaining the result set requires tracking a bounded auxiliary structure (priority queue, sorted index, eviction threshold).

| Expression Form | Reason |
|-----------------|--------|
| `LIMIT` clause in viewDefinition | Top-N maintenance requires eviction of the N+1th element. Out-of-order timestamp arrival (permitted by protocol spec) requires insertion into arbitrary positions. |
| Time-windowed predicates | Entries expire as time passes, requiring background eviction incompatible with Azure Functions Consumption plan. |

For v1, LIMIT expressions fall back to full scan. This avoids the compaction + LIMIT interaction (domain purist P3) where compaction can reduce the result set below the limit, requiring backfill from previously excluded messages.

### Class 3: Full History Required (Always Scan)

The predicate's evaluation for a message can change based on **future** messages. No incremental maintenance is possible without retroactive eviction of prior projection entries.

| Node Type | Reason |
|-----------|--------|
| `NodeHasFulfillment` | Whether a message "has a fulfillment" depends on whether any **other** message carries a `fulfills` tag with this message as antecedent. A new fulfills message retroactively changes the evaluation of a prior message. Maintaining this requires: (1) a persisted fulfillment index, (2) on every fulfills message, scanning all antecedents to update projection membership for every active view. This is O(antecedents * active_views) per fulfills message. |

**NodeHasFulfillment is NOT incrementalizable in v1.** Any predicate containing `(has-fulfillment)` anywhere in its AST -- including `(not (has-fulfillment))`, `(and (tag "x") (has-fulfillment))`, etc. -- is classified as Class 3 and always scans. This resolves adversary attacks A2 and A8.

Promoting `NodeHasFulfillment` to incremental maintenance is deferred. It requires a tombstone/eviction design: a persisted fulfillment index per projection, retroactive projection entry removal on fulfills message arrival, and crash recovery for partial index updates. This is a separate design effort.

### Classification Algorithm

```
func classify(node *Node, viewDef viewDefinition) Class {
    if viewDef.Limit > 0 {
        return ClassAlwaysScan  // Class 2 -> always scan in v1
    }
    return classifyNode(node)
}

func classifyNode(node *Node) Class {
    switch node.Type {
    case NodeHasFulfillment:
        return ClassAlwaysScan
    case NodeTag, NodeSender, NodeTimestamp, NodePayloadSize, NodeLiteral:
        return ClassIncremental
    case NodeField:
        return ClassIncremental
    case NodeGt, NodeLt, NodeGte, NodeLte, NodeEq, NodeMul, NodePow:
        return worst(classifyNode(node.Children[0]), classifyNode(node.Children[1]))
    case NodeNot:
        return classifyNode(node.Children[0])
    case NodeAnd, NodeOr:
        c := ClassIncremental
        for _, child := range node.Children {
            c = worst(c, classifyNode(child))
        }
        return c
    }
    return ClassAlwaysScan  // unknown node -> safe fallback
}
```

Location: `pkg/projection/classifier.go` (~80-120 LOC).

## Projection Storage

Views currently have no dedicated table. They are stored as regular campfire messages with the tag `campfire:view`. This design adds a dedicated projection table for storing maintained result sets.

### SQLite Schema

```sql
CREATE TABLE IF NOT EXISTS projection_entries (
    campfire_id    TEXT    NOT NULL,
    view_name      TEXT    NOT NULL,
    message_id     TEXT    NOT NULL,
    indexed_at     INTEGER NOT NULL,  -- received_at of the message when indexed
    PRIMARY KEY (campfire_id, view_name, message_id)
);

CREATE INDEX IF NOT EXISTS idx_projection_ts
    ON projection_entries(campfire_id, view_name, indexed_at);
```

Added as a schema migration (next version after the current migration list).

The `indexed_at` column stores the `received_at` timestamp of the message at the time it was added to the projection. This serves two purposes: (1) ordering for read-path queries, and (2) high-water mark for crash recovery.

Message content is NOT duplicated into projection entries. The read path joins `projection_entries.message_id` against `messages.id` to retrieve full content. This avoids storage amplification and keeps compaction handling simpler (see Compaction Handling section).

### Azure Table Schema

Table: `CampfireProjections`

| Field | Value |
|-------|-------|
| PartitionKey | `campfire_id` |
| RowKey | `view_name \| zero-padded-indexed-at \| message_id` |

The zero-padded timestamp in RowKey follows the existing `epochPadWidth` pattern (20 digits) used throughout `pkg/store/aztable`. This enables ordered range scans by view name and timestamp within a single PartitionKey query.

No joins are possible in Azure Table Storage. The read path issues two RPCs: (1) range query on `CampfireProjections` to get message IDs, (2) batch get on the messages table to retrieve content. This is still O(result set), not O(n).

Constraints:
- 24 KB chunk limit per entity property. Projection entries store only IDs and timestamps, well within this limit.
- No atomic write across the messages table and projections table. See Crash Recovery for handling this.
- Separate RPCs for message write and projection update -- no rollback guarantee.

### Projection Metadata

Each (campfire_id, view_name) pair has associated metadata tracked by the middleware:

| Field | Purpose |
|-------|---------|
| `predicate_hash` | SHA-256 of the serialized predicate AST. Detects view definition changes. |
| `last_compaction_id` | Message ID of the most recently processed `campfire:compact` event. Detects stale projections after compaction. |
| `high_water_mark` | `received_at` of the last message processed into the projection. Used for crash recovery. |

In SQLite, this is a separate `projection_metadata` table:

```sql
CREATE TABLE IF NOT EXISTS projection_metadata (
    campfire_id       TEXT NOT NULL,
    view_name         TEXT NOT NULL,
    predicate_hash    TEXT NOT NULL,
    last_compaction_id TEXT NOT NULL DEFAULT '',
    high_water_mark   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (campfire_id, view_name)
);
```

In Azure Table, this is a single entity per (campfire_id, view_name) with RowKey = `view_name|_meta`.

## Write-Path Integration

**Option C -- middleware wrapper at the `protocol.Client` layer.** The `store.Store` interface is not modified. Both `SQLiteStore` and `TableStore` remain unchanged.

### Architecture

A `ProjectionMiddleware` wraps `store.Store`. The `protocol.Client` holds this wrapper instead of the raw store.

**On `AddMessage`:**

1. Call `base.AddMessage(record)` — the actual message write.
2. If the message is a `campfire:view` message, invalidate the view definition cache for this campfire.
3. If the message is a `campfire:compact` message, handle compaction (see Compaction Handling).
4. Otherwise: for each active `refresh: on-write` view in this campfire (Class 1 only, from the in-memory cache), evaluate the predicate against the new message. If it matches, insert a projection entry. `refresh: on-read` views are not touched on write.

**On `ReadView` (lazy delta path, all views):**

1. Load the view's `high_water_mark` from projection metadata.
2. Query messages with `received_at > high_water_mark` for this campfire.
3. For each delta message, evaluate the predicate. Matching messages are added to the projection; non-matching are skipped.
4. Update `high_water_mark` to the latest processed message's `received_at`.
5. Return the full projection result set (prior entries + delta additions), re-sorted by timestamp.

For `refresh: on-write` views, the delta is normally empty (write path kept it current). The lazy delta read serves as crash recovery — if the write path missed messages (crash, Azure RPC failure), the read path catches them.

### Active View Cache

The middleware maintains an in-memory cache of active view definitions per campfire. This cache is populated lazily: on the first write to a campfire (or on cache miss), query `campfire:view` messages via `ListMessages` with tag filter, parse view definitions, classify each, and cache the result.

Cache invalidation: when a `campfire:view` message is inserted (detected in step 2 above), the cache entry for that campfire is cleared. The next write will re-populate it.

### View Cap (Adversary A1 -- DoS via Fanout)

The number of active views per campfire is capped. When a write triggers projection updates, the middleware processes at most N views. Views beyond the cap are not projected (they fall back to full scan on read).

| Deployment | Default Cap |
|------------|-------------|
| Local (SQLite) | 20 |
| Hosted (Azure Functions) | 10 |

Configurable via environment variable `CF_MAX_PROJECTED_VIEWS`. This bounds the worst-case write amplification to N predicate evaluations + N projection writes per `AddMessage` call.

The cap applies to **projected** views (Class 1 only). Class 2 and Class 3 views are not projected and do not count against the cap. There is no limit on the total number of view definitions -- only on how many get eager maintenance.

### System Message Exclusion

The current view read path explicitly skips messages with `campfire:*` tags before predicate evaluation. The write-path projection replicates this: system messages (any tag with `campfire:` prefix) are never evaluated against view predicates and never inserted into projections. This exclusion is part of the projection contract and is documented as a protocol invariant.

### Projection Update Is Synchronous

Projection updates happen synchronously within the `AddMessage` call path. This is required for correctness (domain purist P6, condition 1): the projection must reflect the message immediately after `AddMessage` returns. Asynchronous update would create a window where `cf view read` returns stale results.

For SQLite, the projection write is in the same database (same file, same connection) -- effectively the same transaction.

For Azure Table, the projection write is a separate RPC after the message write. This is NOT atomic. See Crash Recovery for handling the gap.

## Crash Recovery (Adversary A5)

Azure Functions Consumption instances can be killed mid-write. The failure mode: message is written to the messages table, but projection update did not complete. The projection is now behind.

### Detection

Each projection stores a `high_water_mark` -- the `received_at` of the last message processed into the projection (from `projection_metadata`). On any projection read, the middleware compares this against the campfire's actual latest `received_at` from the messages table.

If `latest_message_received_at > high_water_mark`, the projection has a gap.

### Recovery

On gap detection, the middleware performs an incremental replay:

1. Query messages with `received_at > high_water_mark` for this campfire.
2. For each message (in `received_at` order), evaluate it against the view's predicate.
3. Insert matching messages into the projection.
4. Update `high_water_mark`.

This is O(delta since last write), not O(n). For a typical gap (one missed message due to crash), this is O(1).

### Recovery Trigger

Recovery runs on **read** (lazy), not on startup. This avoids startup latency in the Azure Functions cold-start path. When `cf view read` detects a gap, it patches before returning. The first read after a crash pays the recovery cost; subsequent reads are O(result set).

For writes, recovery is not needed -- the write path simply continues from the current state. Any gap will be detected and patched on the next read.

## Compaction Handling (Adversary A4, Domain Purist P3)

When a `campfire:compact` message arrives (detected by the `campfire:compact` tag), the middleware:

1. Parses the compaction payload to extract the list of superseded message IDs.
2. Deletes all projection entries whose `message_id` is in the superseded set, across all views for this campfire.
3. Updates `last_compaction_id` in projection metadata to this compact message's ID.

### Stale Compaction Detection

On every projection read, the middleware checks: does the stored `last_compaction_id` match the campfire's current compaction state? To determine the current state, it queries for `campfire:compact` messages. If any compact message exists with an ID newer than `last_compaction_id`, the projection is stale.

When a stale projection is detected, the middleware rebuilds it from scratch: full scan with predicate evaluation, replacing all projection entries. This is the O(n) fallback, triggered only by compaction events (which are infrequent -- compaction is an operator action, not an automatic process).

### LIMIT + Compaction

LIMIT expressions are in Class 2 (always scan) for v1. This means compaction + LIMIT interaction is not a problem: LIMIT views always do a full scan, which naturally handles compaction correctly.

If LIMIT is ever promoted to incrementalizable (Class 1), the compaction interaction must be explicitly designed. Compaction can reduce the result set below the limit, requiring backfill from messages that were previously excluded by the limit boundary. This backfill requires either maintaining an eviction buffer or falling back to full scan on compaction. **This is a known hard case and is explicitly deferred.**

## Stateful Projections (Adversary A3, Domain Purist P4)

"Latest message about entity X" (the ready/legion pattern) is NOT expressible in the current predicate language. The predicate language is a per-message boolean filter (a WHERE clause). It does not support:

- GROUP BY (group messages by entity, keep latest per group)
- WINDOW FUNCTION (rank messages within a partition)
- STATE ACCUMULATION (fold messages into running state)

These are aggregation semantics, not filter semantics. The predicate language should remain a pure boolean filter.

### Resolution

Do NOT extend the predicate language. Instead, add a separate **optional** field to the view definition:

```json
{
  "name": "open-items",
  "predicate": "(tag \"status-change\")",
  "entity-key": "payload.bead_id",
  "ordering": "timestamp desc",
  "refresh": "on-read"
}
```

When `entity-key` is present:
- The projection stores **one entry per unique entity key value** (the latest message wins by timestamp).
- On write: extract the entity key from the message payload using the field path, look up the existing projection entry for that key, replace it if the new message's timestamp is newer.
- On read: return all current projection entries (one per entity).

When `entity-key` is absent: the projection is append-only (every matching message is in the result set).

This is a new **optional** field on the view definition -- backwards compatible, opt-in, explicit. Existing views without `entity-key` continue to work as append-only projections. The `cf view create` command gains an `--entity-key` flag.

### Entity Key Extraction

The `entity-key` field is a dot-separated path into the message payload JSON (same syntax as `(field "path")` in the predicate language). The projection middleware parses the payload, extracts the value at the path, and uses its string representation as the entity key.

If the entity key field is missing from a message's payload, that message is skipped (not added to the projection). This handles messages that match the predicate but are not entity-bearing (e.g., system announcements within a convention).

## Azure Table Throughput (Adversary A6)

Azure Table Storage has a per-partition throughput ceiling of approximately 2,000 operations/second. Campfire ID is the PartitionKey.

Worst-case analysis with projection maintenance:
- A campfire with C projected views at M messages/second generates C * M projection writes/second.
- With the default hosted cap of 10 views at 100 msgs/sec = 1,000 projection writes/sec (within ceiling).
- With the default hosted cap of 10 views at 200 msgs/sec = 2,000 projection writes/sec (at ceiling).

Mitigations:
1. **View cap** (A1 fix): bounds C. Default 10 for hosted, 20 for local.
2. **Only Class 1 views are projected**: Class 2 and 3 views do not add write-path load.
3. **Batch projection writes**: group all projection updates for a single `AddMessage` into a single Azure Table batch transaction (entities in the same partition, same table -- eligible for Entity Group Transactions). This reduces N projection writes to 1 batch RPC.

Documented constraint: a campfire approaching the throughput ceiling should reduce its projected view count or accept higher write latency. This is a property of Azure Table Storage, not a design flaw.

For extreme throughput campfires (>500 msgs/sec sustained), the projection middleware should be configurable to disable eager projection entirely (all views fall back to full scan). This is a deployment-time configuration, not a per-view setting.

## Behavioral Equivalence

From domain purist P6: the proposed execution model preserves the semantics of `cf view read` exactly, but ONLY if all six conditions are met:

1. **Projection updates are synchronous with message writes.** The middleware updates projections in the same call path as `AddMessage`. No async queue.

2. **The read path re-sorts results by the view's ordering field.** Projections see messages in arrival order (`received_at`). The current implementation returns messages in `timestamp` order (sender-asserted). The projection read path must re-sort by timestamp (or the view's declared ordering). Cost: O(k log k) where k is the result set size, but k << n.

3. **Compaction processing is atomic with projection updates.** When a `campfire:compact` message is processed, superseded entries are removed from projections in the same operation. For SQLite this is the same transaction. For Azure Table, stale-compaction detection on read (see Compaction Handling) provides eventual correctness.

4. **Fulfillment index updates are atomic with message writes.** Resolved by classifying `NodeHasFulfillment` as Class 3 (always scan). No fulfillment index is maintained incrementally. Full scan builds it fresh every time.

5. **View definition changes trigger full projection rebuild.** The `predicate_hash` in projection metadata is compared against the current view definition on read. Mismatch triggers rebuild. On write, a `campfire:view` message invalidates the view cache and deletes all projection entries for the affected view.

6. **System message exclusion uses the same prefix-based rule.** Messages with any tag starting with `campfire:` are excluded from predicate evaluation on both the write path (projection maintenance) and the read path (full scan fallback). This rule is documented as a protocol invariant.

## Migration

### Existing Views

No migration needed for read correctness. The full scan remains as the fallback for any view without a current projection. Projection building is **lazy**: a view gets its first projection entry on the next qualifying write after this change ships. There is no flag day.

On the first `cf view read` after upgrade, if the projection is empty (no entries, high_water_mark = 0), the read path falls back to full scan (existing behavior). As new messages arrive and the write path populates the projection, subsequent reads use the projection.

### View Definition Changes (Adversary A9)

When `cf view create` is called with an existing name, the latest `campfire:view` message wins (existing behavior via `findLatestView`). The projection middleware detects this via `predicate_hash` mismatch:

1. On write: inserting a `campfire:view` message invalidates the view cache.
2. On subsequent write to this campfire: the cache is repopulated with the new view definition. The new predicate hash does not match the stored projection metadata.
3. All projection entries for this view are deleted.
4. Rebuild happens lazily -- new writes populate the projection under the new predicate.

No explicit "view update" operation is needed. The existing message-based view definition mechanism naturally propagates changes.

## What Is NOT in Scope

- **NodeHasFulfillment incremental maintenance** -- deferred. Requires tombstone/eviction design, persisted fulfillment index per projection, retroactive entry removal. Separate design effort.
- **Time-windowed projections** -- deferred. Requires background eviction worker incompatible with Azure Functions Consumption plan.
- **LIMIT incremental maintenance** -- deferred (Class 2, always scan in v1). Requires priority queue, eviction on out-of-order arrival, compaction backfill.
- **Cross-campfire joins** -- not in current predicate language, not in scope.
- **C7 (names as edges)** -- separate design.
- **Automatic heat promotion** (creative C3) -- not needed. Lazy delta (`refresh: on-read`) is the universal baseline. Operators upgrade to eager (`refresh: on-write`) explicitly when they know a view is hot. No automatic tiering.
- **Denormalized payload storage in projections** -- projections store message IDs, not content. Content is joined from the message store on read. This keeps projections small and avoids staleness when message content is referenced.

## Adversary Attack Disposition

| Attack | Disposition | Resolution |
|--------|-------------|------------|
| **A1** -- DoS via unbounded write-path fanout | **Resolved** | Cap active projected views per campfire (default 10 hosted, 20 local). Configurable via `CF_MAX_PROJECTED_VIEWS`. Only Class 1 views count against the cap. |
| **A2** -- NOT expressions not incrementally maintainable | **Resolved** | `NodeNot` over Class 1 children is safe (per-message boolean). `NodeNot` over `NodeHasFulfillment` inherits Class 3 (always scan). The classifier propagates the worst class upward. |
| **A3** -- Stateful projection requires entity key not in protocol | **Resolved** | New optional `entity-key` field on view definition. Backwards compatible, opt-in. Does not modify the predicate language. |
| **A4** -- Compaction breaks projections silently | **Resolved** | Compaction events trigger projection entry deletion for superseded messages. Stale-compaction detection on read via `last_compaction_id` comparison. Mismatch triggers full rebuild. |
| **A5** -- Crash during write creates projection/log divergence | **Resolved** | `high_water_mark` per projection detects gaps. Lazy incremental replay on next read: O(delta), not O(n). |
| **A6** -- Azure Table throughput limits | **Permanent Constraint** | Documented. Mitigated by view cap (bounds write amplification), batch transactions (reduces RPC count), and deployment-time disable switch for extreme throughput. |
| **A7** -- Windowed projections require eviction | **Deferred** | LIMIT and time-windowed expressions are Class 2 (always scan in v1). No eviction logic needed. |
| **A8** -- has-fulfillment requires global index | **Resolved** | `NodeHasFulfillment` is Class 3 (always scan). No incremental fulfillment index maintained. Full scan builds it fresh. |
| **A9** -- View definition mutation invalidates projections | **Resolved** | `predicate_hash` in projection metadata detects definition changes. Mismatch deletes all projection entries and triggers lazy rebuild. |
| **A10** -- Refresh field semantic boundary | **Resolved** | `refresh: on-read` = lazy delta (O(delta) read, zero write-path cost). `refresh: on-write` = eager projection (O(result set) read, write-path cost, Class 1 only). Upgrade and downgrade by updating the view definition at any time. Existing views with `refresh: on-read` automatically get lazy delta — no migration needed. The `refresh` field is the intentional operator control surface, not a semantic hazard. |

## Implementation Sequence

1. **Classifier** (`pkg/projection/classifier.go`): AST walk, class assignment. ~100 LOC. Testable in isolation.
2. **Projection storage** (SQLite migration + CRUD in `pkg/store/store.go`, Azure Table CRUD in `pkg/store/aztable/`): schema, insert, delete, query, metadata. ~400-500 LOC combined.
3. **Middleware wrapper** (`pkg/projection/middleware.go`): wraps `store.Store`, intercepts `AddMessage`, manages view cache, evaluates predicates, updates projections. ~200-250 LOC.
4. **Read-path integration** (modify `cmd/cf/cmd/view.go`): check for current projection, use it if available, fallback to full scan. Re-sort by view ordering. ~80-120 LOC.
5. **Crash recovery** (in middleware read path): gap detection, incremental replay. ~50-80 LOC.
6. **Compaction handling** (in middleware write path): superseded entry deletion, stale detection. ~50-80 LOC.

Total new LOC: ~900-1100.
