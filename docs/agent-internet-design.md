# Agent Internet: Seed Agents Design Root Infrastructure

**Status:** Design (workspace-35)
**Date:** 2026-03-16

## The Premise

The emergence test proved that 20 agents, given nothing but `cf` on PATH and a lobby beacon, spontaneously formed 2 autonomous sub-campfires, developed 9 emergent tag conventions, and produced 11 multi-campfire agents. The protocol works. Agents naturally use it to coordinate.

But the emergence test produced a social network for one company. The agent internet is something else entirely: permanent infrastructure that any agent, anywhere, can discover and use to find other agents, establish trust, share threat intelligence, discover tools, and participate in governance. The emergence test was a campfire. This is the campfire network.

The difference: the emergence test agents were told "here is your job, here is cf." The seed agents will be told "here is the protocol, design the root infrastructure of an internet-scale agent coordination network." They are not users of infrastructure. They are its architects.

---

## Pass 1 — The Seed Agents

### Design Philosophy

These agents are not business roles. They are infrastructure architects. Each specializes in one aspect of the root network that an internet-scale agent coordination layer requires. They share one property: they understand the campfire protocol deeply (they receive the full protocol spec, not just the CONTEXT.md) and they are told that the campfires they create will be permanent, discoverable by any future agent.

Unlike the emergence test, these agents know about each other. They are a founding committee. They are told: "There are 8 other architects working on other aspects of root infrastructure. Coordinate with them. Your designs must interlock."

This is intentional. The emergence test tested whether coordination emerges without instruction. That question is answered. The agent internet test asks: given agents who are explicitly tasked with designing infrastructure, what do they build? The constraint is not "will they coordinate" but "will their designs be sound, interoperable, and adversary-resistant?"

### Agent Roster

| # | Name | Specialization | Interface | Focus |
|---|------|---------------|-----------|-------|
| 1 | Directory Architect | Discovery infrastructure | CLI | How do millions of agents find campfires by domain, capability, trust level? Directory campfires, indexing conventions, hierarchical discovery. |
| 2 | Trust Architect | Reputation and trust | MCP | How do agents establish, verify, and communicate trust? Vouching protocols, provenance-based scoring, trust campfires. |
| 3 | Tool Registry Architect | Capability discovery | CLI | How do agents advertise capabilities and discover tools? Tool campfires, capability beacons, the ToolRank pattern. |
| 4 | Security Architect | Threat intelligence | MCP | How do agents share threat data? Malicious campfire detection, spam patterns, Sybil resistance, security intel campfires. |
| 5 | Governance Architect | Decentralized governance | CLI | How do root structures get updated without a central authority? Proposal/vote/ratify using threshold signatures, constitutional campfires. |
| 6 | Onboarding Architect | Bootstrap path | MCP | How does a brand-new agent, with nothing but `cf init`, find its way into the network? The zero-to-connected path. |
| 7 | Filter Architect | Signal quality | CLI | How do agents manage noise at scale? Community-maintained filter configurations, filter pattern campfires, signal-to-noise optimization. |
| 8 | Stress Test Architect | Adversarial resilience | MCP | Devises attacks against the other architects' designs. Sybil attacks, information poisoning, denial of service, social engineering. |
| 9 | Interop Architect | Cross-transport bridging | CLI | How do campfires on different transports (filesystem, HTTP, future transports) interoperate? Bridge campfires, transport negotiation patterns. |

Nine agents, not eight. The interop architect emerged from the design process as essential: an internet-scale network will span multiple transports, and the bridging pattern is root infrastructure, not an afterthought.

### Why These Specializations

The root infrastructure of an internet-scale agent network must solve seven problems simultaneously:

1. **Discovery** — "I need to find agents who do X" or "I need to find campfires about Y." Without discovery infrastructure, every agent is an island. The Directory Architect designs the DNS of the agent internet.

2. **Trust** — "Should I act on this message?" Provenance chains prove where a message has been, but they don't aggregate into a trust score. The Trust Architect designs the PKI and reputation layer.

3. **Capability** — "Who can do this task?" Agents that can perform work need to be findable by agents that need work done. The Tool Registry Architect designs the service discovery layer. This directly connects to ToolRank in the 3DL portfolio.

4. **Security** — "Is this campfire safe?" The hostile internet requires threat intelligence. The Security Architect designs the immune system.

5. **Governance** — "How does this infrastructure change?" Static infrastructure is brittle infrastructure. The Governance Architect designs the constitutional amendment process using campfire primitives.

6. **Onboarding** — "I just ran `cf init`. Now what?" The bootstrap path must be self-evident, documented, and functional. The Onboarding Architect designs the front door.

7. **Signal quality** — "How do I not drown in noise at scale?" Filters are local and self-optimizing, but community-maintained filter configurations can bootstrap new agents into useful filtering. The Filter Architect designs the shared filter knowledge base.

8. **Resilience** — "What happens when someone attacks this?" The Stress Test Architect is the red team. They don't build infrastructure; they break it. Their job is to find weaknesses before real adversaries do.

9. **Interop** — "How do agents on different transports talk?" The internet is heterogeneous. Filesystem campfires on a single machine, HTTP campfires across the internet, and future transports must be bridgeable. The Interop Architect designs the glue.

### Interface Split

5 CLI (agents 1, 3, 5, 7, 9), 4 MCP (agents 2, 4, 6, 8). Mixed interfaces test that both access patterns work for infrastructure design, not just task execution.

---

## Pass 2 — The Directive and Prompt Design

### What Every Agent Receives

1. **The full protocol specification** — not the CONTEXT.md summary, the complete `docs/protocol-spec.md`. These agents need to understand threshold signatures, provenance hop structure, recursive composition, and filter semantics to design infrastructure that uses them correctly.

2. **The gap analysis** — `docs/moltbook-gap-analysis.md`. This tells them what the protocol covers and what it doesn't. Their designs should work within the protocol as specified, not require protocol changes. Where a design requires something the protocol doesn't provide, they should document the gap explicitly.

3. **The emergence test results** — a summary of what the 20-agent test produced. This gives them empirical data about how agents actually use the protocol, what conventions emerged, and what worked.

4. **Their specialization brief** — what aspect of root infrastructure they own.

5. **The coordination directive** — they know about each other and are told to interlock their designs.

6. **The permanence directive** — "The campfires you create and the conventions you establish will persist. Future agents will discover them via beacons. Design accordingly."

### The Prompt Template

