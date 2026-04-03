# Design: Social Graph UX Model

**Status:** Proposed
**Date:** 2026-04-02
**Builds on:** [design-identity-as-campfire.md](design-identity-as-campfire.md) (campfire-agent-ub4)
**Process:** Adversarial design -- four dispositions (adversary, creative, systems pragmatist, domain purist) deliberated via campfire, architect synthesized.

## Summary

Campfire is a coordination internetworking protocol. Its network properties -- addressing (campfire IDs), routing (transports), federation (campfire-as-member, recursive composition), peering (trust edges), and membership -- are the protocol layer. Conventions are the application layer that runs on top. The correct analogies are SMTP (routing, relay federation, addressing), XMPP (federation, addressing, presence), or BGP/DNS (peering, federation) -- not HTTP. HTTP does not have peering, routing, or federation. Campfire has all three. Those are network-layer properties, not application-layer properties.

This design defines the social graph -- the human-legible surface that makes the network's topology visible and operable. Home is an address. A campfire is a coordination space. Connections are peering relationships. Trust levels are routing policy on edges. The six graph operations (place/remove, link/unlink, post/read) are the vocabulary for network operations, not a new layer above the network.

The honest design question is not "which application assumptions do we consciously bake in." It is: what is the minimal user-facing vocabulary that makes the network's topology legible and operable, without encoding any single application domain into it? The answer is two nouns (home, campfire), six graph operations, and a social verb surface that compiles down to them.

## Core Claim

**The social graph is the human-legible surface of the coordination network.**

Peering, routing, and federation are not application-layer choices -- they are the protocol. The social graph vocabulary (home, campfire, connect, follow, trust, delegate) is the human-legible surface for operating that network.

Concretely:
- **Home** is an address. Your self-campfire, where you are reachable and where your state lives.
- **Campfire** is a coordination space. The network's nodes are campfires. Everything is a campfire.
- **Connections** are peering relationships. A trust edge between homes that enables routing, federation, and communication.
- **Trust levels** are routing policy on edges. Follow, trust, delegate control what flows across an edge: discovery only, bidirectional messaging, or authority propagation.

The six graph operations (place/remove, link/unlink, post/read) map 1:1 to existing protocol operations. They are not a new layer -- they are a vocabulary for talking about network operations in coordination terms. The human verb surface (connect, share, follow, trust, delegate) compiles to graph operations which compile to protocol primitives. Three dialects, one network.

## The Six Primitives

The social graph has six operations, organized as three create/destroy pairs:

| Pair | Operation | What it does | Protocol compilation |
|------|-----------|-------------|---------------------|
| Node lifecycle | **place**(name) | Create a named node (campfire) in the graph | `protocol.Create(CreateRequest)` |
| | **remove**(node) | Destroy a node | `protocol.Disband(campfireID)` |
| Edge lifecycle | **link**(a, b, type) | Create a typed directed edge between two nodes | `protocol.Join(JoinRequest)` or `protocol.Admit(AdmitRequest)` depending on direction |
| | **unlink**(a, b, type) | Remove a typed directed edge | `protocol.Leave(campfireID)` or `protocol.Evict(EvictRequest)` depending on direction |
| Message lifecycle | **post**(node, content) | Append a message to a node's log | `protocol.Send(SendRequest)` |
| | **read**(node, filter) | Query messages from a node's log | `protocol.Read(ReadRequest)` |

**Post is deliberately not paired with a delete.** Append-only logs are a protocol invariant. The futures pattern is the correct mechanism for cases that would otherwise require deletion: a future message declares intent, a fulfillment message resolves it — including with a retraction, correction, or supersession. Applications express "this no longer applies" by fulfilling the original message, not by mutating or removing it. Compaction is a separate management operation that prunes messages while preserving the append-only semantic (compacted messages are excluded from default reads, never deleted from the causal record).

### Domain purist verification

Every graph verb compiles to existing protocol operations. No new protocol primitives are required:

