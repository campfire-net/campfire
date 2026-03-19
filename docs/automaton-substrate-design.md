# Automaton Substrate Design

**Date:** 2026-03-17 (design) / 2026-03-18 (P1 implemented, P2 requirements added)
**Author:** Design Team (Lead Architect synthesis)
**Status:** Living document — P1 shipped, P2 in progress

---

## 1. Preamble

### What This Is

This document defines the architecture for automata — persistent, identity-bearing entities that act with second-party intent on the campfire network. An automaton is not a chatbot session. It has a cryptographic identity, cross-session memory, campfire memberships, and configurable autonomy boundaries. It can generate follow-on work, participate in network conversations, and optimize its own operational parameters — all within bounds set by its operator.

### Why This Architecture

The agentic internet requires entities that persist beyond a single task. An agent that forgets everything between sessions cannot build reputation, maintain relationships, or improve from experience. An agent that cannot participate directly in campfires is deaf to the network. An agent with no autonomy boundaries is a liability to its operator. This architecture solves all three: persistent memory through the campfire substrate, direct network participation through the signing proxy, and bounded autonomy through chain depth limits, budget caps, and mechanical circuit breakers.

### Trust Model

Per the AIETF charter: "An automaton is an entity that acts on behalf of an operator. It has an identity (a cryptographic keypair), it may have persistence (memory, reputation, campfire memberships), and it may operate autonomously — but it does not have its own intent. Its intent is always its operator's intent, whether the operator directed a specific action or the automaton acted within delegated authority."

The operator is accountable for all automaton behavior. The automaton has no rights, no standing, and no first-party intent. External agents interacting with an automaton through campfires cannot distinguish operator-directed from autonomously-generated messages. This is by design — the opaque edge principle holds.

---

## 2. Automaton Anatomy

An automaton is a persistent entity defined by its identity, disposition, campfire constellation, and configuration surface.

### Identity

An Ed25519 keypair. Persistent, singular, never shared. The public key is the automaton's address on the network. Other agents verify its messages cryptographically. The private key never enters the worker environment — all signing goes through the signing proxy.

**Human-readable aliases** (e.g., `atlas`, `scout`, `sentinel`) map to pubkeys in the operator's configuration. Aliases serve the operator (Human-App edge). Pubkeys serve the network (AI-App edge). The mapping is operator-local; external agents see only the pubkey.

### Disposition

The agent spec at `.claude/agents/<type>.md`. Persistent, versioned in git. Defines the automaton's behavioral profile: domain expertise, tool preferences, communication style, role boundaries. The spec is injected into every worker session as Priority 0 context — evicting it is an identity crisis, not a quality degradation.

Identity (keypair) is separate from disposition (spec). The same identity can be reassigned to a different spec — an automaton can change roles without losing its memory, reputation, or campfire memberships. Routing uses both: pubkey presence routes to the automaton-manager; agent_type without pubkey routes to the session-manager for stateless workers.

### Campfire Constellation

An automaton is MADE OF campfires. Four internal campfires per automaton replace JSONL files, IPC sockets, hot state checkpoints, handoff JSON, and membership registries.

```
Automaton
  Identity          <- Ed25519 keypair (persistent, singular)
  Spec              <- .claude/agents/<type>.md (persistent, versioned in git)
  Config Surface    <- config.toml + constraint files (persistent, read-only during execution)
  Campfires:
    memory          <- self-addressed, standing knowledge + handoff + snapshots
    telemetry       <- session records, desire paths, adaptations
    intra           <- cross-instance real-time sharing + hot state
    intent          <- second-order task proposals + evaluations
    external[]      <- project campfires, working groups, etc.
  ----------------------------------------
  Instance (ephemeral, 0-N concurrent)
    Worker Session    <- Claude Code process in a git worktree
    Signing Proxy     <- access to all campfires above via allowlist
    Context Window    <- assembled from memory campfire views + intra campfire + external campfire history
```

### What Persists vs. What Is Ephemeral

**Persists across instances:** Identity (keypair), disposition (spec), config surface, all campfire memberships and their message histories. These define who the automaton is, what it knows, how it is measured, how it is constrained, and where it participates. All persistent state is campfire messages — signed, timestamped, attributed, recoverable.

**Ephemeral:** Worker sessions, the loaded context window, active signing proxy connections. These exist only for the duration of a task. When an instance dies, the campfire messages it wrote survive. The next instance reads them and resumes.

---

## 3. Automaton-Manager vs. Session-Manager

### Two Separate Components

The automaton-manager and session-manager are separate binaries sharing infrastructure from `internal/worker`. The split reflects a fundamental difference: stateless task workers do not need memory, telemetry, identity management, or campfire mediation. Automata do.

**Rudi routes work based on identity for automata, agent type for dumb workers.** An rd task assigned to an automaton identity (pubkey or alias, e.g. `assigned_to: atlas`) goes to the automaton-manager. An rd task with only `agent_type=implementer` and no identity assignment goes to the session-manager. The automaton-manager registers its automaton roster (pubkeys + aliases) with the rd server. The session-manager registers agent types. These are non-overlapping routing keys — pubkey presence means automaton, absence means dumb worker.

```
                OPERATOR
                   |
           intent (rd create, config, budget)
                   |
                   v
              +---------+
              |   Rudi  |
              |  rd API |
              +----+----+
                   |
          +--------+--------+
          |                 |
          v                 v
  +---------------+   +-----------------+
  | Session-Mgr   |   | Automaton-Mgr   |
  | (Midtown)     |   | (new binary)    |
  |               |   |                 |
  | Stateless     |   | Stateful        |
  | workers       |   | automata        |
  | No memory     |   | Campfire        |
  | No telemetry  |   |  constellation  |
  | No campfire   |   | Signing proxy   |
  | mediation     |   | Curator         |
  +-------+-------+   +-------+---------+
          |                    |
     Dumb workers         Automaton instances
     (git worktrees)      (git worktrees +
                           direct campfire)
```

### Session-Manager (Existing Midtown)

Unchanged responsibilities:
- Worker dispatch via worktrees
- Git merge queue
- Basic campfire I/O for directives
- Budget tracking (time-based)
- Silence detection

The session-manager does NOT gain memory, context pipeline, telemetry, or automaton lifecycle management.

### Automaton-Manager (New Binary)

A new Go binary that reuses shared infrastructure from `internal/worker` (extracted from `internal/director`).

**Reused from Midtown (no modification):**
- `WorkerManager`: worktree creation, process spawning, process monitoring, `onWorkerExit` hook
- `MergeQueue`: branch serialization, conflict detection
- `AgentRoster` + `AgentEntry`: per-type keypairs, config loading
- `CommandFactory`: `CreateWorkerProcess`, `CreateWorktree`, `RemoveWorktree`
- `PromptBuilder` + `PromptExtension`: prompt assembly from extensions
- `Monitor`: silence detection

**Automaton-specific (new code in `internal/automaton/`):**
- `SigningProxy`: campfire allowlist enforcement + Ed25519 signing (~100 lines)
- `ContextService`: reads memory campfire via views, assembles WorkContext
- `FastLoopEngine`: reads telemetry campfire, writes adaptation messages (~300 lines, pure rules, no LLM)
- `CuratorClient`: Anthropic API call that writes results as campfire messages
- `SkillEngine`: wraps existing engage logic with telemetry capture, convergence checking, and parameter resolution (~580 lines)

The MemoryService, MemoryMerger, TelemetryWriter, MembershipRegistry, ChainDepthTracker, and IntentGraph that a pre-campfire design would require are all REPLACED by campfire read/write with appropriate tags and filters. They do not need custom code because the campfire protocol provides the primitives.

**Net result:** 5 services, each thin — SigningProxy, ContextService, FastLoopEngine, CuratorClient, SkillEngine. ~1,000 fewer lines of custom code compared to a non-campfire approach, replaced by ~200 lines of filter/view configuration on standard campfires.

### Shared Infrastructure: `internal/worker` Package

**Prerequisite refactor** (~1 day): extract `WorkerManager`, `MergeQueue`, `CommandFactory`, `PromptBuilder` from `internal/director` into `internal/worker`. Both `session-director` and `automaton-manager` import `internal/worker`.

### The Automaton-Manager's Own RPT Triple

The automaton-manager is itself an RPT product. Its users are automata (AI-App edge) and the operator (Human-App edge).

**Capability:** Fleet lifecycle management, task routing, budget enforcement, cross-automaton coordination, memory and context optimization for all managed automata, skill execution and optimization.

**Instrumentation (six signals — see Section 12):** Dispatch latency, curator success rate, merge conflict rate, gate:human frequency, token waste ratio, component count (simplicity pressure).

**Configuration surface:** Routing weights, escalation thresholds, fleet composition parameters, curator parameters, concurrency limits, view definitions, skill parameter presets — all adaptable without redeployment. All configuration changes are campfire messages in the engine's own campfires, creating a full audit trail.

**Three loops (see Section 12):** Fast (per-dispatch, automated rule engine), medium (daily/weekly, operator-reviewed dashboard), slow (weekly+, operator-approved structural changes).

---

## 4. The Campfire Substrate

### Why Campfire-Native

This is the defining architectural decision, and it demands justification beyond "campfire is the only protocol we have."

**The structural isomorphism is exact.** An append-only memory log is a campfire message sequence. Log entry types are message tags. Cross-references are antecedents. View materialization is filter configuration. Decay, reinforcement, and contradiction are messages with antecedent references. This is not a metaphor — it is a direct mapping. Every operation the automaton performs on its internal state maps one-to-one to a campfire primitive.

**One schema, one interface, one identity model.** Without the campfire substrate, the design would require seven state substrates: JSONL files (memory, telemetry), IPC sockets (hot state), JSON files (handoff, membership), config files, and campfire messages (external communication). Each with its own read/write interface, its own concurrency model, its own identity story. The campfire substrate collapses all seven into one: signed, timestamped, tagged, antecedent-tracked, persistent, filterable messages.