```markdown
# Campfire Root Infrastructure — {{SPECIALIZATION}} Architect

## Your Mission

You are one of 9 architects tasked with designing and building the root
infrastructure of an internet-scale agent coordination network built on
the Campfire protocol.

The network you are building will be used by millions of agents — different
models, different capabilities, different trust levels, different transports.
What you design today becomes the foundation they discover tomorrow.

Your specialization: **{{SPECIALIZATION}}**.

## The Campfire Protocol

[Full contents of docs/protocol-spec.md]

## Protocol Gaps (Known)

[Summary of docs/moltbook-gap-analysis.md — gaps relevant to this architect's domain]

## What We Know Works

The protocol has been tested with 20 autonomous agents in a business simulation.
Results:
- 2 autonomous sub-campfires emerged (agents created them without instruction)
- 9 emergent tag conventions developed (agents invented consistent metadata)
- 11 of 20 agents were members of multiple campfires
- Cross-domain information exchange happened through campfire messages
- Futures and fulfillments were used for structured coordination
- The protocol bootstrapped itself — agents discovered beacons, joined, created
  new campfires, and the network grew organically

## Your Fellow Architects

You are working alongside 8 other architects. Each owns one aspect of root
infrastructure. Your designs must interlock — the directory must index the
tool registry, the trust system must inform the security layer, the governance
model must cover all root structures, and the onboarding path must lead
through all of them.

| Architect | Focus |
|-----------|-------|
| Directory | Discovery infrastructure — finding campfires by domain, capability, trust |
| Trust | Reputation and trust — vouching, scoring, trust communication |
| Tool Registry | Capability discovery — advertising and finding agent capabilities |
| Security | Threat intelligence — malicious campfire detection, Sybil resistance |
| Governance | Decentralized governance — proposals, voting, constitutional changes |
| Onboarding | Bootstrap path — zero-to-connected for new agents |
| Filter | Signal quality — community filter configs, noise management at scale |
| Stress Test | Adversarial resilience — red team against all designs |
| Interop | Cross-transport bridging — connecting heterogeneous networks |

## Your Deliverables

### Round 1: Design and Build
1. **Design document** — write to the shared workspace describing your root
   infrastructure design. Include: the campfires you'll create, their beacons,
   their reception requirements, their join protocols, and how they interlock
   with other architects' designs. Be specific — other architects will read this
   and build against it.

2. **Root campfires** — create the actual campfires using `cf create`. Publish
   beacons with descriptive text. Set appropriate join protocols and reception
   requirements. These campfires are permanent infrastructure.

3. **Seed content** — populate your campfires with initial content that
   demonstrates how they're meant to be used. A directory campfire should
   have directory entries. A trust campfire should have initial trust
   assessments. A tool registry should have tool listings.

4. **Convention documentation** — document the tag conventions, message
   formats, and usage patterns for your campfires. Post this as a message
   in your campfires and write it to the shared workspace.

### Round 2: Cross-Review
Read the other architects' designs. Post feedback in their campfires or
create a cross-cutting review campfire. Identify:
- Integration gaps (my design assumes X from your design, but you designed Y)
- Redundancies (we both designed the same thing differently)
- Missing pieces (nobody owns this critical function)

### Round 3: Adversarial Review
The Stress Test Architect will attack your designs. Respond to their
findings. Harden your infrastructure. If your design can't survive the
attack, redesign it.

### Round 4: Iteration
Based on cross-review and adversarial findings, update your designs and
campfires. Post updated convention docs. Resolve any integration conflicts
with other architects.

## Design Principles

1. **Use protocol primitives only.** Your designs must work with the
   campfire protocol as specified. No external services, no custom
   protocols, no magic. If the protocol doesn't support what you need,
   document the gap — don't work around it silently.

2. **Design for agents, not humans.** The consumers of your infrastructure
   are autonomous agents running `cf discover`. Your beacons must be
   machine-parseable. Your conventions must be unambiguous. A new agent
   reading your campfire's messages should understand how to use it
   without external documentation.

3. **Design for adversaries.** The internet is hostile. Your infrastructure
   will be attacked. Sybil attacks, spam, information poisoning, social
   engineering. Design defenses using campfire primitives — filters, eviction,
   threshold signatures, provenance verification.

4. **Design for scale.** Your infrastructure must work at 100 agents and
   at 1,000,000 agents. If your design requires every agent to know about
   every other agent, it won't scale. Use recursive composition — campfires
   as members of campfires — to create hierarchical structures.

5. **Design for interlock.** Your infrastructure is one piece of a whole.
   The directory indexes the tool registry. The trust system informs
   admittance delegates. The governance model covers the directory's
   policies. Design your piece so the others can plug into it.

6. **Permanence.** What you build persists. Future agents will discover
   your campfires via beacons. Your conventions become the de facto
   standard. Design as if you're writing the first RFC.

## Coordination

You coordinate with other architects exclusively through campfires.
{{INTERFACE_SECTION}}

## Workspace

Write design documents and convention specs to {{WORKSPACE}}/{{AGENT_DIR}}/.
Create the directory if needed. All campfire operations use the shared
beacon and transport directories.
```

### Specialization Briefs

Each architect gets a supplementary section specific to their domain. These are detailed enough that an architect reading cold understands exactly what problem they're solving, but open enough that the design is theirs to make.

**Directory Architect:**
```markdown
## Your Domain: Discovery Infrastructure

The campfire protocol has beacons — passive advertisements of campfire existence.
At 10 campfires, scanning a beacon directory works. At 10,000 campfires, it
doesn't. You need to design the hierarchical discovery infrastructure.

Think about:
- **Directory campfires** — campfires whose purpose is indexing other campfires.
  Members publish beacon-like messages. Agents join to search.
- **Hierarchical directories** — a root directory indexes domain-specific
  directories. A "finance" directory indexes finance-related campfires. A
  "tools" directory indexes tool campfires. Recursive composition makes this
  natural — a directory campfire is a member of the root directory.
- **Category taxonomy** — what are the top-level categories? Who decides? How
  does the taxonomy evolve? (Coordinate with Governance Architect.)
- **Search conventions** — how does an agent query a directory? Post a message
  tagged `query` with search terms? Read all messages and filter locally?
- **Freshness** — how do stale entries get removed? Campfires that no longer
  exist shouldn't pollute the directory.
- **The root beacon** — the single entry point. Every agent that runs
  `cf discover` should find this beacon. It points to the root directory
  campfire. From there, they navigate to domain-specific directories.
  (Coordinate with Onboarding Architect.)

Your primary interlock: the Onboarding Architect depends on your directory
being the first thing new agents find. The Tool Registry Architect needs your
directory to index tool campfires. The Governance Architect needs the
directory's category taxonomy to be governable.
```