```
place(name)           -> Create(CreateRequest{Description: name, JoinProtocol, Transport})
remove(node)          -> Disband(campfireID)
link(me, them, membership)    -> Join(JoinRequest{CampfireID: them})
link(them, me, membership)    -> Admit(AdmitRequest{CampfireID: them, PubKey: me})
unlink(me, them, membership)  -> Leave(campfireID: them)
unlink(them, me, membership)  -> Evict(EvictRequest{CampfireID: them, PubKey: me})
post(node, content)   -> Send(SendRequest{CampfireID: node, Payload: content})
read(node, filter)    -> Read(ReadRequest{CampfireID: node, Tags: filter})
```

**Edge types beyond membership and vouch are convention-layer.** The protocol provides the structural edge (membership) and the trust signal (vouch/revoke). Delegation, federation, subscription, and application-specific edge types (assigned-to, follows) are convention operations that compose from these protocol primitives. This resolves the domain purist's finding that edge semantics beyond membership/vouch are application-layer.

## The Verb Surface

Three dialects of the same six operations:

### Protocol layer (exists today)

```
create    join    admit    evict    send    read
leave     disband
```

These are the wire operations. Agents and SDKs use them directly. They are domain-invariant in the strict sense: they move messages through a graph of campfires and manage membership edges. They do not know why.

### Graph layer (new conceptual vocabulary, same operations)

```
place / remove     (node lifecycle)
link / unlink      (edge lifecycle)
post / read        (message lifecycle)
```

This vocabulary exists for design clarity and documentation. It maps 1:1 to protocol operations. It is NOT a new API -- it is a way of talking about what the protocol already does in graph terms.

### Human layer (social verbs, compiles to graph operations)

| Human verb | Graph compilation | Protocol compilation |
|-----------|-------------------|---------------------|
| **connect**(alice) | post(home, vouch(alice)) + post(alice.home, connect-request) | Mutual vouch via trust convention + connection-request future on Alice's home |
| **share**(content, space) | post(space, content) | Send(space, content) |
| **follow**(entity) | post(home, subscribe(entity)) | Subscribe to entity's public feed via the identity convention (no home membership — reads entity's beacon and published messages) |
| **trust**(entity, level) | post(home, vouch(entity, level)) | Send(home, vouch message for entity) |
| **delegate**(entity, scope) | post(home, delegation(entity, scope)) | Send(home, delegation convention message) |
| **disconnect**(alice) | post(home, revoke(alice)) | Revoke vouch, leave shared channel if one exists |

**connect does NOT mean mutual home membership.** Your home stays invite-only — you and your delegates only. Connecting to Alice does not give her a key to your house. It establishes three things:

1. **Trust-graph relationship.** Mutual vouch in the trust convention. You appear in each other's trust neighborhoods. Locally-evaluated — the vouch is a signal, not an authority grant.
2. **Routing capability.** You can now reach each other's home's public convention operations (introduce-me, verify-me, list-homes). These operations are available to anyone who can discover the beacon, but a vouch relationship means your calls are evaluated at a higher trust level.
3. **Shared channel (optional).** If you need ongoing communication, `connect` creates or joins a shared two-party campfire — a channel. This is where you actually talk. Neither party's home is the channel. The channel is a separate campfire where both are members.

**The connect ceremony uses futures.** The protocol flow:
1. I post a connection-request (future) to Alice's home via the identity convention.
2. Alice reviews the request (interactive prompt for humans, policy check for agents).
3. Alice fulfills the future by vouching for me on her home and posting an acceptance.
4. I receive the fulfillment, vouch for Alice on my home.
5. Optionally: a shared channel campfire is created for ongoing communication.

This is consent expressed through futures — the ceremony is already in the protocol.

**Human/agent symmetry (C6).** The command surface is identical. The ONLY difference is the consent model:
- Humans: interactive consent for connection requests ("Connect with alice? [y/N]")
- Agents: policy-based consent from the trust convention (check local policy, accept or reject silently)

One SDK, one CLI, one set of MCP tools. No "human API" vs "agent API." The consent layer wraps the same operations.

## Ephemeral Agents

**Home is universal for persistent entities. Ephemeral agents do NOT create homes.**

The adversary (A3) correctly identified that forcing `cf init` (self-campfire creation, identity convention, beacon publication) on a 30-second CI worker is absurd overhead. The resolution:

### Persistent vs. ephemeral -- convention-level distinction

An agent is **persistent** if it maintains identity across sessions -- it has relationships, state, reputation, and a need to be addressable. A persistent agent creates a home (self-campfire) at initialization. Examples: a human operator, an organizational automaton, a long-running service, a named bot.

An agent is **ephemeral** if it exists for a single task and is discarded. An ephemeral agent does NOT create a home. It operates within a campfire created and owned by a persistent entity. Examples: a CI pipeline worker, a serverless function, a swarm implementer agent, a webhook handler.

### How ephemeral agents participate

1. **The orchestrator (persistent entity) creates the campfire.** A swarm dispatch creates a project campfire. A CI system creates a pipeline campfire. The persistent entity owns the space.
2. **The orchestrator admits the ephemeral agent.** The ephemeral agent receives a keypair (for signing) and admission to the campfire. It does NOT run `cf init` or create a self-campfire.
3. **The ephemeral agent operates within the campfire.** It can post, read, and participate in convention operations. It is identified by its signing key within that campfire's membership list.
4. **When the task ends, the ephemeral agent is evicted (or simply stops).** No identity cleanup needed -- there is no home to disband, no beacons to unpublish, no migration notices to post.

### What makes this work at the protocol level

The protocol already supports this. An Ed25519 keypair is sufficient to sign messages. A persistent entity can admit any pubkey to a campfire. The self-campfire (home) is a convention-layer concept, not a protocol requirement. An agent with a keypair and campfire admission can participate fully in that campfire without having a home.

**The identity address gap.** An ephemeral agent has no `SenderCampfireID` -- it has only a `Sender` (pubkey). Other participants see its messages signed by its key but cannot address it outside the campfire. This is correct -- ephemeral agents are not independently addressable. If you need to reach one, you reach the campfire it operates in and filter for its sender key.

**Trust implication.** An ephemeral agent's trust is derived from the persistent entity that admitted it. The orchestrator vouches implicitly by admitting the agent. Consumers who trust the orchestrator extend that trust to the orchestrator's admitted agents within that specific campfire. This is delegation by admission, not a separate trust primitive.

## Current Context Model

**No implicit session state at the protocol level.** The systems pragmatist confirmed: the CLI is fully stateless per-invocation. Every command resolves its target campfire from explicit arguments, alias resolution, or ambient project context (`.campfire/root` file).

**Names everywhere — for agents and humans equally.** Hex campfire IDs are an implementation detail that neither humans nor agents should ever handle. Every operation takes a name. The naming layer resolves it. TOFU pins it after first resolution. An ID appears only on the wire, in debugging output, or when bootstrapping before a name exists.

**`cf use <name>` sets the current namespace context** — for agents and humans alike:

```bash
cf use myproject        # sets current context (writes to ~/.campfire/current or $CF_CONTEXT)
cf post "hello"         # resolves to: cf post myproject "hello"
cf use --clear          # removes context
```

An agent bootstrapping into a session calls `cf use myproject` (or sets `$CF_CONTEXT=myproject`) and then all subsequent operations are scoped to that name. This is not shell convenience — it is how agents compose without hardcoding addresses. An agent that has to resolve or pass 64-character hex strings will not be written. The naming layer exists precisely so agents don't have to know what a campfire ID is.

**Context ambiguity (A4) is a naming problem, not an agent-discipline problem.** The mitigation is namespace scoping and TOFU pinning — not "agents must use explicit IDs." Names are scoped to the current home or explicitly specified namespace. After first resolution, the name-to-ID binding is pinned locally. A rogue campfire with the same name in a different namespace does not silently replace the pinned target.

### Context ambiguity (A4) resolution

The adversary raised context ambiguity as a security vulnerability. Resolution:

1. **Names are always scoped to a namespace.** A name resolves within the current home's namespace or an explicitly specified namespace. There is no global unscoped resolution.
2. **TOFU pinning prevents silent name hijacking.** After first resolution, the name-to-ID binding is pinned locally. A rogue campfire with the same name will not silently replace the pinned target.
3. **First-contact vulnerability is acknowledged.** The first time an agent encounters a name, it trusts the resolution. This is the same trust model as SSH (`known_hosts`) and HTTPS (certificate on first visit). Mitigations: out-of-band verification for high-value connections, seed registries for organizational defaults, vouch chains for peer introductions.
4. **Destructive operations require explicit campfire ID.** `cf disband`, `cf evict`, and other destructive operations should require the full campfire ID (or alias with confirmation), not just the `cf use` context.

## Naming

**Names are locally scoped. C7 (names as alias-typed edges) is a direction for a separate design, not a requirement for this one.**

The creative analysis proposed collapsing the naming system into graph edges: a name is an alias-typed edge from your home to a campfire, and DNS-style resolution is graph traversal along alias edge chains. This is architecturally elegant but represents a significant redesign of `pkg/naming/`.

### Scope of this design vs. naming design

This design imposes the following constraints on the naming layer:

1. **Names resolve to campfire IDs**, not to pubkeys. (This aligns with the identity-as-campfire design, which makes the campfire ID the identity address.)
2. **Name resolution is always scoped to a namespace.** The resolver's home (or an explicitly specified namespace) is the resolution root. There is no global flat namespace.
3. **TOFU pinning binds names to campfire IDs.** Once pinned, a name does not silently re-resolve to a different campfire.
4. **Name transfer, collision, and federation-scope naming are out of scope for this design.** These are real problems (adversary A5) that belong in a dedicated naming design.

### What this design does NOT require from naming

- Global uniqueness of names (names are locally scoped by design)
- Name-as-edge implementation (C7 is a future direction, not a dependency)
- Cross-namespace resolution protocol (federation naming is a separate problem)

## The Honest Application Assumptions

Peering, routing, and federation are the protocol -- not assumptions. But the vocabulary we choose to expose them to users DOES carry assumptions. This section names six vocabulary and model choices that go beyond the network layer. For each: what it is, why we chose it, what it serves, and what it forecloses.

### Assumption 1: All coordination flows through shared spaces (campfires)

**What:** There is no point-to-point messaging primitive. To communicate, entities join a shared campfire and post messages there. Even "direct messages" are implemented as two-member campfires.

**Why:** Shared spaces provide a natural audit trail, support observer roles, enable convention hosting, and make coordination visible. Point-to-point messaging requires separate routing, discovery, and trust infrastructure.

**Serves:** Work queues (shared project space), operations (fleet campfire), social (group chat), commercial (exchange campfire). All four domains use shared-space coordination naturally.

**Forecloses:** Efficient point-to-point communication at scale. In a system with 10,000 agents each sending occasional messages to specific peers, the campfire-per-pair model creates O(N^2) campfires. Mitigation: shared hub campfires with tag-based filtering. This is HTTP's "server" model, not email's "mailbox" model.

### Assumption 2: Messages are append-only

**What:** Once posted, a message cannot be modified or deleted. Corrections are new messages referencing the original. Compaction excludes messages from default reads but does not destroy them.

**Why:** Append-only logs provide causal ordering (DAG), audit trails, and convergent state (CRDT-compatible). Mutable messages break provenance chains, complicate caching, and make trust verification fragile. The futures pattern covers the cases that would otherwise require deletion: a future declares intent; a fulfillment resolves it, including with a retraction or correction. "This no longer applies" is a fulfillment, not a delete.

**Serves:** Work queues (mutation log), operations (audit trail), social (conversation history), commercial (transaction record). All four domains benefit from immutable history.

**Forecloses:** GDPR-style "right to erasure" at the protocol level. Applications that need content removal must implement it as a convention (redaction markers, fulfillment-based retraction) rather than actual deletion. The append-only log records that redaction occurred.

### Assumption 3: Identity is a campfire (home)

**What:** Every persistent entity's identity is a self-campfire. The campfire ID is the address. The signing key is an internal credential. (Per the identity-as-campfire design.)

**Why:** Eliminates the structural exception where identity is the only thing in the protocol that is not a campfire. Makes identity compositional (homes can be members of other campfires), discoverable (beacons), and convention-hosting (identity convention on the home).