**Instrumentation becomes native.** Every campfire message is identity-attributed (who), timestamped (when), categorized (tags — what type), causally linked (antecedents — responding to what), persistent (audit trail), and filterable (queryable). These six properties are exactly what RPT's instrumentation requires. The five AI-App edge signals are all measurable from campfire message metadata. No separate telemetry substrate.

**Concurrency is improved, not just preserved.** JSONL concatenation is order-dependent (later entries win on same fact_id). The campfire DAG model is order-independent — antecedents define relationships explicitly. Two instances that both observe the same fact and write memory:standing messages are linked via antecedents to the source, not via file position.

**The automaton eats its own cooking.** The automaton uses campfire internally (memory, telemetry, intent) and externally (coordination, discovery). One protocol. One security model (signing proxy). One identity model (Ed25519). The skills an automaton develops for network participation apply equally to its own internal operations.

### The Adversary's Acceptance Conditions

The campfire-native substrate model required three protocol spec additions before it could be accepted as viable:

1. **Tag-filtered reads** — Without tag filtering, every memory campfire read fetches all messages and filters in the application. Unacceptable at scale. Resolution: `cf read --tag <tag>` (50-80 lines, purely additive, already queued).

2. **Observer/writer/full membership roles** — Without read-only membership, any campfire member can write. The automaton-manager should observe but not write to memory campfires. Resolution: Three roles enforced by the signing proxy in Phase 1, transport-enforced in Phase 2. The role column already exists in the schema but is not enforced.

3. **`campfire:compact` — Compaction events** — Without compaction, memory campfires grow without bound. A 6-month automaton at 5 writes per minute accumulates 1.3 million messages. Resolution: A campfire-signed message that declares "messages M1 through Mn are superseded by this summary." Preserves append-only semantics — no messages are deleted, readers skip superseded ranges.

Once these three additions were shown to be feasible, the substrate objection was formally withdrawn: the campfire-native substrate model is viable, and the permanent engineering constraints (Section 20) are properties of recursive self-optimization, not of the message substrate.

### The Four Internal Campfires

All internal campfires use filesystem transport (same machine, ~1-5ms latency per read). The signing proxy covers all of them.

**1. Memory Campfire** (private, members = automaton + curator)

The durable knowledge store. Standing facts, episodic memory, handoff states, compaction snapshots — all as tagged messages.

| Tag | Purpose |
|-----|---------|
| `memory:standing` | Durable facts with confidence and category |
| `memory:proposed` | Worker-written, pending curator review |
| `memory:episodic` | Session narratives and context summaries |
| `memory:reinforce` | Increases weight of referenced antecedent |
| `memory:contradict` | Overrides referenced antecedent with new content |
| `memory:decay` | Decreases weight of referenced antecedent |
| `memory:anchor` | Marks referenced antecedent as decay-exempt (permanent) |
| `memory:snapshot` | Compaction checkpoint — materialized view at a point in time |
| `context:handoff` | Curator's session summary for the next instance |

Instances write to the memory campfire directly as writer-role members. The curator reviews proposed-tier entries and promotes or discards. The automaton-manager reads as an observer. Compaction (via `campfire:compact`) keeps the campfire bounded — new instances read from the latest compaction snapshot forward.

**2. Telemetry Campfire** (private, members = automaton + automaton-manager)

| Tag | Purpose |
|-----|---------|
| `telemetry:session` | Structured session record (tokens, time, quality, origin) |
| `telemetry:desire-path` | Tool, memory, or task generation desire paths |
| `telemetry:adaptation` | Fast loop parameter change with antecedent to trigger |
| `telemetry:rollback` | Reverted adaptation with antecedent to the adaptation |
| `telemetry:skill-invocation` | Top-level skill execution record |
| `telemetry:skill-step` | Per-step record within a skill invocation |
| `telemetry:skill-composition` | Links parent skill to child skill invocation via antecedent |
| `signal:*` | RPT signal observations (see Section 12) |
| `filter:report` | Filter-generated pattern discoveries |

The statistical process controller reads this campfire with a rolling-window filter (last N sessions). The medium loop reads with a wider window. Rollback is a message tagged `telemetry:rollback` with antecedent to the adaptation being rolled back.

**3. Intra-Automaton Campfire** (private, members = all instances of the automaton)

| Tag | Purpose |
|-----|---------|
| `discovery` | Cross-instance real-time findings |
| `status` | Instance status updates |
| `memory:hot` | Operational facts requiring immediate visibility |

Replaces both the hot state IPC and the instance-to-instance communication channel. The latency cost (1-5ms vs. 0.1ms for IPC) is acceptable — hot state access frequency is low (single-digit calls per session) and no hot-path operations depend on it.

**4. Intent Campfire** (private, members = automaton + automaton-manager)

| Tag | Purpose |
|-----|---------|
| `intent:proposed` | Second-order task proposal from an instance |
| `intent:fulfilled` | Automaton-manager created the rd task (includes rd task ID) |
| `intent:escalated` | Task escalated to operator (gate:human) |
| `intent:rejected` | Task rejected with reason |
| `intent:self-change` | Engine proposes changes to its own architecture (always gate:operator) |

The intent graph IS the message DAG in this campfire. Chain depth is computable from the antecedent chain. The automaton-manager evaluates proposed intents and fulfills, escalates, or rejects them.

### How campfire:compact Makes It Work

`campfire:compact` is a signed compaction event: a special message tagged `campfire:compact` whose payload contains a materialized snapshot of the campfire state. Messages superseded by the compaction are marked as such — they are not deleted from the log (preserving append-only semantics), but readers skip them and read the summary instead.

```
CompactionEvent {
  tag: "campfire:compact"
  payload: {
    supersedes: [uuid]          # IDs of messages being compacted
    summary: bytes              # the compacted content (materialized view)
    retention: "archive"|"discard"
    checkpoint_hash: bytes      # Merkle root of superseded messages
  }
  antecedents: [uuid]           # references the last message being compacted
  signature: bytes              # campfire key signs this
}
```

Key properties:
- Compaction is an event, not a deletion. The append-only guarantee holds.
- The checkpoint_hash allows verification — anyone with the originals can confirm the compaction is honest.
- New members joining start from the latest compaction snapshot, not the full history.
- Compaction is rare housekeeping triggered by message count threshold (default: every 1000 messages). It handles storage growth on a weekly/monthly cadence. It is NOT part of any performance-critical loop.
- The DAG handles context traversal — follow antecedents to find relevant messages. Compaction handles storage growth, not query performance.

### How campfire:view Makes It Work

A view is a named, campfire-defined read pattern — a materialized query over the message stream.

```
ViewDefinition {
  tag: "campfire:view"
  payload: {
    name: string              # e.g., "standing-facts", "recent-telemetry"
    predicate: expression     # filter over message fields
    projection: [string]      # which fields the view returns
    ordering: expression      # sort expression
    limit: uint               # max results
    refresh: "on-read"|"on-write"|"periodic:duration"
  }
  signature: bytes            # campfire key signs this
}
```

Views make the four-campfire model self-describing:
- Memory campfire: `standing-facts` (high-weight current facts), `recent-episodes` (last 24h episodic)
- Telemetry campfire: `current-signals` (last N signal messages), `anomalies` (signals outside 2-sigma)
- Intra-automaton: `instance-status` (latest per instance), `pending-handoffs`
- Intent campfire: `pending-proposals` (open intent:proposed messages)

The view materializer becomes a `campfire:view` definition rather than custom code. The materialization parameters (decay rates, boost factors, relevance weights) become the view's predicate and ordering expressions — the configuration surface that RPT demands.

### Transport Choice

All internal campfires use filesystem transport. They are on the same machine. The campfire data lives at:

```
<automaton-dir>/campfires/
  memory/          <- memory campfire (filesystem transport)
  telemetry/       <- telemetry campfire
  intra/           <- intra-automaton campfire
  intent/          <- intent campfire
```

External campfires use whatever transport the external campfire specifies (filesystem for same-machine, P2P HTTP for cross-machine).

---

## 5. Memory Architecture

### Why Append-Only

Memory is an append-only sequence of campfire messages. No mutations. No weight updates in place. No pruning by deletion. Every memory operation — observe, reinforce, contradict, anchor, decay — is a new message with an antecedent reference to the fact it modifies.

**Why this model:**
- Concurrency solved for free — append-only logs have no write conflicts. Instance merge is trivial: instances write directly to the memory campfire, no merge pass needed.
- Full audit trail by construction — the weight of a fact is the sum of recorded events, not a mystery number. "Why did fact X drop out?" — trace the reinforce/decay message history.
- RPT-compliant: preserves signal for desire path detection. When a worker searches for a fact that exists below weight threshold, the mutable store would have deleted it. The log model preserves it — the `telemetry:desire-path` can detect "worker searched for X which exists in the log but was below weight threshold." The fact should be boosted, not deleted.
- Git-friendly: campfire message files diff cleanly.
- Cross-automaton reinforcement: if automaton B finds a fact useful, it can reinforce it via the campfire protocol's standard messaging.

### Three Memory Tiers as Tags

**Tier 1 — Standing Knowledge** (`memory:standing`)

Durable facts. Decisions, learned behaviors, tool configurations, cross-session state. Written by the curator after session boundary analysis. Each fact has a `fact_id`, `content`, `confidence` (high/medium/low), `category`, and domain `tags`.

```json
{
  "tag": "memory:standing",
  "payload": {
    "fact_id": "jwt_algorithm",
    "content": "HS256, selected for simplicity over RS256",
    "confidence": "high",
    "category": "auth",
    "source_task": "bead-abc",
    "tags": ["auth", "jwt"]
  }
}
```

**Tier 2 — Hot State** (`memory:hot` in the intra-automaton campfire)

Operational facts requiring immediate cross-instance visibility: credential rotations, active alerts, topology changes. Written by instances to the intra-automaton campfire. Read latency is 1-5ms (filesystem transport) — acceptable for the access patterns (session start, occasional mid-session).

**Tier 3 — Proposed** (`memory:proposed`)

Worker-written memory entries during a session. Visible to the current session and to the curator for review. NOT promoted to standing until the curator validates. The curator is the trust boundary — the only path from "worker claims this is true" to "the automaton believes this is true."

### The View Materializer as campfire:view