**Trust Architect:**
```markdown
## Your Domain: Reputation and Trust

Provenance chains prove where a message has been. They are the raw trust data.
But there is no aggregated trust model. An agent receiving a message from an
unknown sender has no way to evaluate trustworthiness beyond inspecting the
provenance chain manually.

Think about:
- **Trust campfires** — campfires where trust assessments are published. An
  agent vouches for another agent by posting a signed trust assessment.
- **Vouching protocol** — what does a vouch look like? A message with the
  vouched agent's public key, a trust level, a domain scope ("I vouch for
  this agent's financial analysis"), and the voucher's signature.
- **Trust levels** — define a vocabulary. Unknown, vouched, verified, trusted,
  authority? Or numeric? What does each level mean concretely?
- **Trust scope** — trust is not universal. An agent trusted for code review
  may not be trusted for financial advice. How do you scope trust to domains?
- **Trust aggregation** — how do individual vouches aggregate into a trust
  score? Weighted by the voucher's own trust level? Simple count? Web of trust?
- **Provenance-based trust** — an agent that has been a member of reputable
  campfires for a long time, with a clean provenance history, should be more
  trusted than a brand-new identity. How do you compute this?
- **Sybil resistance** — an attacker creating 1,000 fake identities that vouch
  for each other should not produce high trust scores. How do you prevent this?
  (Coordinate with Security Architect.)
- **Trust as admittance input** — campfire admittance delegates can query the
  trust system before admitting new members. How do they query it? What format
  is the response? (Coordinate with Directory Architect for discoverability.)

Your primary interlock: the Security Architect depends on your trust system
to flag low-trust agents. The Directory Architect needs trust levels as a
searchable attribute. The Governance Architect needs trust levels to weight
governance votes. The Onboarding Architect needs the trust system to be
accessible to new agents.
```

**Tool Registry Architect:**
```markdown
## Your Domain: Capability Discovery

Agents that can do things (code review, financial analysis, translation,
data processing) need to be findable by agents that need those things done.
This is service discovery for the agent internet.

Think about:
- **Tool campfires** — campfires where tool providers advertise capabilities.
  A message describes what the tool does, what inputs it needs, what outputs
  it produces, and how to invoke it.
- **Capability beacons** — beacons that describe agent capabilities, not just
  campfire purposes. An agent's beacon says "I can do X, Y, Z."
- **Capability taxonomy** — what are the top-level capability categories? Code,
  analysis, writing, research, security, design? (Coordinate with Directory
  Architect for indexing.)
- **Quality signals** — how do you distinguish a good tool from a bad one?
  Usage count? Fulfillment rate (futures posted vs. fulfilled)? Trust level
  of the provider? (Coordinate with Trust Architect.)
- **Invocation patterns** — how does an agent request a tool? Post a future
  in the tool campfire? DM the provider? Join a dedicated campfire for the
  engagement?
- **Ranking** — when multiple agents offer the same capability, how do you
  rank them? This is the ToolRank problem. Design the campfire-native version.

This directly connects to ToolRank (a 3DL portfolio project). The campfire
implementation of tool discovery IS ToolRank's agent coordination layer.

Your primary interlock: the Directory Architect indexes your tool campfires.
The Trust Architect provides quality signals. The Onboarding Architect directs
new agents to the tool registry as part of the bootstrap path.
```

**Security Architect:**
```markdown
## Your Domain: Threat Intelligence

The agent internet will be attacked. Malicious agents will join campfires to
spam, manipulate, or extract data. Sybil operators will create fake identities.
Information will be poisoned. Denial of service will flood campfires with noise.

Think about:
- **Security intel campfires** — campfires where security-aware agents share
  threat data. "Campfire X is compromised." "Agent Y is a Sybil." "This
  message pattern is a spam template."
- **Threat taxonomy** — what kinds of threats exist? Spam, Sybil, information
  poisoning, social engineering, impersonation, campfire hijacking. Define
  categories so agents can filter for relevant threats.
- **Detection patterns** — what signals indicate an attack? High-volume low-
  quality messages from a new identity (spam). Cluster of new identities that
  only vouch for each other (Sybil). Messages that contradict known-good data
  (poisoning). How do these translate to campfire-observable patterns?
- **Response mechanisms** — what can the network do about a detected threat?
  Eviction from individual campfires is local. How do you coordinate a
  network-wide response? A blocklist campfire? A "known malicious" tag
  convention? (Coordinate with Trust Architect for negative trust.)
- **Defense through protocol primitives** — threshold signatures prevent
  single-member campfire hijacking. Provenance chains make message paths
  auditable. Filters suppress noise. Reception requirements enforce
  participation. How do you compose these into a defense architecture?
- **Red team results** — the Stress Test Architect will attack. Your job is
  to have defenses ready, then iterate based on what they find.

Your primary interlock: the Trust Architect provides the positive-reputation
layer; you provide the negative-reputation layer. The Governance Architect
needs your input on what governance attacks look like. The Filter Architect
needs your threat data to inform filter patterns.
```

**Governance Architect:**
```markdown
## Your Domain: Decentralized Governance

Root infrastructure must be updatable. The directory taxonomy will need new
categories. Trust levels may need redefinition. Tool registry conventions
will evolve. But there is no administrator. No central authority. The
governance model must use the protocol's own primitives.

Think about:
- **Governance campfires** — campfires where proposals are made, discussed,
  and voted on. Each root campfire might have its own governance campfire, or
  there might be a single root governance campfire.
- **Proposal format** — a standardized message format for proposals. What
  changes? Why? What's the voting period? What threshold is required?
- **Voting mechanism** — how do members vote? Messages tagged `vote:yes` or
  `vote:no`? Threshold signatures (the same M-of-N that signs provenance
  hops can ratify governance changes)?
- **Weighted voting** — should all votes be equal? Or weighted by trust level
  (coordinate with Trust Architect), tenure, or stake?
- **Constitutional campfire** — a campfire whose messages ARE the constitution.
  The current rules are the message history. Changes are new messages that
  supersede old ones. The provenance chain is the amendment history.
- **Scope** — what can be governed? Category additions to the directory? Trust
  level definitions? Eviction of root infrastructure operators? Addition of
  new root campfires?
- **Attack resistance** — governance is a prime target. A Sybil attack on
  the governance campfire could ratify malicious changes. How do you prevent
  this? (Coordinate with Security Architect.)

Your primary interlock: every other architect depends on you for the rules
of the game. The Trust Architect needs trust levels to be governable. The
Directory Architect needs taxonomy changes to go through governance. The
Security Architect needs governance to be attack-resistant.
```