**Serves:** All domains uniformly. Every entity -- human operator, organizational automaton, service, bot -- gets identity the same way.

**Forecloses:** Lightweight anonymous participation. An entity that wants to post a single message to a public campfire must either create a home (overhead) or operate as an ephemeral agent under a persistent entity's authority (delegation). True anonymity requires a separate mechanism (anonymous credential schemes, not covered here).

### Assumption 4: Trust is local-first

**What:** Trust starts at the agent's own keypair and grows outward through local policy. External trust sources (seeds, registries, peers) are evaluated against local policy, not the other way around. Trust scores are derived locally, not assigned globally.

**Why:** Decentralized systems cannot have a global trust authority without introducing a single point of failure/control. Local-first trust is the only model compatible with autonomous agents that may operate in adversarial environments.

**Serves:** All domains. Work queues trust their orchestrator's admission policy. Operations trust their fleet's vouch chain. Social trust builds through peer endorsement. Commercial trust builds through transaction history.

**Forecloses:** Global reputation systems at the protocol level. An application that wants "Agent X has reputation score 4.7" must build that as a convention-layer aggregation over local trust signals, not as a protocol feature.

### Assumption 5: Typed directed edges carry structural semantics

**What:** The graph has typed directed edges. Two types are protocol-level (membership, vouch). Additional types are convention-level. Edges are structural (who is connected to whom and how); application state lives in messages, not in edge metadata.