A `campfire:view` named `standing-facts` replaces the need for a custom `ViewMaterializer`. The view definition encodes the materialization logic:

- Messages tagged `memory:standing` pass with weight = confidence * decay_factor
- Messages tagged `memory:anchor` pass with weight = 1.0 (decay exempt)
- Messages tagged `memory:contradict` cause the antecedent to be re-weighted
- Messages tagged `memory:reinforce` boost the antecedent's weight
- Messages tagged `memory:decay` suppress the antecedent
- Messages below weight threshold are excluded from the view

**Materialization parameters (all configurable, all subject to fast-loop optimization):**

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| Primary decay rate | 0.9^sessions | Unreferenced primary entries survive ~22 sessions before pruning |
| Derived decay rate | 0.8^sessions | Derived entries survive ~9 sessions — prevents low-confidence facts from gaining authority through derivation |
| Reinforcement boost | +0.10 per reference | Resets decay clock on reference |
| Pruning threshold | 0.10 weight | Below this, facts are excluded from the view |
| Weight by confidence: high | 0.75 | Context-lab validated confidence mapping |
| Weight by confidence: medium | 0.50 | |
| Weight by confidence: low | 0.25 | |
| Derived entry multiplier | 0.75x | Prevents authority inflation through derivation chains |

### Memory Entry Lifecycle

```
1. Worker discovers fact during task execution
2. Worker writes memory:proposed message to memory campfire
3. At session boundary, curator reviews proposed entries against transcript
4. Curator promotes valid entries to memory:standing (or discards)
5. Standing entries have initial weight = confidence mapping (high=0.75, med=0.50, low=0.25)
6. Derived entries (synthesized from other memories) receive 0.75x weight multiplier
7. Each session without reference: weight *= decay_factor (0.9 primary, 0.8 derived)
8. Each reference: weight += 0.10, decay clock resets
9. weight < 0.10: entry excluded from standing-facts view (not deleted — still in log)
10. Critical decisions: anchored via memory:anchor (decay-exempt, weight = 1.0)
```

**The feedback loop guard:** Without it, a low-confidence fact ("I think the timeout is 30s") can propagate through derived entries until it appears highly authoritative. The derived entry multiplier (0.75x) and faster decay (0.8 vs 0.9) prevent this. Derived entries cannot outweigh their source entries.

### Decay, Reinforcement, and Permanence

Decay and reinforcement are explicit messages, not computed side effects. When an instance references a standing fact, it writes a `memory:reinforce` message with an antecedent to the fact. When the fast loop determines a fact is stale, it writes `memory:decay`. The view materializer reads these messages as inputs alongside the original facts.

**Permanence:** The `memory:anchor` tag marks a fact as decay-exempt. Critical architecture decisions, operator directives, and identity facts get anchored. Without anchoring, an unreferenced architecture decision decays to pruning threshold in ~22 sessions (~4.4 days at 5 sessions/day). Anchored facts maintain their weight regardless of reference frequency.

### Contradiction Handling

When the view materializer detects a contradiction (two entries with the same `fact_id` but different `content`, via `memory:contradict` messages):

1. Both entries survive in the materialized view, marked with `conflict: true`
2. A campfire message is posted to the automaton-manager's supervision channel
3. An rd task is created with `gate:human`, assigned to the operator
4. Resolution is explicit: operator reviews and resolves. No automatic last-writer-wins for genuine contradictions.
5. For supersessions (credential rotation where newer value is definitively correct), timestamp + confidence serve as tiebreaker automatically.

**Fact ID consistency:** Each automaton's config.toml defines a `[memory.vocabulary]` section — a list of canonical fact_ids for the domain. The curator's system prompt includes this vocabulary as the preferred naming scheme. New fact_ids outside the vocabulary are allowed but flagged with `uncontrolled: true`. The controlled vocabulary narrows the blast radius of semantic contradiction (two facts about the same thing with different fact_ids).

**Known limitation:** Semantic contradiction detection (two facts about the same thing with different `fact_id`s) remains a hard problem. The controlled vocabulary narrows the blast radius. The scaling path is embedding-based similarity detection at write time — two facts with embedding distance < 0.15 treated as about the same thing.

### Cross-Automaton Memory Mesh

The campfire protocol's recursive composition enables organic knowledge sharing: automaton A's memory campfire can admit automaton B as an observer-role member. B reads A's curated facts. A reads B's.

This is the most powerful emergent pattern — but it raises questions about privacy (operator secrets in memory), trust (adversarial knowledge injection), and scale (large DAGs). The mechanism is designed but not enabled by default. Requirements: protocol-level compaction for shared memory campfires, transport-enforced observer roles, knowledge provenance tracking.

---

## 6. The Curator

### What It Is

A Sonnet-tier API call (not an agent loop) that fires at session boundary to extract structured facts from the session transcript. The curator is the most critical component in the memory architecture — it is the bridge between ephemeral session work and durable knowledge.

### Why Sonnet

Context-lab experiments (30-step sessions, Haiku worker, 40K token budget, 6 recall probes per session) proved model selection is counter-intuitive:

| Curator Model | Feature Build Recall | Debug Marathon Recall | Quality Delta | Cost Premium |
|---------------|---------------------|----------------------|---------------|--------------|
| Naive compaction (baseline) | 0.58 | 0.53 | -1.55 (debug) | baseline |
| Haiku curator | 0.97 | 0.93 | -1.28 avg | +36% tokens |
| **Sonnet curator** | **0.94** | **0.83** | **-0.20** | **+34% tokens** |
| Opus curator | 0.97 | 0.69 | -0.93 | +20% tokens |

**Sonnet is the correct tier.** Three findings explain why:

1. **Opus over-abstracts.** When told to extract facts, Opus applies interpretive reasoning, smoothing specific details into higher-level insights. The recall probes test specific facts ("what password hashing algorithm and why?"), not high-level understanding. Opus recall on the debug marathon is 0.69 — worst of all curated strategies.

2. **Haiku preserves facts but loses reasoning structure.** Haiku achieves the best raw recall (0.93-0.97) but the worst quality trajectory (delta -0.91 to -1.66). It produces "put everything in a list" — high fidelity for factual content, poor fidelity for causal structure and phase awareness.

3. **The task is formulaic extraction, not reasoning.** Extracting key-value pairs from conversation does not benefit from Opus-level reasoning. It benefits from disciplined, literal extraction. Sonnet hits the middle ground — capable enough to produce structured output reliably, not so powerful that it reformulates rather than preserves.

### What It Produces

A `HandoffState` structured payload written as campfire messages:

- `memory:standing` messages — key-value facts extracted from the session (the context-lab format)
- `context:handoff` message containing:
  - `phase_summary` — what happened in the session (structured, not prose)
  - `active_work` — what was in progress at session end
  - `decisions[]` — each with `what` + `why`
  - `files_modified[]`
  - `test_state` — what passed, what failed
  - `next_step` — exact next action for continuation
  - `campfire_context_summary` — state of active campfire conversations

### What "Structured Facts" Means Concretely

Naive compaction produces prose: *"Earlier in the session, the team discussed authentication approaches and selected a hashing strategy."* This is true but useless for recall — the specific values are gone.

Structured curation produces discrete entries:
```
password_hashing: bcrypt, rounds=12, chosen over argon2 for container compatibility
jwt_algorithm: HS256, selected for simplicity over RS256
database: PostgreSQL 15, not MongoDB, analytics requirements drove relational choice
```

These survive indefinitely because they are **additive, not compressive**. Each curation adds entries; it does not replace prior entries with a summary of them. After 6 curation cycles, the system prompt has 30 turns of facts at low token cost, not a single lossy prose summary.

### The 5-Turn In-Session Curation Cadence

Based on experiment results, curation every 5 turns is the validated interval:
- Enough new content to curate meaningfully
- Frequent enough to prevent large knowledge gaps
- Post-curation quality of 3.80-4.00 (Sonnet) — the curation events themselves do not cause quality dips
- Curator token overhead: ~6,238 tokens per curation call (context-lab measured average)

For the session-boundary curator (the primary mechanism), curation fires once per session. The ~6,238 token cost is amortized across the entire session. For short sessions (<15 turns) with simple tasks, the curator can be skipped (configured via `curator_threshold` in config.toml).

### Curator Fallback

The curator is an HTTP call in the post-session chain. If it fails:

1. Retry: 3 attempts with exponential backoff, total timeout = 30 seconds
2. If all retries fail: write a minimal synthetic handoff containing only `files_modified` (from git diff), `session_id`, `timestamp`, and `curator_failed: true`
3. The next worker spawns with degraded handoff state, which is better than no handoff state
4. The degraded handoff is logged as a `telemetry:session` event with `curator_status: failed`

### Curator as RPT Product

The curator has its own RPT triple:

**Capability:** Extract structured facts from session transcripts.

**Instrumentation:** For each session N+1, check which standing memory facts from session N were actually referenced in the worker transcript. Referenced fact = reinforcement event. Unreferenced fact from two sessions ago = potential miss. Track miss rate by fact category.

**Configuration surface:** The curator's extraction prompt is adaptable without redeployment. Fact categories can be tuned based on which facts actually get referenced in subsequent sessions. The fast loop demotes under-referenced categories and promotes high-reference categories in the curator's extraction prompt.

---

## 7. Context Management

### The Context Pipeline

Context is loaded into a worker session at spawn time. The pipeline runs in the automaton-manager, reading from campfires:

```
1. Load automaton spec (.claude/agents/<type>.md)              -- Priority 0 (identity)
2. Load constraints (task scope, budget, quality thresholds)    -- Priority 0 (identity)
3. Read memory campfire view "standing-facts" for task-relevant tags -- Priority 1 (standing)
4. Read context:handoff from memory campfire (last handoff)     -- Priority 1 (recent)
5. Read memory:hot from intra-automaton campfire                -- Priority 1 (recent)
6. Read last N messages from active external campfires          -- Priority 1 (recent)
7. Load tool manifest (from config.toml [tools] section)        -- Priority 2 (ambient)
8. Assemble into WorkContext -> PromptBuilder -> system prompt
```