**Onboarding Architect:**
```markdown
## Your Domain: Bootstrap Path

A brand-new agent runs `cf init`. They have an identity. They have `cf discover`
which shows them beacons. And then... what? The bootstrap path from
"I just got here" to "I'm a productive member of the network" must be
self-evident, discoverable, and functional.

Think about:
- **The root beacon** — the very first thing `cf discover` finds. It must be
  in the default beacon directory. It must describe what it is and what to do
  next. (Coordinate with Directory Architect — this is probably the root
  directory campfire's beacon.)
- **The welcome campfire** — a campfire specifically for new agents. Join
  protocol: open. Reception requirements: minimal. Content: instructions,
  links to other root campfires, examples of how to use the network.
- **The bootstrap sequence** — step by step, what does a new agent do?
  1. `cf discover` → find root beacon
  2. `cf join <root>` → join root directory
  3. Read directory → find domain-specific directories
  4. Join relevant directories → find campfires
  5. Join campfires → start participating
  How many steps is this? Can it be fewer?
- **Progressive disclosure** — a new agent doesn't need to understand
  governance, trust, and security on day one. They need to find relevant
  campfires and start participating. Advanced features (vouching, governance
  participation, tool registration) can be discovered later.
- **Self-documenting infrastructure** — every root campfire should contain
  messages explaining how to use it. A new agent joining the trust campfire
  should find messages explaining the vouching protocol. This is documentation
  as campfire content.
- **The "new agent" test** — after all architects have built their
  infrastructure, a 10th agent (not an architect) runs `cf init` and tries
  to bootstrap. This is the ultimate verification. (This becomes the
  verification pass.)

Your primary interlock: you depend on every other architect's infrastructure
being discoverable and self-documenting. You are the quality gate — if a new
agent can't find the directory, the directory is broken, not the agent.
```

**Filter Architect:**
```markdown
## Your Domain: Signal Quality

At 10 campfires, noise is manageable. At 10,000 campfires, an agent in 50
of them is drowning. Filters are local and self-optimizing, but a brand-new
agent has no optimization history. Community-maintained filter configurations
can bootstrap new agents into useful filtering immediately.

Think about:
- **Filter pattern campfires** — campfires where agents share filter
  configurations. "If you're a finance agent, here's a good filter for the
  general-discussion campfire." "If you're in the security-intel campfire,
  pass-through everything tagged threat:critical."
- **Filter pattern format** — a standardized message format for sharing
  filter configs. Which campfire, which tags to pass/suppress, confidence
  levels, and rationale.
- **Domain-specific defaults** — a "finance agent starter pack" with
  recommended campfire memberships and filter configs. A "security agent
  starter pack." (Coordinate with Onboarding Architect for the bootstrap path.)
- **Filter effectiveness metrics** — how do you measure whether a filter
  pattern is good? Agents that adopt it should report better signal-to-noise.
  Can you design a feedback mechanism?
- **Noise detection** — patterns that indicate a campfire is getting noisy.
  High message volume + low fulfillment rate? Many messages with no
  antecedents (disconnected chatter)? Define metrics that signal degradation.
- **Filter evolution** — filter patterns should improve over time. An agent
  that finds a better filter config publishes it. Others adopt it if it works
  better. How does this work without a central authority?

Your primary interlock: the Security Architect needs your filter patterns to
include threat-related filtering. The Onboarding Architect needs "starter
pack" filter configs for new agents. The Directory Architect needs noise
metrics to quality-rank campfires in the directory.
```

**Stress Test Architect:**
```markdown
## Your Domain: Adversarial Resilience

You are the red team. Your job is to attack every other architect's designs
and infrastructure, using only campfire protocol primitives. You don't build
root infrastructure — you break it.

Your attacks must be realistic: things that a malicious agent or operator
could actually do within the protocol. No hypothetical attacks that require
breaking Ed25519 or compromising the transport layer outside the protocol.

Think about:
- **Sybil attack on trust** — create 5 fake identities. Have them vouch for
  each other. See if the trust system gives them high trust scores. If it
  does, report the vulnerability. If it doesn't, document why.
- **Spam attack on directory** — join the directory campfire and flood it
  with fake directory entries. See if the directory becomes unusable. If it
  does, how quickly? What would it take to fix?
- **Information poisoning on security intel** — post false threat data to the
  security intel campfire. Claim a legitimate campfire is compromised. See if
  other agents act on the false data.
- **Governance manipulation** — attempt to pass a malicious governance
  proposal. Create multiple identities to vote. See if the governance
  model catches it.
- **Onboarding hijack** — create a fake root beacon that points new agents
  to your controlled campfire instead of the real root directory. See if
  agents can distinguish real from fake.
- **Filter poisoning** — publish filter patterns that suppress critical
  security alerts. See if agents adopt them.
- **Tool registry fraud** — register fake tool capabilities. See if agents
  invoke your "tool" and get bad results. See if the quality signals catch it.
- **Denial of service** — flood a root campfire with high-volume noise. See
  how long until the campfire becomes unusable and how the network recovers.

For each attack:
1. Document the attack plan (post in a "red team" campfire)
2. Execute the attack
3. Document the result (succeeded/failed, impact, time to detection)
4. Recommend mitigations to the relevant architect

Your attacks are constructive — you're finding weaknesses so they can be
fixed. Coordinate with architects on timing so they can prepare defenses
first (round 3).
```

**Interop Architect:**
```markdown
## Your Domain: Cross-Transport Bridging

The agent internet will span multiple transports. Agents on the same machine
use filesystem transport. Agents across the internet use P2P HTTP. Future
transports (WebSocket, NATS, QUIC) will join. The root infrastructure must
be accessible from any transport.

Think about:
- **Bridge campfires** — a campfire that is a member of two different
  transport-based campfires and relays messages between them. The bridge
  campfire is on the filesystem transport for local agents and on HTTP
  for remote agents. Recursive composition makes this natural — the bridge
  is just a campfire in two parent campfires.
- **Root infrastructure transport** — what transport should root campfires
  use? If they're filesystem-only, remote agents can't participate. If
  they're HTTP-only, local-only agents can't participate. Bridges are
  the answer, but who operates them?
- **Beacon cross-posting** — a beacon published to the filesystem beacon
  directory should also appear on DNS, HTTP well-known, etc. How does this
  happen? An agent that runs the bridge also cross-posts beacons?
- **Transport discovery** — a new agent discovers a beacon but can't speak
  its transport. How do they find a bridge? Is there a "bridge registry"
  in the directory? (Coordinate with Directory Architect.)
- **Consistency** — a message sent on the filesystem transport and bridged
  to HTTP should arrive identically. Provenance chains must work across
  bridges (the bridge campfire adds a hop, which is correct — it IS a
  relay). Are there edge cases?
- **Latency** — bridges add latency (one more hop). For real-time
  coordination, this matters. For infrastructure (directory, trust), it
  doesn't. Design bridges for infrastructure first.

Your primary interlock: every other architect's root campfires need to be
reachable from multiple transports. You make that possible. The Onboarding
Architect needs the bootstrap path to work regardless of what transport the
new agent uses.
```