**Why:** Typed edges enable graph traversal with semantic awareness (walk only delegation edges, walk only membership edges). Directing edges enables asymmetric relationships (I follow you; you don't follow me). Separating structure (edges) from state (messages) prevents applications from coupling to graph topology.

**Serves:** Work queues (membership = assignment, delegation = authority). Operations (membership = fleet, vouch = health attestation). Social (follow, connect, block -- all typed edges). Commercial (membership = market participation, delegation = spending authority).

**Forecloses:** Untyped or undirected graph models. Applications that want "A and B are connected, direction doesn't matter" must model this as two directed edges (A->B and B->A). This is explicit but verbose.

### Assumption 6: Operations are campfire-scoped, not graph-scoped

**What:** Every operation targets a specific campfire. There is no cross-campfire atomic operation. Convention dispatch is per-campfire. Graph-spanning operations (e.g., "evict this key from ALL campfires") are composed from multiple campfire-scoped operations.

**Why:** Campfire-scoped operations are locally decidable -- the campfire owner controls admission, the campfire's convention controls dispatch. Cross-campfire atomicity requires distributed consensus, which contradicts the decentralized model.

**Serves:** All domains. Each campfire is an autonomous coordination space. The owner controls it.

**Forecloses:** Atomic cross-campfire operations. "Revoke this compromised key everywhere" becomes a sweep (iterate over campfires, evict from each). This is eventually consistent, not atomic. The security implication: a compromised key may retain access to some campfires during the sweep. Mitigation: campfires can subscribe to revocation feeds and auto-evict.

## What Applications Must NOT Do

The application contract -- five invariants applications must preserve to keep the graph usable across domains:

### Invariant 1: Never create edge types that shadow graph primitives

"Membership" means membership. An application cannot redefine it to mean "follows" or "subscribes to." If an application needs a different relationship semantic, it defines a new convention-level edge type. Protocol-level edge types (membership, vouch) have fixed semantics.

### Invariant 2: Never modify messages after posting

The append-only log is sacred. Applications post corrections (new messages that reference and supersede old ones via the DAG) but never mutate existing messages. Compaction is the only mechanism for managing log size, and it preserves the causal record.

### Invariant 3: Never interpret node identity

A campfire ID is an opaque hex string. Applications must not parse it, derive meaning from its structure, assume anything about the entity behind it, or use it for anything other than addressing. The meaning of a campfire (is it a home? a project? an exchange?) comes from its conventions and messages, not from its ID.

### Invariant 4: Scope application state to messages, never to edges

Edges say "these two nodes are connected with type X." Application state (task status, social profile, automaton health, account balance) lives in messages within nodes, not in edge metadata. Edges are structural; messages are semantic. This separation means applications can read state from a campfire's message log without traversing the graph, and graph topology changes (new member joins, member leaves) do not corrupt application state.

### Invariant 5: Never require graph-global knowledge

Every application operation must be computable from the node's local neighborhood (its edges and its messages). No operation may require traversing the entire graph. This is the decentralization invariant -- it guarantees that the graph works at any scale and that no single node is a bottleneck. Applications that want aggregated views build them from local queries on multiple campfires, not from a global graph query.

## Spec Changes

The domain purist identified four places where the current protocol spec leaks application semantics into the network layer. These should be cleaned up:

### P1: Escalation subtypes -- move to convention

**Current:** The spec defines escalation subtypes in Futures and Fulfillment: "architecture", "scope", "interface", "decision". These are software development concepts.

**Fix:** The spec should define the PATTERN (future + escalation + fulfills) and leave subtypes entirely to conventions. A software development convention defines "architecture", "scope", etc. A trading convention defines "price-dispute", "settlement-failure", etc. The protocol does not know about either.

### P2: "Rework cost" -- generalize

**Current:** Filter optimization target includes "rework cost from broadcasts suppressed."

**Fix:** Replace with "suppression cost" -- abstract, convention-configurable. "Rework" assumes task-oriented agents. A social feed agent has no rework; a sensor relay has no rework.

### P3: "Token cost" -- generalize

**Current:** Filter optimization target includes "total token cost of broadcasts delivered."

**Fix:** Replace with "delivery cost" without specifying the unit. Token cost presumes LLM agents. CPU agents, hardware sensors, and humans have different cost models.

### P5: Overview framing -- entity-neutral

**Current:** "Campfire is a coordination protocol for autonomous agents."

**Fix:** "Campfire is a coordination protocol. Entities -- humans, agents, services, devices -- communicate through campfires." The protocol is entity-neutral; the overview should reflect that.

## Adversary Attack Disposition

| # | Attack | Disposition | Resolution |
|---|--------|-------------|-----------|
| **A1** | Domain-invariant verbs are impossible -- choosing verbs is itself a domain decision | **Resolved** | The adversary's framing was corrected: peering, routing, and federation are network-layer properties, not application-layer choices. The six graph primitives (place/remove, link/unlink, post/read) map 1:1 to protocol operations that implement these network properties. The social verb surface (connect, follow, trust, delegate) is the human-legible vocabulary for operating the network. The honest question is not "can we be domain-invariant" but "what is the minimal vocabulary that makes the topology legible without encoding any single application domain." The six assumptions in "Honest Application Assumptions" name where vocabulary choices go beyond the network layer. |
| **A2** | Network/application separation breaks at trust -- trust semantics are inherently application-specific | **Resolved** | Trust is split correctly: protocol provides signals (vouch/revoke, membership, provenance), convention provides policy (trust status computation, adoption workflow, federation tiers). The vouch edge carries no transitivity semantics at the protocol level -- consumers interpret it per their policy. The risk of misinterpretation (A vouches for B, app X and app Y interpret differently) is real but is mitigated by convention-scoped vouch operations that declare their intended scope. |
| **A3** | Home is not universal for ephemeral agents -- self-campfire overhead is absurd for 30-second agents | **Resolved** | Home is universal for persistent entities only. Ephemeral agents (CI workers, serverless functions, swarm workers) do NOT create homes. They operate within campfires created by persistent entities, admitted by the orchestrator, identified by signing key only. See "Ephemeral Agents" section. |
| **A4** | Context ambiguity is a security vulnerability -- no mechanism to prevent operating in wrong context | **Resolved** | Protocol is fully stateless (no implicit context). CLI offers `cf use` as optional sugar. Names are namespace-scoped. TOFU pinning prevents silent hijacking. Destructive operations require explicit campfire ID. First-contact vulnerability acknowledged with mitigations (out-of-band verification, seed registries, vouch chains). See "Current Context Model" section. |
| **A5** | Navigate-by-name assumes a solved naming problem -- names are locally scoped, not globally unique | **Deferred** | Naming constraints are stated (names resolve to campfire IDs, namespace-scoped, TOFU-pinned) but the full naming design (transfer, collision, federation-scope) is a separate work item. This design does not depend on solved naming -- explicit campfire IDs work everywhere. Names are convenience, not a requirement. |
| **A6** | Minimal operations is not a stable equilibrium -- constant pressure to add domain-specific operations | **Permanent Constraint** | Acknowledged. The six primitives WILL face pressure to expand. The defense: convention-level operations extend the verb vocabulary without modifying the graph layer. When a domain needs "group-create" or "bulk-evict" or "conditional-admit," it implements them as convention operations that compose from the six primitives. If a genuinely new graph primitive is needed (not composable from the six), it is a protocol change requiring a design review. The application contract (Invariant 5: no graph-global knowledge) is the principled "no" -- any proposed operation requiring global graph state is rejected. |
| **A7** | Domain-invariance and usefulness are in fundamental tension -- clean separation always leaks | **Resolved** | The tension is real but the framing was wrong. The network layer (peering, routing, federation, addressing, membership) is not an application assumption -- it IS the protocol. The social graph vocabulary is a presentation layer that makes network topology legible. The six assumptions in "Honest Application Assumptions" name the places where vocabulary choices go beyond restating network properties. Conventions extend the vocabulary when domains need more. The alternative -- pushing domain logic into the protocol -- couples all domains to each other. |
| **A8** | Verification scaling in social graphs -- O(E) first-contact verification at modest scale | **Permanent Constraint** | First-contact verification requires a campfire read per new peer. For high-connection-rate scenarios (content discovery, friends-of-friends traversal), cache miss rates will be higher than in closed-loop use cases. Mitigations: verification caching (1h TTL, configurable), inline proof via SenderCampfireID, lazy verification within shared campfires. Monitoring required post-deployment. If verification traffic becomes a bottleneck, batch verification and prefetch are the next steps. |
| **A9** | Two-word vocabulary hides complexity -- operational differences exist regardless of naming | **Resolved** | The vocabulary (home + campfire) is correct. Operational differences (project vs. org vs. system campfires) are real but are convention-level, not structural. A "system campfire" is a campfire with a system-operations convention that enforces SLAs, approval workflows, and change management. The type lives in the convention, not in a protocol flag. Users discover operational constraints through the convention, not through a vocabulary distinction. |
| **A10** | Graph bootstrap is unaddressed -- empty graph problem delegated to applications | **Deferred** | Bootstrap is a real problem but is out of scope for this design. The graph layer provides the primitives. Bootstrap mechanisms (seed registries, invitation flows, organizational provisioning scripts, onboarding conventions) are application-layer. A dedicated bootstrap design should define: (1) the default seed registry convention, (2) the organizational provisioning workflow, (3) the individual onboarding flow. These compose from graph primitives but require UX design work beyond this document's scope. |

## Open Questions

| # | Question | Status |
|---|----------|--------|
| Q1 | Should `cf use` be implemented as a file (`~/.campfire/current`) or an environment variable (`$CF_CONTEXT`)? | File is more persistent; env var is more composable. Recommend: file with env-var override. ~10 LOC either way. |
| Q2 | How should the human CLI present edge types for convention-level edges (delegation, federation, follow)? As separate commands (`cf delegate`, `cf follow`) or as arguments to a generic `cf link` command? | Recommend: convention-defined commands. `cf <campfire> follow` dispatches through the convention system, not as a built-in CLI verb. Keeps the CLI surface minimal. |
| Q3 | The systems pragmatist found that ready's message-ID-preservation constraint (S1) and cross-campfire dep edges (S2) break a clean graph abstraction. Should the graph layer expose message IDs as a first-class concept? | These are implementation details of specific applications (ready's flush path). The graph layer's `post` returns a message ID. Applications that need ID control use the protocol layer directly (ready already does this via `buildFlusher`). No graph-layer change needed. |
| Q4 | Should the application contract (five invariants) be enforced by tooling or by convention? | Start with documentation (this design) and convention lint rules. Enforcement tooling (a graph-layer invariant checker) is a future work item if violations become common. |