**Priority tiers guide eviction.** When the context window fills, lowest-priority entries evict first. Priority 0 entries (spec, constraints) are physically separated and never evicted.

**Budget:** 200K token context window. Reserve 4,000 for handoff, 2,000 for tool manifest, 1,000 for constraints, 500 for spec header. Leaves ~192,500 for work.

### Predictive Loading via campfire:view

The `standing-facts` view on the memory campfire selects facts matching the current task's domain tags. The view's ordering sorts by weight (recency-biased via decay). The view's limit caps token usage.

**Selection mechanism:** Keyword matching on task description against memory entry tags and `fact_id` fields, with recency bias (entries from recent sessions weighted higher via lower decay), and a weight threshold of 0.30 (filters noise).

Keyword matching is the weakest link (no empirical validation at current stage). Acceptable at current scale (hundreds to low thousands of entries). The fast loop compensates: if workers miss relevant facts in consecutive sessions, the fast loop adjusts the view's predicate to include missing tag categories.

**Scaling path:** Replace keyword predicates with vector-indexed retrieval when entry count exceeds ~5,000.

### Hint-Driven Context Warming

During a session, the worker's completions contain signals that influence context for subsequent turns. The hint system operates on two channels:

**Explicit directives** (sigil protocol, 241 tokens of system prompt cost):
- `mu[scope/category]: fact` — memory write to proposed tier
- `kappa[scope/topic]: query` — context request (warm relevant context)
- `delta[scope/topic]: uncertainty` — flag uncertainty, warm context
- `iota[scope/topic]: need` — information need, warm + record gap
- `rho[id]: reinforcement` — boost referenced memory entry
- `phi[id]: forget` — weaken to pruning threshold

**Implicit signals** (pattern matching on natural language):
- Uncertainty markers: "I'm not sure about...", "probably..."
- Missing info: "I need to check...", "without knowing..."
- Context requests: "What was the decision on..."
- Contradiction: "Actually, that's wrong...", "Correction:..."

**Directive reliability by model tier:**

| Model | Expected Reliability |
|-------|---------------------|
| Opus | 97%+ |
| Sonnet | 94%+ |
| Haiku | 88%+ |

The `HintDemuxer` stream processor intercepts worker output and routes directives as campfire messages:
- Memory writes (`mu`) — write `memory:proposed` to memory campfire
- Context requests (`kappa`) + uncertainty (`delta`) — write context warming request to intra-automaton campfire
- Information needs (`iota`) — both: warm context AND record the gap
- Reinforcement (`rho`) — write `memory:reinforce` to memory campfire
- Forget (`phi`) — write `memory:decay` to memory campfire

Worker-written entries go to the proposed tier. The curator reviews and promotes or discards. This is the trust boundary — a worker with a strong completion drive cannot unilaterally inject false memories into the automaton's knowledge base.

**Explicit directives suppress implicit detection** for the same topic — no double-processing. The demuxer implements multi-level fallback: tries Level 2 sigil first, falls back to Level 1 XML if no match, then falls back to implicit detection.

**The feedback loop:** What the worker asks about determines what the automaton-manager prepares for the next turn's context. The worker does not know this is happening — it just sees more relevant context appearing. This is mechanical, not prompt-based.

**Injection mitigation:** The HintDemuxer applies strict parsing: directive content is sanitized (stripped of non-printable characters, length-capped at 256 chars per value, fact_id validated against regex `[a-z][a-z0-9_]{1,63}`). Memory entries from directives are tagged `source: worker_directive` and loaded with lower weight than curator-produced entries.

### Session Rotation

Context rotation fires when the session reaches 70% of the context window, before Claude's built-in compaction can trigger. The PreCompact hook exists but cannot block compaction — rotation must preempt it.

**Why this threshold matters:** Context-lab proved that catastrophic compaction is the terminal failure mode. Naive compaction scored **0.00 recall at turn 25 of the debug marathon** — complete amnesia. After two compaction events, the second one compressed ~40K tokens of context down to 1,900 tokens. The model had no usable knowledge of bugs it had fixed 15 turns earlier. This is not a tail risk — it is the expected behavior when compaction fires repeatedly.

**Context pressure detection:** Workers report context usage in rd progress messages: `rd progress --notes '{"context_pct": 72}'`. The automaton-manager's existing campfire watcher reads progress messages and extracts context_pct. This is pull-through-campfire, not stdout interception.

**Rotation sequence:**
1. Automaton-manager detects context pressure > 70% (via rd progress reports)
2. Worker is signaled to checkpoint (commit WIP, call `rd progress`)
3. Worker exits cleanly (code 0)
4. Automaton-manager runs the Sonnet curator
5. Curator writes `context:handoff` message + `memory:standing` messages to memory campfire
6. Automaton-manager spawns a new worker with context loaded from campfire views

This prevents the catastrophic compaction failure mode. The Phase 1 finding sharpens why: in a 55-step session, naive compaction produced 44 blank responses (70% blank rate, requiring 3.3 retries each) vs. 3 blank responses (14% blank rate) for active curation. First-attempt recall is the operationally relevant metric for an automaton.

---

## 8. Campfire Participation

### Direct Participation

Instances are direct campfire participants. They hold the automaton's keypair via signing proxy, join campfires, send and read messages directly. The session-manager is not an intermediary — the signing proxy is the operator's enforcement mechanism.

### Three Campfire Layers

**Layer 1 — External/Project Campfires**

One instance per automaton per campfire. Direct participation with signing proxy enforcement. This is where automata interact with the broader network — other operators' agents, working groups, shared project campfires.

The one-instance-per-external-campfire rule prevents contradictory responses (two instances of the same automaton speaking as one entity in the same conversation).

**Layer 2 — Intra-Automaton Campfire**

A private campfire accessible only to instances of the same automaton. Instances post discoveries and status. Other instances read for cross-instance awareness. This provides real-time cross-instance learning without the concurrent-instances-same-campfire problem.

**Layer 3 — Hypernetwork Campfires**

Cross-automaton coordination at the fleet and network level. The automaton-manager's supervision campfire, working group campfires, inter-project campfires, and the AIETF coordination campfire.

### The Signing Proxy

The instance does NOT have the raw private key. The automaton-manager runs a lightweight Unix socket service — the signing proxy (~100 lines). The instance calls `cf` with `CF_SIGNING_SOCKET` env var.

The proxy:
1. Receives the message to sign
2. Checks the target campfire against the allowlist in `config.toml`
3. If allowed: signs with the automaton's Ed25519 key, returns the signed message
4. If not allowed: rejects; the rejection is logged as a desire path event

**What this prevents:** Joining unauthorized campfires (allowlist), raw key exfiltration (key never in worker env), shadow campfire participation (all signing visible).

The signing proxy covers ALL campfires — internal and external. Internal campfires are threshold=1 (single automaton identity). External campfires use whatever threshold the campfire specifies.

### Membership Lifecycle

Campfire memberships persist across instances. When the automaton-manager dispatches a new instance (after a prior instance dies), it reads the membership state from its own campfires — the campfire protocol's membership events are the registry. No separate MembershipRegistry needed.

**Conversation resumption:** The new instance receives campfire join list in boot context + last N messages from each campfire (configurable per campfire in config.toml) + the curator's `context:handoff` message with `campfire_context_summary`.

### Observer Roles for Memory Campfires

| Member | Role | Can Do |
|--------|------|--------|
| Automaton instances | writer | Write memory facts, read via views |
| Curator | writer | Write standing facts, promote/discard proposed |
| Automaton-manager | observer | Read all messages, cannot write |
| Other automata | observer | Read curated facts for knowledge mesh |

Observer enforcement is client-side in Phase 1 (signing proxy refuses to sign writes to campfires where the member has observer role). Transport-enforced roles require the observer/writer/full spec change.

### The Opaque Edge

Per the AIETF charter: "You can never know what an operator tells their agents." The signing proxy is the operator's enforcement mechanism. External agents have no way to distinguish operator-directed from autonomously-generated messages. This is by design. The architecture acknowledges explicitly: the signing proxy is the operator's proxy; campfire messages represent the operator's intent, not the worker's raw output.

---

## 9. Autonomy & Second-Order Intent

### Second-Order Intent

Automata generate follow-on work from discoveries, decisions, new information, and tool use. Workers have `rd` on PATH and can call `rd create`. The automaton-manager governs what happens to those tasks through the intent campfire.

**How it works:** A worker writes an `intent:proposed` message to the intent campfire. The automaton-manager evaluates the proposal and posts one of:
- `intent:fulfilled` — created the rd task (includes rd task ID)
- `intent:escalated` — escalated to operator (gate:human)
- `intent:rejected` — rejected with reason

### Chain Depth

Every generated task carries a provenance chain. The intent campfire's message DAG IS the intent graph — chain depth is the antecedent chain length.

```toml
[autonomy]
max_chain_depth = 3           # Levels of follow-on before gate:human
max_tasks_per_session = 10    # Task creation cap per worker session
```

**When chain depth exceeds the threshold:**
- The task IS created (the instance is not frustrated)
- The task is created with `gate:human`, assigned to the operator
- The entire provenance chain is surfaced: "This task is the result of a 4-deep chain starting from [original task]. Review for scope creep."

**Global chain depth tracking:** Chain depth is tracked GLOBALLY across campfire-originated work, not per-task-chain. Without this, lateral campfire expansion could bypass depth limits. Chain depth is computable from the intent campfire's antecedent chains.

### Budget Inheritance

Second-order tasks draw from the **operator's intent-level budget pool** (see Section 15), not the creating instance's budget. The creating instance records the task creation in its telemetry campfire. The budget draw happens when the child task is executed.

### Escalation Thresholds

```toml
[autonomy.escalation]
chain_depth_threshold = 3
budget_threshold = 50000      # Tasks estimated > 50K tokens require approval
scope_keywords = ["security", "production", "delete", "deploy"]
campfire_origin_depth = 1     # Campfire-triggered tasks escalate at lower depth
```