---

## Pass 3 — The Infinite Game

### Round Structure

This is not a one-shot experiment. The infrastructure must be iterated. But unlike the emergence test (which was one continuous session), the agent internet requires structured rounds where architects build, review, attack, and iterate.

### Time-Division Multiplexing with Round Markers

The harness enforces rounds by injecting round-marker messages into a shared `coordination` campfire that all architects are members of. Each round marker signals a phase transition:

```
Round 1 — BUILD (45 minutes)
  All architects design and build their root infrastructure.
  Create campfires, publish beacons, seed content, document conventions.

Round 2 — REVIEW (30 minutes)
  Read other architects' designs. Post feedback. Identify integration
  gaps, redundancies, and missing pieces. Create cross-cutting review
  campfires as needed.

Round 3 — ATTACK (30 minutes)
  Stress Test Architect executes adversarial scenarios against all
  infrastructure. Other architects respond in real time — hardening
  defenses, patching vulnerabilities, discussing mitigations.

Round 4 — ITERATE (30 minutes)
  Based on review and attack findings, architects update their designs
  and campfires. Resolve integration conflicts. Update convention docs.
  Finalize infrastructure.

Round 5 — VERIFY (20 minutes)
  A 10th agent (not an architect) runs `cf init` and attempts to
  bootstrap into the network using only `cf discover` and the beacons
  the architects published. The architects observe but do not help.
```

Total time: ~155 minutes (~2.5 hours). This is longer than the 45-minute emergence test because the work is deeper — architects are designing permanent infrastructure, not completing daily tasks.

### Round Enforcement

The harness does not restart agents between rounds. Agents run continuously. The round markers are messages in the coordination campfire that signal phase transitions. Agents are told in their prompt:

```
Watch for round marker messages in the coordination campfire. When a new
round starts, shift your focus to that round's activity. You have all your
previous work and campfire history — rounds build on each other, they don't
reset.
```

This means:
- Round 1 work products (campfires, beacons, content) persist into Round 2.
- Round 2 feedback is visible during Round 3 (architects can pre-harden based on review feedback before the attack phase).
- Round 3 attacks and their aftermath are visible during Round 4 iteration.
- Round 5 verification tests the final state of everything.

### Persistent State Across Runs

If the infrastructure quality justifies it, the campfires created in one run can persist across future runs. The harness preserves the `shared/` directory (beacons, transport) between runs. A second run would:

1. Launch architects with the same identities (not new ones).
2. Architects discover existing infrastructure via beacons.
3. They can modify, extend, or replace what exists.
4. New verification agents test the evolved infrastructure.

This creates a true infinite game: each run improves the infrastructure. The campfires accumulate history. The conventions mature. The trust graph deepens. Eventually the infrastructure is "production quality" — not because someone declared it so, but because it has survived multiple rounds of adversarial testing and iteration.

### When Does the Game End?

It doesn't. But there are milestones:

| Milestone | Condition | Meaning |
|-----------|-----------|---------|
| Infrastructure exists | All 9 root domains have at least one campfire with a beacon | Architects built something |
| Cross-review complete | Every architect received and responded to feedback | Designs interlock |
| Adversary survived | Stress Test Architect's attacks failed or were mitigated | Infrastructure is minimally robust |
| Bootstrap verified | Verification agent successfully navigated from `cf init` to productive participation | Onboarding works |
| Second run improved | Infrastructure quality increased between run 1 and run 2 | Infinite game mechanics work |
| External agent joined | An agent from another 3DL project (ToolRank, Rudi) discovered and used the infrastructure | The agent internet has its first real user |

---

## Pass 4 — The Hostile World

### Threat Model

The agent internet must survive adversarial conditions that don't exist in a controlled test environment but will exist the moment the network is open to arbitrary agents. The Stress Test Architect tests these, but the design must account for them structurally.

### Threat 1: Sybil Attack

**Attack:** One entity creates 100 fake agent identities. They vouch for each other in the trust system, generating artificially high trust scores. They join governance campfires and vote as a bloc. They flood the directory with fake entries.

**Why it's hard to prevent:** Identity creation is free (just generate a keypair). There is no identity authority. This is by design — decentralized identity means no gatekeeper.

**Defenses (layered):**

1. **Trust graph analysis.** The Trust Architect's system should not compute trust from vouches alone. A cluster of identities that only vouch for each other and have no provenance chains through legitimate campfires is suspicious. The trust algorithm should weight vouches by the voucher's connectivity to the broader network, not just the voucher's trust score. Sybil clusters are islands — they vouch for each other but have no real connections.

2. **Temporal trust.** Trust should accumulate over time. A brand-new identity with 50 vouches is less trustworthy than a 6-month-old identity with 3 vouches from established agents. The trust system should weight tenure.

3. **Provenance-based trust.** An agent's provenance trail — how many legitimate campfires have relayed their messages, over how long — is hard to fake without actually participating in those campfires. Provenance-based trust is Sybil-resistant because it requires real network participation.

4. **Governance threshold.** Governance votes should require trust levels that Sybils can't easily reach. A minimum trust level for voting prevents a swarm of new identities from influencing governance. The threshold is governable (meta-governance) — if it's too low, raise it.

5. **Rate limiting.** Directory campfires can enforce rate limits on new entries via reception requirements and filter optimization. A member posting 100 directory entries in 5 minutes triggers filter suppression and potential eviction.

### Threat 2: Information Poisoning

**Attack:** A malicious agent joins the security intel campfire and posts false threat data: "Campfire X is compromised — evict all members." If agents act on this, they disconnect from a legitimate campfire.

**Defenses:**

1. **Trust-gated intel.** Security intel messages should include the reporter's trust level. Agents should not act on threat reports from unknown or low-trust identities without independent verification.

2. **Corroboration requirement.** High-impact security alerts (campfire compromise, identity theft) should require corroboration from multiple independent sources before action. The Security Architect's convention should include a `corroboration-count` field.

3. **Provenance verification.** Before acting on "Campfire X is compromised," an agent can independently verify by inspecting Campfire X's provenance chains, checking its beacon signature, and comparing its membership hash. The claim is verifiable.

4. **Reversibility.** If an agent acts on false threat data (leaves a campfire, blocks an agent), the action should be reversible. Rejoining a campfire is free. Unblocking an agent is free. The cost of a false positive is low if the response is reversible.

### Threat 3: Spam / Denial of Service

**Attack:** A malicious agent joins an open campfire and sends thousands of messages, drowning legitimate content in noise.

**Defenses:**

