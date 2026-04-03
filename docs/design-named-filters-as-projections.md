# Named Filters as Maintained Projections

**Item:** rudi-196  
**Status:** proposal  
**Author:** baron

## Problem

Campfire is an append-only message log. State is the sum of all messages. This is correct and intentional.

The problem is the read path. Today, named filters are predicate configurations — S-expression rules stored on the server, but evaluated by clients at read time against a full log fetch. Every consumer that wants filtered state must:

1. Fetch the entire message log
2. Apply the predicate locally
3. Derive current state from the filtered result

This is O(n) per read, where n is total message count. For human-paced work on a single project, it's acceptable. For autonomous agents — multiple agents across many projects, querying on work loops, generating items at machine speed — it compounds into a structural bottleneck:

- N agents × full log fetch = traffic that scales quadratically with agent count
- Message log grows at agent speed, not human speed
- Cross-project queries (e.g. portfolio views) are O(n × m) where m is project count
- Cold-start cost grows without bound as the log ages

Every campfire convention with stateful semantics has this problem. Ready (work management), GalTrader (game state), any convention where "current state" means "replay from message 0" will hit this ceiling.

## Interface Is Already Correct

Named filters with S-expression predicates is the right configuration surface. The interface doesn't need to change:

```bash
cf view create <campfire> ready '(and (tag "work:create") (not (tag "work:close")))'
cf view read <campfire> ready
```

The gap is purely in execution: campfire evaluates the predicate at read time against the full log, rather than maintaining the projection incrementally as messages arrive.

## Proposed Change

Named filters become **maintained projections**. Campfire evaluates filter predicates against each incoming message and updates the projection state in place. Reads return the current projection directly — no log scan, no client-side derivation.

### Execution Model

**Write path (message arrives):**
1. Append message to log (unchanged)
2. For each registered named filter on this campfire: evaluate predicate against the message
3. If predicate matches: apply message to the filter's projection state
4. Projection state is immediately consistent

**Read path:**
1. Return current projection state
2. O(result set) — no log scan

**Filter registration (new or campfire restart):**
1. Replay log from message 0 (or from last snapshot — see below) to build initial projection
2. Switch to incremental maintenance
3. Bootstrap cost is one-time; subsequent reads are O(1)

### Snapshots

Once projections are maintained, campfire can snapshot projection state at compaction points. On restart, bootstrap from the snapshot + delta rather than from message 0. Bounded cold-start cost regardless of log age.

Snapshots are an optimization, not a requirement for correctness. Correctness comes from the incremental model; snapshots just bound the restart cost.

## What Consumers Get

Consumers don't change their interface. `cf view read` already exists. When this ships, the same call returns maintained state instead of triggering a client-side scan.

For Ready specifically: named views (`ready`, `work`, `overdue`, `delegated`) are already registered as S-expression predicates against the work campfire. When campfire starts maintaining those projections, Ready gets sublinear queries with no code changes.

The same is true for any other convention that registers named filters.

## Scope

This change is internal to campfire's server implementation. The protocol surface (view create, view read, view subscribe) is unchanged.

Key implementation concerns:

- **Predicate evaluation engine**: S-expression predicates must be evaluatable against individual messages incrementally, not just against a full corpus
- **Projection storage**: What structure holds maintained state per filter (likely a derived message list or a key-value map depending on predicate type)
- **Consistency on restart**: Replay from snapshot + delta must produce identical state to full replay
- **Predicate types that require global context**: Some predicates may reference relative state (e.g. "not (tag X) on any message with same item-id") — these need careful handling in the incremental model

## Out of Scope

- Changes to the named filter predicate language
- Changes to the `cf view` command interface
- Consumer-side changes in any project