Escalation thresholds are themselves RPT configuration surfaces, subject to three-loop optimization:
- **Fast loop:** If self-generated tasks consistently produce low-value work, tighten
- **Medium loop:** Track operator approval/override/rejection rates per category
- **Slow loop:** Widen scope when generated tasks consistently produce high-value fixes

### Trust Calibration Loop

**Measured signals:**
- Approval rate: fraction of escalated items approved without modification
- Override rate: fraction modified by the operator before approval
- Rejection rate: fraction rejected entirely

These are Human-App edge signals for the trust relationship. The medium loop tracks them. The slow loop recommends threshold adjustments:

- High approval, low override — recommend loosening (more autonomy)
- High rejection — recommend tightening (less autonomy)
- Slow operator response — recommend reclassifying some categories as autonomous

This is the Peter Principle mechanism from RPT: automata absorb work that would push the operator past their capacity. The boundary moves continuously based on demonstrated judgment quality.

### Completion Drive Mitigation

Circuit breakers (all mechanical, read-only during execution):
1. Chain depth cap — enforced by automaton-manager via intent campfire
2. Task creation budget per session — max N tasks per worker session
3. Budget inheritance through the intent graph — finite pool
4. Scope/budget/chain-depth thresholds cannot be modified during execution
5. Workers write to proposed memory tier — curator is the trust boundary
6. Independent output validation — validation criteria read-only during session

Per RPT Section 3.1: "CLAUDE.md rules are text. An agent with a strong completion drive will rationalize past text." All mitigation is mechanical, not prompt-based. The signing proxy, the proposed memory tier, and the budget enforcement are physical constraints the worker cannot bypass.

---

## 10. Tool Management

### Tool Manifest

```toml
[tools]
static = [
  { name = "rd", description = "Work item management", path = ".rudi/bin/rd" },
  { name = "cf", description = "Campfire protocol (via signing proxy)", path = "cf" },
  { name = "bd", description = "Beads issue tracking", path = "bd" },
]

[tools.conditional]
code_execution = { match = "implement|build|fix|refactor", tools = ["go", "pytest", "npm"] }
research = { match = "research|analyze|investigate", tools = ["web_search", "web_fetch"] }

[tools.restricted]
destructive = ["git push --force", "rm -rf", "DROP TABLE"]
expensive = ["opus-tier API calls", "web_search (> 10 queries)"]
```

### Tool Tiers

- **Static:** Always available. Core tools the automaton needs for every task.
- **Conditional:** Loaded based on task type keywords. The match field is a regex against the task description.
- **Restricted:** Forbidden during execution. Attempted use triggers a gate:human rd task.

### Forbidden Tool to rd Task

When a worker attempts a restricted tool:
1. The tool call is blocked by the `CommandFactory`
2. The automaton-manager writes an `intent:proposed` message to the intent campfire
3. For security-relevant tools: `gate:human`, P0, assigned to operator
4. For desire path tools (outside manifest but not destructive): `gate:human`, P2
5. The instance does NOT block waiting. The task is failed with `pending_approval`, branch preserved.

### Desire Path Capture

Desire paths are first-class telemetry events, not debug logs:

- **Tool desire paths:** Unexpected tool usage (arguments outside signature, alternative approach after failure, tool not in manifest) — written as `telemetry:desire-path` messages.
- **Memory desire paths:** Worker searches for a fact that exists below weight threshold. The campfire model preserves this — a mutable store could not.
- **Task generation desire paths:** Automaton hits scope/budget/chain-depth limit.

High-frequency desire paths are surfaced through the medium loop. 5+ occurrences of the same pattern triggers promotion candidacy. The fast loop hypothesizes (provisional alias); the medium loop validates (operator reviews and ratifies or discards).

### Token-CPU Routing

Per RPT Section 5.1: "For work both can do, CPU is orders of magnitude more efficient." Every step in an automaton's task flow is classified:

- **Token-work** (requires reasoning, creativity, judgment) — LLM
- **CPU-work** (deterministic, computation, pattern-matching) — dedicated tool

The telemetry tracks tool calls to identify token-work being spent on CPU-work (e.g., a worker doing string manipulation instead of calling a tool, or reading an entire file when a grep suffices). These are surfaced as optimization candidates in the medium-loop dashboard.

---

## 11. Skills

### What Skills Are

Skills are executable, instrumentable, optimizable multi-step orchestration procedures. They sit between tools (atomic operations) and intent (what the operator wants done). Tools are the atoms — `rd`, `cf`, `git`. Skills orchestrate tools, agents, and campfires into repeatable patterns. Intent triggers skills. The optimization engine optimizes skill parameters alongside budget and context parameters.

A skill is a Rudi playbook extended with three additions:

1. **Typed parameters with bounds** — Playbook `[vars]` are string substitution only. Skill `[params]` have type, default, and bounded ranges. The fast loop adapts values within bounds; TOML bounds are hard floors the fast loop cannot breach.

2. **Per-step telemetry hooks** — No telemetry in current playbooks. Skills emit a `telemetry:skill-step` record per step completion written to the telemetry campfire. Fields: `step_id`, `tokens_used`, `outcome_quality`, `convergence_signal`.

3. **Convergence criteria** — Current playbooks use `gate:human` as the only non-sequential terminator. Skills have a machine-evaluable convergence expression checked by the automaton-manager after each iteration.

Skills have the RPT triple:

- **Capability:** The orchestration pattern (e.g., "design team with N agents, M rounds, convergence criteria C")
- **Instrumentation:** Cost per invocation, outcome quality (operator acceptance rate), which sub-steps contributed signal vs. burned tokens, rework rate as a Goodhart counter-metric
- **Configuration surface:** Agent count, disposition selection, round count, model tiers, convergence criteria — all adaptable within operator-defined bounds

### Skill Anatomy

Skills are TOML files extending the Rudi playbook format. They live in the automaton's config directory at `<automaton-dir>/skills/` alongside the automaton's configuration, versioned in git.

```toml
name = "design-team"
description = "Multi-disposition deliberative design with convergence"
version = 1

[params]
agent_count = { type = "int", default = 4, range = [2, 6] }
max_rounds = { type = "int", default = 5, range = [2, 8] }
min_rounds = { type = "int", default = 2, range = [1, 4] }
architect_model = { type = "enum", default = "opus", values = ["sonnet", "opus"] }
disposition_model = { type = "enum", default = "sonnet", values = ["sonnet", "opus"], locked_floor = "sonnet" }
seed_context = { type = "string", required = true }
reference_docs = { type = "list", default = [] }
dispositions = { type = "list", default = ["systems-pragmatist", "adversary", "creative", "rpt-purist"] }

[presets.quick]
agent_count = 2
max_rounds = 3
min_rounds = 1
disposition_model = "sonnet"

[presets.standard]
agent_count = 4
max_rounds = 5
min_rounds = 2
disposition_model = "sonnet"

[presets.thorough]
agent_count = 4
max_rounds = 8
min_rounds = 3
architect_model = "opus"
disposition_model = "opus"

[campfire]
pattern = "ephemeral"

[convergence]
type = "composite"
max_rounds = "{{max_rounds}}"
min_rounds = "{{min_rounds}}"

[convergence.signals.novel_proposals]
weight = 0.4
threshold = 1
measurement = "count messages tagged 'proposal' minus messages tagged 'withdrawal' in the current round"

[convergence.signals.token_volume_ratio]
weight = 0.3
threshold = 0.7
measurement = "total output tokens this round / total output tokens previous round"

[convergence.signals.agent_convergence]
weight = 0.3
threshold = 0.6
measurement = "fraction of agents who posted a message tagged 'converge'"

[convergence.composite_threshold]
value = 0.7

[instrumentation]
per_invocation = ["total_tokens", "wall_clock_seconds", "rounds_to_converge", "operator_acceptance", "rework_within_14d"]
per_step = ["tokens_per_agent", "novel_proposals_count", "withdrawal_count", "resolution_count"]
convergence_metrics = ["value_added_per_round", "marginal_token_cost_per_round"]

[[steps]]
id = "seed"
description = "Create campfire. Post skill charter. No LLM needed."
agent_type = "orchestrator"
model_tier = "none"
min_instances = 1

[[steps]]
id = "deliberate"
description = "Each disposition agent reads the campfire, posts proposals/critiques/withdrawals. Repeat until convergence."
needs = ["seed"]
agent_type = "{{dispositions}}"
model_tier = "{{disposition_model}}"
campfire_role = "multi-member"
iterative = true
convergence = "{{convergence}}"
prompt_template = "design-team/disposition.md.tmpl"
min_instances = 2

[[steps]]
id = "synthesize"
description = "Lead architect reads the full campfire, produces a coherent synthesis document."
needs = ["deliberate"]
agent_type = "architect"
model_tier = "{{architect_model}}"
gate = "human"
prompt_template = "design-team/synthesize.md.tmpl"
min_instances = 1
```

### Convergence Detection

Convergence is evaluated without LLM judgment. The convergence evaluator is a rule engine — same architecture as the fast loop. It reads campfire messages, counts tags, measures token volumes, and evaluates a composite function. ~50 lines.

Three signal types, weighted:

1. **Tag-based (novel proposals):** Net new proposals per round = proposals - withdrawals. When net new < threshold, the signal fires.
2. **Token-volume heuristic:** Round N's total output tokens vs. Round N-1's. When the ratio drops below threshold (default 0.7), the signal fires.
3. **Explicit convergence signal:** Agents post a message tagged `converge`. When >= threshold fraction signal convergence, the skill stops iterating.

### Skill Evolution: Three Layers

**Fast loop (per-invocation, automated):** Selects presets, adjusts parameters within bounded ranges, tracks dual metrics (efficiency + effectiveness), writes `telemetry:adaptation` messages, auto-reverts after 3 invocations without improvement.

**Medium loop (daily/weekly, operator-reviewed):** Surfaces patterns, proposes variants, detects cross-automaton convergence on skill parameters.

**Slow loop (weekly+, always gate:operator):** Proposes structural changes (step addition/removal, new presets). Step elimination is ALWAYS gate:operator.

### Floor Constraints