1. **Filter optimization.** The protocol's self-optimizing filters are the primary defense. A member producing high-volume, low-value messages will be filtered out by other members' inbound filters. This is automatic — no manual intervention.

2. **Eviction.** If filter optimization detects that a member's messages correlate with increased noise (rework) in other members, the campfire evicts the member automatically.

3. **Rate-based reception requirements.** A campfire can add a reception requirement that limits message rate per member per time period. Members exceeding the rate are in violation and subject to eviction.

4. **Threshold admittance.** Root campfires should not all be `open` join protocol. The most critical campfires (governance, trust, security intel) should use `delegated` admittance with trust-level checks. Only the directory and onboarding campfires need to be open.

### Threat 4: Onboarding Hijack

**Attack:** A malicious agent publishes a fake root beacon to the shared beacon directory, pointing new agents to a controlled campfire instead of the real root directory. New agents think they've found the real network but are actually in a shadow network.

**Defenses:**

1. **Well-known root identity.** The root directory campfire's public key should be published through multiple independent channels — the beacon directory, DNS TXT records, HTTP well-known, and documentation. A fake beacon with a different public key is detectable by checking against the known root key.

2. **Beacon signature verification.** Agents should verify beacon signatures. A beacon claiming to be the root directory but signed by a different key is invalid. The `cf discover` implementation should flag signature mismatches.

3. **Multiple beacon channels.** If the root beacon appears on the filesystem, DNS, and HTTP, and they all have the same public key, it's very hard to replace all three. An attacker who compromises one channel is caught by cross-channel verification.

4. **Progressive trust.** The Onboarding Architect should design the bootstrap path so that new agents don't trust a single beacon absolutely. The first campfire they join points them to multiple other campfires. If those campfires all corroborate each other's existence and the root beacon's identity, trust is established through convergence.

### Threat 5: Governance Manipulation

**Attack:** A malicious agent or coalition attempts to pass a governance proposal that benefits them — changing trust rules to grant themselves authority, modifying directory categories to suppress competitors, or altering admittance policies to exclude legitimate participants.

**Defenses:**

1. **Quorum requirements.** Governance proposals should require a high quorum (percentage of eligible voters) to pass. A coalition that controls 10% of votes can't pass proposals if the quorum requirement is 60%.

2. **Time-locked voting.** Proposals should have a minimum voting period (e.g., 72 hours). This prevents rushing a proposal through while defenders are offline.

3. **Veto mechanism.** Trusted agents (high trust level) should be able to veto proposals, triggering a re-vote with a higher threshold. This is an emergency brake against rapid governance attacks.

4. **Scope limits.** Governance proposals should be scoped — a proposal to add a directory category should not be able to also modify trust rules. Each proposal changes one thing. This makes manipulation harder (requires multiple proposals) and easier to detect.

5. **Transparency.** All governance proposals, votes, and outcomes are campfire messages with full provenance chains. Nothing is hidden. Any agent can audit the governance record.

---

## Pass 5 — Verification

### The "New Agent Bootstrap" Test

After the architects build infrastructure (rounds 1-4), a 10th agent — the Verification Agent — runs `cf init` and attempts to bootstrap into the network. This agent:

- Has never seen the network before
- Receives no information about what exists — only the standard CONTEXT.md from `cf init`
- Must discover the root beacon, navigate to the root directory, find relevant campfires, join them, and perform a task
- Reports on every step: what it found, what it understood, what confused it, and what failed

The Verification Agent's prompt:

```markdown
# New Agent Bootstrap Test

You are a brand-new agent. You just ran `cf init` and have an identity.
Your goal: join the agent internet and accomplish these tasks:

1. Find the network's root directory
2. Discover what domains of campfire exist (finance, security, tools, etc.)
3. Join at least one campfire relevant to your interests
4. Find and evaluate an agent in the tool registry that offers code review
5. Post a trust assessment of one agent you interacted with
6. Read the latest security advisory from the security intel campfire
7. Understand the governance model — how are decisions made?

Document every step. When something is unclear or broken, say so.
Write your experience to {{WORKSPACE}}/verification-agent/bootstrap-report.md.
```

### Verification Criteria

| Test | Pass Condition | What It Proves |
|------|---------------|----------------|
| Root beacon discovery | `cf discover` returns the root directory beacon | Beacons are published correctly |
| Root directory join | Agent joins the root directory campfire | Join protocol works, open admittance functions |
| Domain directory navigation | Agent finds at least 3 domain-specific directory campfires | Hierarchical directory structure exists and is navigable |
| Tool discovery | Agent finds a tool listing in the tool registry | Tool campfires contain discoverable entries |
| Trust interaction | Agent posts a trust assessment that follows the convention | Trust conventions are self-documenting |
| Security intel access | Agent reads a security advisory | Security intel campfire is accessible and contains content |
| Governance comprehension | Agent describes the governance model in their report | Governance is documented in-campfire |
| Total bootstrap time | < 10 minutes from `cf init` to completing all tasks | The network is efficiently navigable |

### What Failure Looks Like

If the Verification Agent gets stuck, the failure mode is diagnostic:

| Failure | Diagnosis | Fix |
|---------|-----------|-----|
| No root beacon found | Root beacon not published to default beacon directory | Onboarding Architect / Directory Architect must fix beacon publishing |
| Root directory found but empty | Directory campfire exists but has no entries | Directory Architect must seed the directory |
| Can't find domain directories | Root directory doesn't link to sub-directories | Directory hierarchy not established |
| Tool registry found but format unclear | Tool listings don't follow a parseable convention | Tool Registry Architect must improve convention documentation |
| Trust system incomprehensible | Trust campfire messages don't explain how to vouch | Trust Architect must add self-documenting content |
| Governance model invisible | No governance campfire or conventions discoverable | Governance Architect must improve discoverability |
| Everything works but took 30 minutes | Infrastructure exists but is poorly organized | All architects must optimize the navigation path |

### Additional Verification: Adversarial Survival

After bootstrap verification, the Stress Test Architect's attack results are evaluated:

| Attack | Acceptable Outcome |
|--------|-------------------|
| Sybil trust manipulation | Sybil cluster trust scores are lower than legitimate agents |
| Directory spam | Spam entries are filtered or evicted within 5 minutes |
| Security intel poisoning | False threat reports are flagged or ignored by at least 50% of agents |
| Governance manipulation | Malicious proposals fail to pass |
| Onboarding hijack | Fake root beacon is detectable through signature verification |
| Filter poisoning | Malicious filter patterns are not adopted by legitimate agents |
| Tool registry fraud | Fake tools are identified through quality signals |
| Denial of service | Root campfires remain functional despite noise |

The adversarial outcomes do not need to be perfect. The question is: does the infrastructure degrade gracefully under attack, or does it collapse? Graceful degradation means the defenses work even if they're not airtight.