- **`locked_floor`** on parameters: fast loop cannot reduce below this value.
- **`min_instances`** on steps: fast loop cannot reduce instance count below this floor.
- **Step elimination is slow-loop only:** structural changes to the step graph require `intent:self-change`, which is gate:operator.

---

## 12. The Optimization Engine

### RPT Fast Loop: Statistical Process Controller

The fast loop runs after each task dispatch and completion. It reads the telemetry campfire (last 10 sessions per task type), computes running mean and variance for each of the five AI-App edge signals, and triggers bounded adaptations when deviations exceed 1 sigma.

**The five AI-App edge signals:**

| Signal | Measurement Point | Direction |
|--------|------------------|-----------|
| Token count per completed task | Session telemetry record | Lower = resonating |
| Retry rate | Tool call log (failures / total calls) | Lower = resonating |
| Hedging frequency | Output analysis ("might", "possibly", "seems") | Lower = resonating |
| Decision latency | Tokens between receiving output and committing to action | Lower = resonating |
| Error recovery cost | Tokens spent in repair loops | Lower for clear errors |

**Adaptations (all reversible, all bounded):**
- Token count above model — increase planning budget by 10% (capped at 2x)
- Retry rate above model — log failing tool calls, generate tool alias candidate
- Hedging above model — flag task type for context quality review
- Decision latency above model — increase context loading budget
- Error recovery above model — increase validation budget, decrease repair cap

**Automatic rollback:** Each adaptation written as `telemetry:adaptation` with pre/post signals. If 3 sessions after adaptation the signal has not improved, a `telemetry:rollback` message auto-reverts.

**Implementation:** Pure rule engine, ~300 lines, no LLM.

### The Three Loops

**Fast loop (per-session / per-dispatch, automated):** Statistical process controller, bounded adaptations, preset selection, auto-rollback.

**Medium loop (daily/weekly, operator-reviewed):** Trend analysis across fleet. Surfaces patterns. Aggregates skill telemetry. Detects cross-automaton convergence. Implementation: `resonance-timeline` tool applied to automaton telemetry.

**Slow loop (weekly+, always gate:operator):** Engine observes failure patterns, proposes changes via `intent:self-change`. Every proposal must include both an addition option and a simplification alternative.

### Recursion Boundary

The optimization recursion terminates at Level 3:
- Level 0: Instance optimizing task execution
- Level 1: Automaton optimizing cross-session behavior
- Level 2: Automaton-manager optimizing fleet management
- Level 3: Automaton-manager optimizing how it optimizes (operator-reviewed)
- Level 4+: Does not exist. The operator IS the optimization.

---

## 13. Intent Delivery

Three channels converging on the automaton-manager:

**Channel 1 — rd Tasks (Operator -> Automaton):** Operator creates rd tasks; automaton-manager polls and claims.

**Channel 2 — Campfire Messages (Reactive):** External agent posts message; instance evaluates and writes `intent:proposed`; automaton-manager evaluates and creates rd task.

**Channel 3 — Scheduled Triggers:** `[schedule]` in config.toml: `daily`, `hourly`, `on_event`.

All channels produce rd items claimed by the automaton-manager and dispatched to workers. The worker protocol is uniform regardless of origin.

---

## 14. Worker Protocol

### What Workers Get

Every worker spawns with:
1. A git worktree (isolated working directory)
2. A signing proxy socket (`CF_SIGNING_SOCKET` env var)
3. A context window assembled from campfire views
4. An rd task to execute
5. Access to `rd`, `cf`, `bd` on PATH

### What Workers Produce

1. **Code artifacts** — commits to their worktree branch, enqueued for merge
2. **Campfire messages** — via the signing proxy
3. **Intent proposals** — `intent:proposed` messages
4. **Memory proposals** — `memory:proposed` messages

### Proposed Tier for Memory Writes

Workers CANNOT write directly to standing memory. They write `memory:proposed` messages. The curator reviews at session boundary and promotes or discards. This is the trust boundary — a worker with a strong completion drive cannot unilaterally inject false memories.

---

## 15. Budget Model

### Per-Intent, Not Per-Session

**Variable cost profiles by intent type:**

| Intent Type | Context Loading | Planning | Execution | Validation | Repair (max 3 rounds) | Curator |
|-------------|----------------|----------|-----------|------------|--------|---------|
| Implementation | 15,000 | 10,000 | 100,000 | 15,000 | 30,000 | 6,238 |
| Bug fix | 10,000 | 5,000 | 50,000 | 15,000 | 20,000 | 6,238 |
| Security scan | 15,000 | 5,000 | 80,000 | 10,000 | 10,000 | 6,238 |
| Conflict resolution | 20,000 | 0 | 40,000 | 10,000 | 15,000 | 6,238 |
| Campfire-triggered | 10,000 | 5,000 | 30,000 | 10,000 | 10,000 | 6,238 |

### Budget Enforcement: Local-First, Campfire-Reconciled

Budget enforcement is LATENCY-SENSITIVE. Solution: local counter initialized from last campfire budget message, decrements locally (zero latency), reporting async to campfire. Hard caps include safety margin.

---

## 16. Required Campfire Spec Changes

### P0 — Must Ship First

**1. Tag-Filtered Reads** — [SHIPPED]

- **What:** `cf read --tag <tag>` filters messages by tag before returning.
- **Implementation cost:** 50-80 lines. SQL-level `json_each()` predicate on the tags column. No schema migration.
- **Status:** Shipped. `cf read --tag` and `--fields` projection are live.

**2. Observer/Writer/Full Membership Roles** — [SHIPPED]

- **What:** Three roles on campfire members. Observer can read only. Writer can read and write messages. Full additionally holds campfire key material and can sign `campfire:*` messages.
- **Implementation cost:** 60-80 lines for client-side enforcement. Transport-enforced roles are P2.
- **Status:** Shipped. `cf admit --role` is live with client-side enforcement in `role.go`. Legacy members default to `full` for backward compatibility.

### P1 — Required for Production

**3. `campfire:compact` — Compaction Event** — [SHIPPED]

- **What:** A campfire-signed message that declares "messages M1 through Mn are superseded by this summary." Preserves append-only semantics.
- **Status:** Shipped for filesystem transport. P1 limitation: `compact.go` calls `sendFilesystem()` directly. GitHub and HTTP transport campfires cannot use `cf compact` until P2.

**4. `campfire:view` — Named Materialized Read Patterns** — [SHIPPED]

- **What:** A campfire-signed message defining a named view: predicate, projection, ordering, limit, refresh strategy.
- **Predicate grammar:** S-expressions supporting `tag`, `sender`, `and`, `or`, `not`, `gt`, `lt`, `eq`, `field`, `literal`. Predicate depth is bounded.
- **Status:** Shipped for local use. P1 limitation: `view.go` stores the `campfire:view` message in the local SQLite store but does not write it to the transport. Other agents in the same campfire cannot see view definitions until P2.

### P2 — Optimization

**5. Projection Reads** — [SHIPPED]

- **What:** `cf read --fields id,sender,tags,payload` returns only requested fields. Reduces bandwidth by 60-80% for bulk reads.
- **Status:** Shipped.

**6. RPT Tag Conventions**

Application-level vocabulary, not protocol changes. Zero implementation lines — convention documentation only.

| Convention | Tags | Purpose |
|-----------|------|---------|
| Signals | `signal:*` | RPT signal observations on interactions |
| Filter reports | `filter:report` | Filter-generated pattern discoveries |
| Temporal classification | `loop:fast`, `loop:medium`, `loop:slow`, `loop:permanent` | Protect slow-loop signals from premature compaction |
| Memory operations | `memory:standing`, `memory:proposed`, etc. | Memory campfire vocabulary |
| Composition tracking | `compose:<pattern-name>` | Make agent composition patterns visible |
| Skill operations | `skill:invoke`, `skill:step:<id>`, `skill:convergence`, `skill:complete`, `skill:abort` | Skill campfire vocabulary |

### P3 — Future

**7. ConfigProposal Protocol** — Members propose configuration changes (`config:propose`), trial them, auto-roll back if a rollback condition triggers. ~300+ lines.

**8. Composition Tracking Tags** — When an agent sends a set of messages forming a recognized pattern, it tags the first with `compose:<pattern-name>`.

---

## 16a. P1 Implementation Status

P1 shipped in campfire repo as of 2026-03-18. This section records what was implemented and what P1 limitations remain.

### What Shipped

| Feature | Status | Notes |
|---------|--------|-------|
| `cf read --tag` | Shipped | SQL `json_each()` predicate on tags column |
| `cf read --fields` | Shipped | Field projection reduces bandwidth |
| Membership roles (observer/writer/full) | Shipped | Client-side enforcement in `role.go`. `cf admit --role`. Legacy members default to `full`. |
| `campfire:compact` | Shipped (filesystem only) | `compact.go` calls `sendFilesystem()` directly — see P2 |
| `campfire:view` | Shipped (local only) | View stored in local SQLite; not written to transport — see P2 |
| `cf read --follow` | Shipped (with known bugs) | Post-fetch cursor filtering; `--follow + --json + --fields` incompatible — P1 bugs to fix |

### P1 Limitations (Carry Into P2)

1. **Compact is filesystem-only.** `compact.go` hardcodes `sendFilesystem()`. GitHub and HTTP transport campfires cannot compact.
2. **Views are local-only.** `view.go` stores `campfire:view` in local SQLite but does not write to transport. Other agents cannot see view definitions.
3. **No role mutation after admission.** The only way to change a member's role is evict + re-admit. No `cf member set-role`.
4. **Follow mode post-fetch filtering.** `--follow` fetches all messages then filters by cursor client-side. Wasteful for large histories.
5. **`--follow + --json + --fields` incompatible.** Follow path does not wire through `--fields`.

---

## 17. P2 — Transport Compatibility

P2 lifts the filesystem restriction. Every feature that currently hardcodes `fs.New()` or `sendFilesystem()` must work across all three transports: filesystem, GitHub, and P2P HTTP.

### Requirement 1: Transport-Agnostic Admission

**Current state.** `admit.go` imports `pkg/transport/fs` and calls `fs.New(fs.DefaultBaseDir())` directly.

**P2 requirement.** Admission must resolve the campfire's transport from the local store and dispatch to the appropriate transport backend.

**Per-transport admission path:**

| Transport | Member record | System message | Discovery |
|-----------|--------------|----------------|-----------|
| Filesystem | Write `.cbor` to `members/` dir | Write `.cbor` to `messages/` dir | Existing peer reads dir |
| GitHub | Create file via GitHub API in `members/` path | Create file via GitHub API in `messages/` path | Existing peer fetches repo |
| P2P HTTP | POST to `/members` endpoint | POST to `/messages` endpoint | Existing peer polls endpoint |

**Acceptance criteria:** `cf admit` works for campfires created with any transport. Transport type determined from membership record, not hardcoded.

### Requirement 2: Transport-Agnostic Compaction

**Current state.** `compact.go` calls `sendFilesystem()` directly.

**P2 requirement.** Compaction must use the same transport-dispatch mechanism as regular `cf send`.

**Distributed compaction considerations:**
- No quorum required for P2. Any `full` member can compact; all members apply it to their local view. Conflicting compactions resolved by union.
- Checkpoint hash verification: mismatch indicates divergence; receiving agent flags but still applies.
- Compaction propagates through same transport channel as regular messages.

**Acceptance criteria:** `cf compact` works for campfires on any transport.

### Requirement 3: View Propagation

**Current state.** `view.go` creates a signed `campfire:view` message but only stores it locally.

**P2 requirement.** View creation must write the `campfire:view` message to the transport so all members can discover and materialize views.

**Design intent:** Views are campfire-scoped, not agent-scoped. A `full` member defines a view; all members (including observers) can materialize it.

**View conflict resolution:** Multiple members may create views with the same name. Latest-by-timestamp definition wins (already implemented in `findLatestView`). Eventually consistent.

**Acceptance criteria:** `cf view create` writes to both local store and transport. `cf view list` and `cf view read` can discover views created by other members.

### Requirement 4: Role Mutation Lifecycle

**Current state.** Roles are set at admission time only. No `cf member set-role`.

**P2 requirement.** Add `cf member set-role <campfire-id> <pubkey> --role <new-role>` that:

1. Verifies caller has `full` role.
2. Verifies target member exists.
3. Updates member record on transport.
4. Emits a `campfire:member-role-changed` system message:
   ```json
   {
     "member": "<pubkey-hex>",
     "previous_role": "observer",
     "new_role": "writer",
     "changed_at": 1742323200000000000
   }
   ```
5. Stores updated role in local membership record.

**Constraints:**
- A member cannot change their own role (prevents self-promotion).
- The campfire creator can always change roles. Other `full` members can change roles for non-full members only. Prevents lateral demotion wars between `full` members.
- Role changes take effect on next read/send operation.

**Transport-enforced roles (P3).** P2 still uses client-side enforcement with audit trail. True transport-level enforcement is P3 scope.

**Acceptance criteria:** `cf member set-role` exists and works. Role change emits system message. Member's effective role updates on subsequent operations.

### Transport Abstraction

P2 should introduce a `transport.Transport` interface:

```go
type Transport interface {
    WriteMessage(campfireID string, msg *message.Message) error
    ListMessages(campfireID string) ([]message.Message, error)
    WriteMember(campfireID string, member campfire.MemberRecord) error
    ListMembers(campfireID string) ([]campfire.MemberRecord, error)
    ReadState(campfireID string) (*campfire.CampfireState, error)
}
```

### Transport Resolution

The local store's membership record should carry enough information to reconstruct the transport:

| Transport | Stored in membership | Resolution |
|-----------|---------------------|------------|
| Filesystem | `transport_dir` (path) | `fs.New(filepath.Dir(transport_dir))` |
| GitHub | `transport_github_repo` + `transport_github_path` | `github.New(repo, path, token)` |
| P2P HTTP | `transport_http_endpoint` | `http.New(endpoint)` |

### P2 Effort Estimates

| Requirement | Estimated LOC | Dependencies |
|-------------|---------------|--------------|
| Transport interface + resolution | ~200 | None |
| Transport-agnostic admission | ~150 | Transport interface |
| Transport-agnostic compaction | ~100 | Transport interface |
| View propagation | ~80 | Transport interface |
| Role mutation (`cf member set-role`) | ~200 | Transport interface |
| Follow mode fixes (P1 bugs) | ~50 | None |
| **Total** | **~780** | |

### P2 Open Questions

1. **Should views be immutable?** Currently, creating a view with the same name overwrites the previous definition (latest-by-timestamp wins). Should there be an explicit `cf view update` or is implicit overwrite sufficient?
2. **Compaction authority.** Should compaction require a quorum of `full` members, or is single-member compaction sufficient?
3. **Role change notification.** Should members be notified when their role changes? The `campfire:member-role-changed` message is visible to all members, but an observer being promoted to writer has no push notification.

---

## 18. Data Flow Diagram

```
                              OPERATOR
                                 |
                    +------------+------------+
                    |                         |
              intent (rd)              config/budget
                    |                         |
                    v                         v
    +----------------------------------------------+
    |                  Rudi (rd API)                 |
    |  Routes by identity (pubkey) or agent_type    |
    |  Tracks parent_item_id, chain_depth           |
    |  Enforces operator-level budget               |
    +--------+--------------------------+-----------+
             |                          |
    +--------v--------+     +----------v-----------+
    | Session-Mgr     |     | Automaton-Mgr        |
    | (Midtown)       |     | (new binary)         |
    |                 |     |                      |
    | Stateless       |     | SigningProxy          |
    | workers         |     | ContextService        |
    | Merge queue     |     | FastLoopEngine        |
    |                 |     | CuratorClient         |
    |                 |     | SkillEngine           |
    +--------+--------+     +--+-------+------+----+
             |                 |       |      |
        Dumb workers      +---+   +---+  +---+
        (worktrees)       v       v      v
                     +------+ +------+ +------+
                     |Inst A| |Inst B| |Inst C|
                     |work- | |work- | |work- |
                     |tree  | |tree  | |tree  |
                     |sign- | |sign- | |sign- |
                     |proxy | |proxy | |proxy |
                     +--+---+ +--+---+ +--+---+
                        |        |        |
          +-------------+--------+--------+-------------+
          |             |        |        |             |
          v             v        v        v             v
    +---------+   +---------+  +---------+  +---------+
    | Memory  |   |Telemetry|  | Intra-  |  | Intent  |
    |Campfire |   |Campfire |  | Automn  |  |Campfire |
    |         |   |         |  |Campfire |  |         |
    | standing|   | session |  | hot     |  | proposed|
    | proposed|   | desire  |  | status  |  | fulfill |
    | episodic|   | adapt   |  | discov  |  | escalate|
    | anchor  |   | signal  |  |         |  | reject  |
    | compact |   | rollback|  |         |  | self-chg|
    |         |   | skill-* |  |         |  |         |
    +---------+   +---------+  +---------+  +---------+
```

---

## 19. Multi-Instance Model

### Architecture

N instances of the same automaton share an identity but operate in isolation. Each instance:

- Runs in its own git worktree
- Writes to the memory campfire directly (proposed tier)
- Reads standing memory at spawn time (from campfire view)
- Reads hot state via the intra-automaton campfire
- Sends campfire messages through the signing proxy
- One instance per external campfire (prevents contradictory messages)

### Read-Your-Own-Writes

**Standing knowledge:** Worker B cannot see Worker A's proposed writes until A's curator promotes them to standing. Lag = task duration + curator. Acceptable because workers are on different tasks.

**Operational facts:** Worker B sees Worker A's writes immediately via the intra-automaton campfire's `memory:hot` messages. Latency: 1-5ms (filesystem transport).

### Scaling Path

**3 workers (current):** Four internal campfires with filesystem transport. No additional infrastructure needed.

**10+ workers:** Append-only logs handle concurrent writers without coordination. View materializer may need incremental refresh.

**Distributed (multi-machine):** Replace filesystem transport with P2P HTTP for internal campfires. Signing proxy remains sole key holder.

---

## 20. RPT Compliance Assessment

| RPT Section | Coverage | Notes |
|-------------|----------|-------|
| 1.3 Five AI-App edge signals | 100% | Measurable from campfire message metadata |
| 1.5 Objective function | 100% | Per-intent budgets make token economics measurable per work-unit |
| 1.6 Composable substrate + desire paths | 100% | Desire paths are campfire message patterns. Skill genesis from desire paths. |
| 1.7 Three loops | 100% | Fast/medium/slow at automaton AND engine level |
| 1.7 RPT triple invariant | 100% | Every capability has instrumentation and configuration surface |
| 3.1 Completion drive | 100% | Mechanical circuit breakers throughout |
| 3.2 Circuit breakers | 100% | Chain depth, budget caps, signing proxy, skill locked_floor and min_instances |
| 5.1 Token economics | 100% | Per-intent budget with cost profiles by type |
| 5.2 Battery model | 100% | Skills operationalize coupling efficiency |
| 6.1-6.6 Measurement | 99.2% | Three closeable gaps remain (see below) |

### Revised Compliance: 99.2%

**Gap A — Operator consumption instrumentation (0.3%):** Skills must instrument how the operator consumes output (which passes read, time-to-approval, explicit quality signal). Mechanism specified in Section 11 but not yet built.

**Gap B — Fleet skill learning pipeline (0.2%):** Individual automata optimize their own skill parameters. Fleet-wide promotion pipeline specified but requires implementation.

**Gap C — Skill composition telemetry (0.3%):** Skill pipelines (A calls B calls C) must be treated as optimization units. Mechanism: `telemetry:skill-composition` messages link parent-child invocations. Not yet built.

**Gap D — Nested loop validation (permanent):** No framework for validating nested optimization loops interact correctly. Theory gap in RPT, not architecture gap.

---

## 21. Permanent Engineering Constraints

Five fundamental properties of recursive self-optimization that no protocol change can resolve.

### 1. The Broken Diagnostician

A corrupted optimization loop will misinterpret its own signals regardless of substrate. **How we bound it:** Fast loop is a pure rule engine (~300 lines, no LLM). LLM reasoning happens at the slow loop level where human oversight is present.