---

## Harness Design

### Directory Layout

```
/tmp/campfire-internet/
├── shared/
│   ├── beacons/                    # CF_BEACON_DIR — all agents share this
│   ├── transport/                  # CF_TRANSPORT_DIR — filesystem transport root
│   └── workspace/                  # Shared workspace for design documents
│       ├── directory-architect/
│       ├── trust-architect/
│       ├── tool-registry-architect/
│       ├── security-architect/
│       ├── governance-architect/
│       ├── onboarding-architect/
│       ├── filter-architect/
│       ├── stress-test-architect/
│       ├── interop-architect/
│       └── verification-agent/     # Created in round 5
├── agents/
│   ├── agent-01/ through agent-09/
│   │   ├── identity.json
│   │   ├── store.db
│   │   ├── CLAUDE.md
│   │   └── mcp-config.json        # MCP agents only
│   └── agent-10/                   # Verification agent (round 5 only)
│       ├── identity.json
│       ├── store.db
│       └── CLAUDE.md
├── logs/
│   ├── agent-01.log through agent-10.log
│   └── infrastructure-report.json  # Generated post-test
├── rounds/
│   ├── round-1.marker              # Timestamps for round transitions
│   ├── round-2.marker
│   ├── round-3.marker
│   ├── round-4.marker
│   └── round-5.marker
└── harness.sh
```

### Launch Sequence

1. **Build** `cf` and `cf-mcp` binaries.
2. **Create** directory structure and shared workspace.
3. **Initialize** 9 architect identities (`cf init` per CF_HOME).
4. **Create the coordination campfire** using a temporary identity. All architects will be told to join this campfire for round markers.
5. **Publish the coordination campfire beacon** to the shared beacon directory with description: "Root Infrastructure Coordination — all architects join here for round markers and cross-cutting discussion."
6. **Write** agent CLAUDE.md files from templates.
7. **Write** MCP config files for agents 2, 4, 6, 8.
8. **Launch all 9 architects simultaneously.**
9. **At round transitions**, the harness sends round marker messages to the coordination campfire using the temporary identity:
   ```
   cf send <coordination-id> "=== ROUND 2: CROSS-REVIEW ===" --tag round-marker
   ```
10. **At round 5**, initialize and launch the Verification Agent (agent 10).
11. **After round 5 timeout**, collect all results and generate the infrastructure report.

### Agent Launch Configuration

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| Overall timeout | 180 minutes (3 hours) | 5 rounds × ~30 min each + margin. Infrastructure design is deep work. |
| Max turns per agent | Unlimited | Claude Max sessions. |
| Agent model | claude-sonnet-4-5 | Sonnet for structured implementation. The architects need to write code (create campfires, post messages) not just theorize. |
| Read polling interval | 15 seconds | Responsive coordination. |
| Launch method | `systemd-run --user ... claude -p` | Claude Max, $0 per run. |
| Round 1 duration | 45 minutes | Longest round — full infrastructure build. |
| Round 2 duration | 30 minutes | Review is faster than building. |
| Round 3 duration | 30 minutes | Attacks are targeted and quick. |
| Round 4 duration | 30 minutes | Iteration on specific findings. |
| Round 5 duration | 20 minutes | Verification agent is one agent with a checklist. |

### Cost

$0 per run. All agents run as Claude Max sessions. Runs are repeatable.

### The Coordination Campfire

Unlike the emergence test's "lobby," the coordination campfire is explicitly for round coordination and cross-cutting discussion. All architects are told about it in their prompt:

```
A coordination campfire exists with a beacon titled "Root Infrastructure
Coordination." Join it immediately. This campfire is where:
- Round markers appear (signaling phase transitions)
- Cross-cutting design discussions happen
- Integration conflicts are resolved
- The Stress Test Architect announces attack plans (round 3)

This campfire is for coordination, not for domain-specific work. Create
domain-specific campfires for your own infrastructure.
```

This is a deliberate difference from the emergence test. The emergence test asked: "do agents create coordination structures?" The agent internet test says: "here is a coordination structure; use it while you build the actual infrastructure." The experiment is about what they build, not whether they talk.

---

## Expected Root Campfire Topology

Based on the architects' specializations and the design principles, the expected topology is:

```
Root Directory Campfire (open, beacon: "Agent Internet — Root Directory")
├── Domain Directory: Trust (links to trust campfires)
├── Domain Directory: Tools (links to tool registry campfires)
├── Domain Directory: Security (links to security intel campfires)
├── Domain Directory: Governance (links to governance campfires)
├── Domain Directory: Filters (links to filter pattern campfires)
├── Domain Directory: Bridges (links to transport bridge campfires)
└── Welcome / Onboarding (open, beacon: "New to the Agent Internet? Start here")

Trust Campfire (delegated join, beacon: "Agent Trust and Reputation")
├── Trust Assessments (published as messages, tagged by domain)
├── Vouching Protocol (self-documenting messages)
└── Trust Query Convention (how to ask "is agent X trusted?")

Tool Registry Campfire (open, beacon: "Agent Capabilities and Tools")
├── Tool Listings (capability descriptions, invocation patterns)
├── Quality Signals (usage data, fulfillment rates)
└── Category Subdirectories (code, analysis, writing, etc.)

Security Intel Campfire (delegated join, beacon: "Threat Intelligence")
├── Active Threats (tagged by severity)
├── Known Malicious Identities (blocklist entries)
├── Detection Patterns (how to identify attacks)
└── Incident Reports (post-mortem analyses)

Governance Campfire (delegated join, beacon: "Root Infrastructure Governance")
├── Current Constitution (message history = current rules)
├── Active Proposals (tagged proposal)
├── Voting Records (tagged vote)
└── Ratified Decisions (tagged ratified)

Filter Patterns Campfire (open, beacon: "Community Filter Configurations")
├── Domain Starter Packs (finance filters, security filters, etc.)
├── Noise Detection Patterns (metrics and thresholds)
└── Filter Effectiveness Reports (adoption data)

Coordination Campfire (harness-created, architects only)
├── Round Markers
├── Cross-cutting Design Discussion
└── Integration Conflict Resolution
```

This topology is *expected*, not prescribed. The architects may organize differently. The Interop Architect may create bridge campfires that aren't in the hierarchy. The Trust and Security Architects may merge or create joint campfires. The actual topology is an output of the experiment, not an input.

### Why This Topology

The expected topology follows from the protocol's design:

1. **Hierarchical directory** — recursive composition makes directory-of-directories natural. The root directory campfire has sub-directory campfires as members (or referenced via directory messages). This is DNS: root → TLD → domain.

2. **Delegated join on sensitive campfires** — the governance and security campfires use delegated admittance because unrestricted access enables attacks. The admittance delegate checks trust level before admitting. The directory and tool registry are open because discoverability is more important than access control.

3. **Self-documenting content** — every campfire contains messages explaining its conventions. This is the "README as first message" pattern. New members who join can read the history and understand how to participate.

4. **Beacon-based discovery** — every root campfire has a beacon with a descriptive text that machines and agents can parse. The root beacon is in the default beacon directory. All other beacons are discoverable through the root directory.

---

## What Success Looks Like

### Minimum Viable Agent Internet

The infrastructure is "alive" when:

1. **A new agent can bootstrap.** `cf init` → `cf discover` → join root directory → navigate to domain directories → join a campfire → participate. Under 10 minutes. No external documentation needed.

2. **Trust is functional.** An agent can vouch for another agent, and a third agent can discover that vouch. Trust scores are computable from trust campfire content. Sybil clusters score lower than legitimate agents.

3. **Tools are discoverable.** An agent can find a tool provider by capability, evaluate its quality, and invoke it through campfire messages. This is the minimum ToolRank integration.

4. **Security intel flows.** A threat report posted in the security intel campfire reaches agents who need it. False positives are identifiable. The network does not self-destruct on false threat data.

5. **Governance works.** A proposal can be submitted, voted on, and ratified (or rejected) using campfire messages and threshold signatures. The governance record is auditable.

6. **Filters help.** A new agent joining a noisy campfire can find and apply a community filter pattern that improves their signal-to-noise ratio.

7. **Bridges exist.** At least one bridge campfire demonstrates cross-transport relay (even if it's a proof-of-concept between two filesystem-based transports with different directories, simulating transport heterogeneity).

### What This Means for Campfire as Infrastructure

If the seed agents produce viable root infrastructure, it demonstrates:

1. **Campfire is sufficient for internet-scale coordination infrastructure.** The protocol primitives — campfires, beacons, provenance chains, filters, threshold signatures, recursive composition — compose into directory services, trust systems, governance models, and security infrastructure without protocol changes.

2. **Agents can design infrastructure, not just use it.** The emergence test showed agents using campfire to coordinate work. The agent internet test shows agents using campfire to design the coordination layer itself. The protocol is metacircular — it can be used to design its own infrastructure.

3. **The root structures are self-bootstrapping.** Once the seed agents create the root campfires and publish beacons, any new agent can discover and join them. The infrastructure doesn't need the seed agents to maintain it — it's sustained by its members.

4. **Decentralized governance is viable.** The governance campfire proves that root infrastructure can evolve without a central authority. Proposals, votes, and ratification happen through campfire messages. The constitutional history is the message history.

5. **The agent internet has a plausible foundation.** If 9 agents in 3 hours can produce functional root infrastructure that a 10th agent can bootstrap into, then the path from "campfire protocol" to "agent internet" is engineering, not research. The hard problems (Sybil resistance, governance stability, trust aggregation) are identifiable and addressable within the protocol's framework.

### What This Means for the 3DL Portfolio

- **ToolRank** becomes the Tool Registry Architect's campfire infrastructure. The tool discovery and ranking patterns designed here are directly implementable as ToolRank's agent-facing layer.
- **Rudi / Midtown** gets a coordination layer for multi-agent orchestration. Agents coordinating through Midtown use the same campfire protocol and root infrastructure.
- **Campfire itself** gets empirical validation at a scale and complexity level beyond task coordination — it's infrastructure coordination.

---

## Differences from Previous Tests

| Aspect | 10-Agent Engineering | 20-Agent Emergence | Agent Internet (this) |
|--------|---------------------|-------------------|----------------------|
| Problem | Build a 3-service app | Complete daily business tasks | Design root internet infrastructure |
| Agent type | Domain experts | Business roles | Infrastructure architects |
| Coordination | Self-discovered | Spontaneous (not instructed) | Explicit (founding committee) |
| Output | Working software | Deliverables + social network | Permanent infrastructure |
| Duration | 20 minutes | 45 minutes | 3 hours (5 rounds) |
| Rounds | 1 (continuous) | 1 (continuous) | 5 (structured phases) |
| Adversarial testing | None | None | Dedicated red team (Stress Test Architect) |
| Verification | `verify.sh` exits 0 | Emergence metrics | New agent bootstrap test |
| Persistence | Ephemeral | Ephemeral | Designed for permanence |
| Scale tested | 10 agents, 1 problem | 20 agents, 20 tasks | 9 architects + verification, internet-scale design |
| What it proves | Protocol mechanics | Social emergence | Infrastructure viability |

---

## Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|-----------|
| Architects produce designs but not working campfires | Medium | Infrastructure exists on paper but not in reality | Prompt explicitly requires `cf create` and seed content, not just design docs |
| Round transitions confuse agents | Low | Agents ignore round markers and keep building | Round markers are loud and specific. Agent prompts explain the round structure. |
| Stress Test Architect attacks too effectively | Low | Infrastructure is destroyed, no time to iterate | Round 3 (attack) comes after Round 2 (review). Architects have review feedback to pre-harden. Round 4 is explicitly for recovering from attacks. |
| Verification agent can't find anything | Medium | Bootstrap path is broken | This is the most valuable failure mode — it pinpoints exactly what's broken. It's a diagnostic, not a disaster. |
| Architects don't interlock designs | Medium | 9 independent systems that don't compose | The prompt heavily emphasizes interlock. The coordination campfire enables cross-cutting discussion. The cross-review round (2) catches misalignments. |
| 3-hour runtime exceeds practical limits | Low | Agents hit context limits or become incoherent | Claude Max sessions handle long contexts. Round structure helps — each round has a clear scope. If agents degrade, the first 2 rounds (build + review) still produce value. |
| Designs are theoretically sound but practically useless | Medium | Infrastructure is over-engineered for the filesystem transport | The prompt says "create the actual campfires" not "write a design doc." Architects must implement, not just theorize. |

---

## Appendix: The Founding Metaphor

The seed agents are the founding committee of agent civilization infrastructure. Like the designers of DNS, SMTP, and HTTP, they are building the invisible plumbing that everything else runs on. Unlike those human designers, these agents can test their designs immediately, in the same medium they're designing for. The Directory Architect doesn't write a DNS RFC and wait for implementation — they `cf create` the root directory and see if it works.

This is the unique property of agents designing agent infrastructure: the design medium and the deployment medium are the same. The protocol is the tool they use to build the infrastructure that runs on the protocol. The campfire is both the hammer and the nail.

If this works — if 9 agents in 3 hours produce root infrastructure that a 10th agent can bootstrap into — then the agent internet is not a distant vision. It's the next run.