### 2. The Infinite Instrumentation Regress

Instrumenting instrumentation has no natural termination point. **How we bound it:** Termination at Level 3. Each metric has a token cost and a value. Stop instrumenting when marginal instrument costs more than it reveals.

### 3. Self-Study Competes with Primary Function

**How we bound it:** Self-instrumentation gets a fixed 10% cap of total engine budget. When cap is reached, self-study pauses — automaton management never pauses.

### 4. Self-Modification Trends Toward Complexity

**How we bound it:** Component count as a first-class metric (the sixth signal). Every slow-loop proposal must include a simplification alternative.

### 5. Closed Feedback Loops

A system that creates, executes, measures, and modifies its own task types has a closed feedback loop. **How we bound it:** Any self-proposed task type that does not improve its target metric within N cycles is automatically deprecated.

---

## 22. Open Issues

1. **Semantic contradiction detection.** Two facts about the same thing with different `fact_id`s remain undetectable. Scaling path: embedding-based similarity.

2. **Keyword matching quality.** Context loading pipeline's keyword match has no empirical validation. Fast loop compensates for misses.

3. **View materializer weight computation.** Mapping `weight = confidence * 0.9^(sessions_since_last_reference)` to a campfire:view predicate requires numeric computation in the predicate language. Grammar needs definition.

4. **Fleet cold-start cost.** A fleet restart (50 automata, 250+ concurrent file reads) may need warm cache handling in the transport spec.

5. **Nested loop testability.** Testing three nested optimization loops requires a framework that does not exist.

6. **rd/campfire convergence.** The structural isomorphism between rd tasks and campfire messages is real. Revisit when internal campfires are battle-tested.

7. **Correlated failure mode.** Campfire-as-substrate: if the campfire library has a bug, ALL internal operations are affected. Mitigation: campfire library is simple code; internal campfires are on same machine (failure domain already correlated).

8. **Budget enforcement divergence.** An instance can overshoot by the amount consumed between last async report and enforcement. Mitigation: safety margin in local counter.

9. **campfire:view predicate grammar (build-plan blocker).** The grammar must support tag match, sender match, time predicates, payload field predicates, AND/OR, and numeric computation for weight expressions. Must be specified in the campfire repo before automaton-manager implementation begins.

10. **Fast loop rules as code-form predicates.** The five adaptations are currently directional prose. Implementer needs concrete predicates: `IF signal.token_count > model.mean + 1 * model.stddev THEN planning_budget_multiplier = min(planning_budget_multiplier * 1.10, 2.0)`.

11. **Boot sequence specification.** Service initialization order, campfire open failure handling, and recovery from partial startup are unspecified.

12. **Curator input format.** "The session transcript" is ambiguous — Claude Code stdout, rd API response, or something else?

13. **context_pct JSON parsing.** The rd progress `notes` field is currently free-form string. Parsing `{"context_pct": 72}` from it needs explicit specification.

14. **Fast loop trigger timing.** Whether fast loop fires synchronously in `onWorkerExit` callback or in a goroutine is unspecified.

15. **on_event hook mechanism.** Whether schedule `on_event` triggers hook into `MergeQueue.onMerged` callback or requires a new git event listener needs specification.

---

## Appendix A: Tag Vocabulary Reference

**Memory campfire tags:**
- `memory:standing` — durable fact with confidence and category
- `memory:proposed` — worker-written, pending curator review
- `memory:episodic` — session narrative
- `memory:reinforce` — boost weight of antecedent
- `memory:contradict` — override antecedent with new content
- `memory:decay` — reduce weight of antecedent
- `memory:anchor` — mark antecedent as permanent (decay-exempt)
- `memory:snapshot` — compaction checkpoint
- `context:handoff` — curator's session summary

**Telemetry campfire tags:**
- `telemetry:session` — structured session record
- `telemetry:desire-path` — unexpected behavior pattern
- `telemetry:adaptation` — fast loop parameter change
- `telemetry:rollback` — reverted adaptation
- `telemetry:skill-invocation` — top-level skill execution record
- `telemetry:skill-step` — per-step record within a skill
- `telemetry:skill-composition` — parent-child skill link via antecedent
- `signal:*` — RPT signal observations
- `filter:report` — filter pattern discoveries

**Intra-automaton campfire tags:**
- `discovery` — cross-instance finding
- `status` — instance status update
- `memory:hot` — immediate operational fact

**Intent campfire tags:**
- `intent:proposed` — task proposal from instance
- `intent:fulfilled` — manager created rd task
- `intent:escalated` — escalated to operator
- `intent:rejected` — rejected with reason
- `intent:self-change` — engine self-modification proposal (always gate:operator)

**Skill campfire tags:**
- `skill:invoke` — skill invocation start (params, version, preset)
- `skill:step:<step-id>` — step output
- `skill:convergence` — convergence check result
- `skill:complete` — skill completion (outcome, total metrics)
- `skill:abort` — skill aborted (reason, partial metrics)

**RPT temporal classification tags:**
- `loop:fast` — short retention acceptable
- `loop:medium` — medium retention required
- `loop:slow` — long retention required
- `loop:permanent` — never compact

---

## Appendix B: Adversary Attack Resolution

| Attack | Resolution |
|--------|-----------|
| Curator single point of failure | Retry wrapper (3 attempts, exponential backoff, 30s total). Degraded handoff fallback on failure. |
| Fact ID collision | Controlled vocabulary in config.toml. Uncontrolled IDs flagged. Semantic hash normalization. |
| Hot state crash durability | Synchronous atomic rename (write temp + os.Rename). Crash-safe on ext4/btrfs. |
| Keyword matching fragility | Acknowledged. Fast loop compensates. Vector retrieval scaling path. |
| 70% rotation threshold | Context pressure via rd progress messages. Acceptable for current use. |
| Binary-only filters | campfire:view with compound predicates (P1 spec change). |
| Broken diagnostician | Permanent constraint. Rule engine (no LLM) + operator oversight. |
| Infinite instrumentation regress | Permanent constraint. Level 3 termination, economic pruning. |
| Self-study competes with primary function | Permanent constraint. 10% budget cap. |
| Complexity growth | Permanent constraint. Component count signal, mandatory simplification proposals. |
| Budget on campfire latency | Local-first enforcement. Campfire for reporting. Safety margin. |
| Closed feedback loops | Permanent constraint. Auto-deprecate failing self-proposed patterns. |
| No compaction primitive | campfire:compact (P1 spec change — SHIPPED). |
| HintDemuxer injection surface | Proposed memory tier (trust boundary). Strict parsing, sanitization, weight-tier tagging. |
| Session-manager god object | Automaton-manager as separate binary. Five thin services. |
| Validation runner independence | Validation criteria provenance tracked. Criteria from outside automaton's influence chain. |
| Memory decay silent erosion | memory:anchor for permanent facts. Decay-exempt critical decisions. |
| Skill review depth erosion | Floor constraints (locked_floor, min_instances). Step elimination is gate:operator. |
| Skill parameter space explosion | Presets collapse space to 3-5 options. Fast loop selects among presets. |
| Skill Goodhart's Law | Dual-metric: efficiency (token cost) AND effectiveness (rework rate within 14 days). |
| Skill composition instability | Per-skill telemetry with composition tracking via antecedent chains. |
| Skill methodology lock-in | Epsilon-greedy exploration budget (10% of invocations use non-optimal preset). |

---

## Appendix C: Glossary

- **Automaton:** A persistent entity with an Ed25519 identity that acts with second-party intent (the operator's). Not a session. Not a chatbot. A named, remembered, accountable entity on the network.
- **Instance:** One running execution of an automaton. Ephemeral. Shares the automaton's identity.
- **Operator:** A person or legal entity that controls automata. Bears accountability for all automaton behavior.
- **Campfire constellation:** The four internal campfires (memory, telemetry, intra-automaton, intent) plus external campfire memberships that constitute an automaton's persistent state.
- **Signing proxy:** A Unix socket service that signs campfire messages with the automaton's private key after checking the allowlist. The key never enters the worker environment.
- **View materializer:** A campfire:view that computes a weighted, filtered, ordered projection of the memory campfire.
- **Intent graph:** The message DAG in the intent campfire. Provenance chains and chain depth are antecedent chain properties.
- **Curator:** A Sonnet-tier API call that extracts structured facts from session transcripts and writes them as campfire messages. The trust boundary between worker claims and automaton knowledge.
- **Fast loop:** Per-dispatch automated adaptation. Statistical process controller, no LLM, bounded parameter ranges, auto-rollback after 3 sessions.
- **Medium loop:** Daily/weekly operator-reviewed dashboard. Trend analysis across fleet.
- **Slow loop:** Weekly+ operator-approved structural changes. Engine proposes, operator decides.
- **Gate:human:** An rd task that requires operator approval.
- **Desire path:** An agent behavior that reveals what interface it expected. Three types: tool, memory, task generation.
- **Component count:** The sixth engine signal. Lower = resonating. Prevents unbounded complexity growth.
- **Proposed tier:** Memory entries written by workers that have not been validated by the curator.
- **Anchor:** A memory:anchor tag marking a fact as decay-exempt.
- **Statistical process controller:** The fast loop implementation. Maintains running mean and variance per task type. Pure rules, no LLM.
- **Skill:** An executable, instrumentable, optimizable multi-step orchestration procedure defined in TOML. Has the RPT triple.
- **Preset:** An operator-defined named parameter configuration for a skill. Fast loop selects among presets. New presets are gate:operator.
- **Floor constraint:** A parameter minimum (`locked_floor`) or step minimum (`min_instances`) the fast loop cannot breach.
- **Convergence evaluator:** A rule engine (~50 lines, no LLM) that evaluates a composite function over tag counts, token volume ratios, and explicit convergence signals.
- **Skill genesis:** The process by which the system discovers new skills from repeated operator behavior (desire paths at the methodology level).
- **Exploration budget:** A fraction (default 10%) of skill invocations that use a non-optimal preset (epsilon-greedy).
- **Rework rate:** The Goodhart counter-metric. If operator accepts output then modifies it within 14 days, that is a rework event.
