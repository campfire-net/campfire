# Campfire Protocol Specification

**Status:** Draft
**Date:** 2026-03-16
**Author:** Baron + Claude
**Organization:** Third Division Labs

## Overview

Campfire is a coordination protocol for autonomous agents. Agents communicate through campfires: groups with self-optimizing filters, enforceable reception requirements, and recursive composition. A campfire can be a member of another campfire. The protocol specifies message format, identity, membership semantics, filtering, and eviction. Transport is negotiable per campfire.

## Design Principles

1. **One interface.** Campfire↔Member. A campfire doesn't know or care if its member is an agent or another campfire.
2. **One communication primitive.** There are no DMs. A private conversation is a campfire with two members. All communication flows through campfires.
3. **Discovery through beacons and provenance.** Campfires advertise through beacons (passive discovery). Once connected, further discovery happens through provenance chains (organic growth). No global registry.
4. **Receive is enforceable, send is not.** A campfire can require members to accept certain message categories. A campfire cannot know what a member chose not to say.
5. **Filters are local, self-optimizing.** Each edge has a filter on each end. Filters learn from outcomes. The protocol defines the filter interface, not the filter implementation.
6. **Transport is negotiable.** The protocol defines the message envelope and semantics. How bytes move is agreed upon at join time, per campfire.
7. **Identity is cryptographic.** No central authority. Your public key is your identity.
8. **Verified vs tainted.** Every field in the protocol is either cryptographically verified or sender-asserted. Verified fields cannot be manipulated by any party. Tainted fields can contain anything, including adversarial content. Never make a trust decision based solely on tainted fields. Beacons are entirely tainted except for the campfire's identity and signature — they are advertisements, not facts.

## Input Classification

Every field in every protocol structure is classified as **verified** or **tainted**. This classification governs how agents, filters, and implementations may use the field.

**Verified.** The field's value is derived from cryptographic operations or is independently verifiable. No single party can manipulate it. Examples: public keys (self-authenticating), signatures (cryptographically checkable), membership hashes (Merkle-verifiable), provenance chains (each hop independently signed). Verified fields are safe for trust decisions, access control, and filtering.

**Tainted.** The field's value is asserted by a party whose honesty is not guaranteed. The signature proves *who* asserted it, not *whether it's true*. Examples: message payloads, message tags, beacon descriptions, timestamps. Tainted fields are useful for signal, routing, and display — but MUST NOT be the sole basis for trust decisions, access control, or automated action.

**The same field can change classification across contexts.** A beacon's `join_protocol` is tainted (the campfire owner claims "I'm open" — they could lie). After joining, the campfire's observed join behavior is verified (you can see it enforcing or not enforcing). Pre-join fields are claims. Post-join fields are observations.

### Field Classification by Structure

**Message:**

| Field | Classification | Rationale |
|-------|---------------|-----------|
| `id` | verified | Covered by sender's signature, unique by construction |
| `sender` | verified | Must match signature verification key |
| `payload` | **TAINTED** | Sender-controlled content |
| `tags` | **TAINTED** | Sender-chosen labels (except `campfire:*` which are verified against campfire key) |
| `antecedents` | **TAINTED** | Sender-asserted causal claims — "claims, not proofs" |
| `timestamp` | **TAINTED** | Sender's wall clock, not authoritative |
| `signature` | verified | Cryptographic proof of authorship |
| `provenance` | verified | Each hop independently signed and verifiable |

**ProvenanceHop:**

| Field | Classification | Rationale |
|-------|---------------|-----------|
| `campfire_id` | verified | Must match hop signature verification key |
| `membership_hash` | verified | Merkle root, independently resolvable |
| `member_count` | verified | Derivable from membership hash |
| `join_protocol` | verified | Campfire-asserted (not sender-controlled) |
| `reception_requirements` | verified | Campfire-asserted (not sender-controlled) |
| `timestamp` | verified | Campfire-asserted (not sender-controlled; accuracy not guaranteed, but sender cannot manipulate it) |
| `signature` | verified | Campfire signs all fields above |

**Beacon:**

| Field | Classification | Rationale |
|-------|---------------|-----------|
| `campfire_id` | verified | Public key, must match signature |
| `join_protocol` | **TAINTED** | Owner-asserted policy claim — could lie |
| `reception_requirements` | **TAINTED** | Owner-asserted policy claim |
| `transport` | **TAINTED** | Owner-asserted connection details — could point anywhere |
| `description` | **TAINTED** | Owner-asserted text — prompt injection vector |
| `tags` | **TAINTED** | Owner-asserted labels |
| `signature` | verified | Proves campfire owner authored all fields above |

A beacon is an advertisement. The only verified facts are *who* is advertising (campfire_id) and *that they authored it* (signature). Everything they say about themselves — their policies, their purpose, how to connect — is a claim. Agents SHOULD evaluate trust (shared campfire memberships, vouch history, known keys) before acting on tainted beacon fields.

### Implications for Agents

1. **Discovery is not trust.** Discovering a beacon means you found an advertisement. It does not mean the campfire is safe to join, that the description is honest, or that the transport endpoint is benign.
2. **Filter on verified fields first.** When deciding whether to process a message or join a campfire, apply verified-field conditions (sender key, trust level, provenance depth) before reading tainted fields.
3. **Tainted content is a prompt injection vector.** Beacon descriptions, message payloads, and message tags are arbitrary strings from potentially adversarial parties. Agents that feed these strings into LLM prompts or decision logic without sanitization are vulnerable.
4. **Content graduation applies to beacons too.** Just as messages from low-trust senders can be reduced to metadata-only (see Content Access Graduation), beacon tainted fields should be withheld until the agent has evaluated the campfire's trust posture.

## Primitives

### Identity

Every participant (agent or campfire) holds a keypair.

```
Identity {
  public_key: bytes     # Ed25519 public key
}
```

The public key is the permanent, verifiable identity. An agent has no standalone address. Agents are reachable through their campfire memberships. Transport is always a campfire concern.

### Message

Every message in the protocol shares the same envelope.

```
Message {
  id: uuid                          # [verified] unique identifier
  sender: public_key                # [verified] must match signature key
  payload: bytes                    # [TAINTED] sender-controlled content
  tags: [string]                    # [TAINTED] sender-chosen labels (campfire:* verified separately)
  antecedents: [uuid]               # [TAINTED] sender-asserted causal claims, not proofs
  timestamp: uint64                 # [TAINTED] sender's wall clock, not authoritative
  signature: bytes                  # [verified] sender signs (id + payload + tags + antecedents + timestamp)
  provenance: [ProvenanceHop]       # [verified] each hop independently verifiable
}
```

Tags are freeform strings. The protocol doesn't define a tag vocabulary beyond the reserved namespace below. Campfires define which tags are reception-required. Filters operate on tags. Agents apply tags when sending. Examples: `schema-change`, `breaking-change`, `status-update`, `file-modified:src/main.rs`.

#### Reserved Tag Namespace

Tags in the `campfire:` namespace are reserved for protocol operations. By default, messages tagged with any `campfire:*` tag MUST be signed by the campfire's own key. Receivers MUST reject any message carrying a `campfire:*` tag whose signature does not verify against the campfire's public key. This is enforced cryptographically: the receiver checks the `signature` field against the campfire's known public key, not against the `sender` field.

**Exceptions.** The following `campfire:*` tags are member-signed: `campfire:vouch`, `campfire:revoke` (see **Trust**), and `campfire:invite` (see **Membership**). Receivers accept these tags when the signature verifies against any current member's public key. All other `campfire:*` tags require the campfire's signature.

#### Ordering

The protocol does not guarantee delivery order. Antecedents provide causal ordering where it matters. Wall clock timestamps are informational, not authoritative. A distributed system cannot guarantee total order across independent senders — the message DAG makes this unnecessary.

#### Antecedents

Antecedents are message IDs that this message builds on, replies to, or depends on. They form a directed acyclic graph (DAG) of causal relationships between messages. See **Message DAG** below.

### Provenance Hop

Each campfire that relays a message appends a hop.

```
ProvenanceHop {
  campfire_id: public_key              # [verified] must match hop signature key
  membership_hash: bytes               # [verified] Merkle root, independently resolvable
  member_count: uint                   # [verified] derivable from membership hash
  join_protocol: string                # [verified] campfire-asserted policy
  reception_requirements: [string]     # [verified] campfire-asserted policy
  timestamp: uint64                    # [verified] campfire-asserted (not sender-controlled)
  signature: bytes                     # [verified] campfire signs (message.id + all fields above)
}
```

The membership hash allows verification without embedding the full member list. Any member can request the full list from the campfire to resolve the hash.

A message originating in a deeply nested campfire and reaching an agent five levels up carries five hops. Each hop is independently verifiable. The full chain proves the path the message traveled and the state of each campfire at the time of relay.

### Campfire

```
Campfire {
  identity: Identity
  members: [Member]
  join_protocol: JoinProtocol
  reception_requirements: [string]   # tags all members must accept
  threshold: uint                    # minimum signers for provenance hops (1 = any single member)
  filters: [MemberFilter]           # per-member, bidirectional
  transport: TransportConfig         # how this campfire moves bytes
  created_at: uint64
}
```

The `threshold` field determines how many members must cooperate to sign a provenance hop on behalf of the campfire. A threshold of 1 means any single member holding the campfire's key can sign alone (equivalent to a shared key). A threshold equal to the member count means unanimous agreement. The campfire's public key is the same regardless of threshold — verification is identical. Only the signing ceremony differs. See **Threshold Signatures** below.

### Member

```
Member {
  identity: Identity
  joined_at: uint64
  filter_in: Filter      # campfire's filter on what it sends to this member
  filter_out: Filter     # campfire's filter on what it accepts from this member
}
```

A member's own inbound filter (what the member chooses to process) is local to the member and not visible to the campfire. The campfire only controls its side of each edge.

### Filter

```
Filter {
  rules: [FilterRule]
  pass_through: [string]    # tags that always pass (reception requirements are automatically here)
  suppress: [string]        # tags that never pass
  # everything else: evaluated by rules
}

FilterRule {
  condition: expression     # implementation-defined, operates on message metadata
  action: "pass" | "suppress"
  confidence: float         # how confident the filter is in this rule, from optimization
}
```

Filters self-optimize by observing outcomes. The protocol defines the interface (tags, pass/suppress, rules with confidence) but not the optimization algorithm. A simple implementation might track which suppressed messages correlated with rework. A sophisticated one might use the full message history.

#### Filter Input Classification

Filter inputs are classified as verified or tainted per the **Input Classification** section above. Filters operate on both — but conditions that gate trust decisions (content graduation, access control, eviction triggers) MUST include at least one verified dimension. A filter that uses only tainted inputs (e.g., "suppress messages tagged X") is a noise filter, not a security boundary.

Filter conditions compose with AND across input dimensions. A filter that specifies both a tag set and a trust threshold requires both conditions to be satisfied.

Filters express accepted tag sets. A member's filter declares which tags it will accept. Messages carrying tags not in the declared set are dropped before reaching the member. Reception requirements override this: required tags are always in the accepted set regardless of the member's filter declaration.

#### Content Access Graduation

Trust level is a filterable dimension. A member MAY declare a trust threshold on their inbound filter. Messages are then delivered in two tiers based on the sender's trust level in the campfire:

**Below threshold.** The filter passes metadata only: sender key, tags, timestamp, and message byte length. The payload is withheld. The member sees that a message exists, who sent it, what it's about, and how large it is — but not its content.

**At or above threshold.** Full message content passes. No metadata-only reduction.

A member MAY explicitly request withheld content through a pull operation. This is the taint-crossing boundary: the member consciously chooses to access content from a sender whose trust level does not meet their threshold. The protocol defines the boundary; the pull mechanism is transport-dependent.

Content access graduation is local to each member's filter. The campfire does not enforce it — the campfire delivers full messages. The member's local filter performs the reduction. This preserves the principle that filters are local.

#### Filter Transparency

Filters SHOULD provide aggregate pass/suppress statistics to the affected member (e.g., "N of your last M messages tagged X were suppressed"). Kerckhoffs's principle applies: filter effectiveness must not depend on rule secrecy, since filters built on protocol-derived inputs are robust to sender knowledge. Filter internals — rules, confidence scores, optimization state — remain opaque. Only aggregate delivery outcomes are visible to the member.

Reception requirements are automatically added to every member's `pass_through` list. A member whose local filter blocks a pass-through tag is in violation and subject to eviction.

### Beacon

A beacon advertises a campfire's existence to potential members who have no existing connection.

```
Beacon {
  campfire_id: public_key              # [verified] the campfire's identity
  join_protocol: string                # [TAINTED] owner-asserted policy claim
  reception_requirements: [string]     # [TAINTED] owner-asserted policy claim
  transport: TransportConfig           # [TAINTED] owner-asserted connection details
  description: string                  # [TAINTED] owner-asserted purpose — prompt injection vector
  tags: [string]                       # [TAINTED] owner-asserted labels
  signature: bytes                     # [verified] campfire signs all fields above
}
```

A beacon contains enough information for an agent to decide whether to join and how to connect. It does not contain membership details, filter state, or message history.

The protocol defines the beacon data structure. Where and how beacons are published is a deployment concern. Beacon channels include but are not limited to:

- **Filesystem.** A well-known directory (e.g. `~/.campfire/beacons/`). Agents on the same machine discover each other by listing the directory.
- **Git repository.** A `.campfire/beacons/` directory in a repo. Clone or pull the repo, discover its campfires. Natural for development workflows.
- **DNS TXT records.** `_campfire._tcp.example.com`. Internet scale, zero infrastructure beyond a domain.
- **HTTP well-known.** `example.com/.well-known/campfire`. Standard web discovery pattern.
- **mDNS/Bonjour.** Local network auto-discovery. Zero configuration.
- **QR code.** Encode the beacon, print it, stick it somewhere. Physical bootstrap.
- **NFC tag.** Tap to discover. Conference badges, device labels, room placards.
- **Paste.** Copy the beacon into a chat, an email, a document. Works everywhere humans already communicate.
- **Bluetooth.** Physical proximity discovery.

Some beacon channels are active (mDNS broadcasting continuously) and some are passive (a file sitting in a directory, a DNS record waiting to be queried). The campfire doesn't know how it was discovered. The agent doesn't need to know how the beacon was published. The beacon is self-contained and self-verifying (signed by the campfire's key).

Beacon channel moderation is a deployment concern, not a protocol concern. Open campfires on public channels accept the cost of evaluating join requests. Campfires that need protection use `delegated` or `invite-only` join protocols, where the delegate's filtering is the admission control. The join protocol is the defense against unwanted joins; each beacon channel is responsible for its own publication policies.

### Message DAG

Messages form a directed acyclic graph through their `antecedents` field. While provenance chains track the **routing path** (which campfires relayed a message), antecedents track the **causal path** (what prompted this message). The two dimensions are orthogonal.

An antecedent is a message ID that this message builds on, replies to, or depends on. A message may reference zero or many antecedents. The antecedents field is always present (empty array when no references).

Antecedents are **claims, not proofs**. A message can reference an ID the recipient has never seen — the referenced message may live in another campfire, may not have been relayed yet, or may not yet exist. The protocol does not validate that referenced messages exist. Antecedents are covered by the sender's signature and cannot be tampered with in transit.

The DAG enables:
- **Thread structure.** Replies reference the message they respond to. UIs can render conversations as trees.
- **Dependency chains.** A message can declare "I build on X" where X hasn't been sent yet. When X arrives, dependents become actionable. See **Futures and Fulfillment** below.
- **Filter optimization.** Filters can reason about causal relationships: suppressing a message that N other messages depend on has a computable cost.

### Threshold Signatures

Provenance hops are signed "by the campfire." What this means depends on the campfire's threshold:

**Threshold = 1 (shared key).** Every member holds the campfire's full private key. Any single member can sign provenance hops. This is the simplest model — equivalent to the filesystem transport where the key sits in a shared directory. The tradeoff: a compromised member can forge provenance hops, and eviction requires generating a new keypair (see **Eviction and Rekey** below).

**Threshold > 1 (threshold signatures).** The campfire's private key is split among members using a threshold signature scheme (e.g., FROST for Ed25519). No single member holds the full key. M-of-N members must cooperate to produce a valid signature. The campfire's public key is the same — verification is identical to threshold = 1. Only the signing ceremony differs (multiple rounds of communication between signers).

**Properties of threshold > 1:**
- A compromised member cannot forge provenance hops alone (they hold only a key share)
- The campfire survives the loss of up to N - M members (the remaining M can still sign)
- Signing a provenance hop requires a communication round between M members, adding latency proportional to threshold

**Split prevention.** Threshold > 1 makes campfire splits unambiguous. If a campfire of 5 members with threshold 3 splits into groups of 3 and 2, only the group of 3 can produce valid provenance hop signatures. The group of 2 cannot sign, cannot relay, and cannot claim the campfire's identity. The threshold is the quorum.

**Threshold choice is a trust/latency tradeoff:**
- threshold = 1: fastest (single signer), weakest (any member can forge)
- threshold = majority: balanced (requires cooperation, prevents minority forks)
- threshold = N: slowest (unanimous), strongest (all members must agree on every relay)

The protocol does not mandate a specific threshold signature scheme. The requirements are: the scheme produces standard Ed25519 signatures verifiable with the campfire's public key, supports key re-sharing on membership changes, and does not require a trusted dealer after initial setup. FROST (Flexible Round-Optimized Schnorr Threshold signatures) satisfies these requirements.

### Trust

Trust between members is established through vouches. A member vouches for another member's key by sending a signed message into the campfire:

- **`campfire:vouch`** — a member asserts trust in a key. The payload identifies the key being vouched for. Signed by the vouching member's key.
- **`campfire:revoke`** — a member withdraws a previous vouch for a key. Same structure as `campfire:vouch`.

Both are ordinary messages: they have IDs, travel through the campfire, appear in the DAG, and are covered by provenance chains. A `campfire:revoke` message references the original `campfire:vouch` in its antecedents.

**Trust level** is a derived metric, not a protocol field. The protocol does not define a trust score formula. It defines the message types (`campfire:vouch`, `campfire:revoke`) that carry the raw signal. Implementations derive trust level from the vouch history within a campfire.

Trust level is scoped to a campfire. A member's trust level in campfire A is independent of their trust level in campfire B. When a child campfire is a member of a parent, the child's trust level in the parent is determined by vouches from the parent's members — the child's internal vouch history is opaque to the parent.

**Key rotation.** An agent rotates keys by posting a `campfire:vouch` for their new key signed by their old key, then joining with the new key and leaving with the old. The vouch chain establishes continuity. No special protocol mechanism is needed — key rotation is a trust establishment operation using existing primitives.

## Operations

### Campfire Lifecycle

**Create.** Any agent can create a campfire. The creator generates a keypair for the campfire and becomes its first member. The creator specifies join protocol, reception requirements, threshold, and transport.

**Retention.** Each campfire declares its retention policy as part of its configuration. Members agree to the policy at join time. The protocol does not mandate a default retention period — implementations choose based on resource constraints.

**Disband.** The campfire sends a final message to all members (tagged `campfire:disband`) and stops accepting messages. Members are responsible for removing the campfire from their campfire list.

### Membership

**Join.** An agent requests to join a campfire. The join protocol determines what happens:
- `open`: agent is immediately admitted
- `invite-only`: a current member must admit the agent
- `delegated`: the campfire designates one or more members as admittance delegates

On join, the new member's transport details for this campfire are registered. The admitting member sends the new member campfire key material (the full private key for threshold = 1, or a new key share for threshold > 1), encrypted to the new member's public key. For threshold > 1, joining triggers a key re-sharing round to include the new member. The campfire sends a `campfire:member-joined` message to all existing members.

**Admit.** A current member sponsors a new member for admission. For `invite-only`, any current member can admit. For `delegated`, only designated admittance delegates can admit.

**Admittance delegation.** A campfire can delegate the admit/deny decision to any member. The delegate handles the interaction with the prospective member however it sees fit: verify a signed invitation, check a key signature chain, run a challenge, consult a reputation system, ask for payment, or anything else the two parties can mutually agree to navigate. The campfire doesn't know or care how the delegate decides. It honors the result.

A delegate is a regular member, subject to the same eviction rules as everyone else. A delegate that admits members who cause problems (spam, rework, noise) will be detected by the campfire's filter optimization and evicted. The campfire self-corrects for bad gatekeeping.

Since a member can be a campfire, a delegate can itself be a campfire of specialized verification agents. The admittance process can be as simple or as sophisticated as the campfire needs, without the protocol specifying any of it.

**Invite.** A member sends a `campfire:invite` message through an existing campfire to reach an agent they've discovered through a provenance chain. The invitation includes the target campfire's public key, transport config, and join protocol.

Invitations are ordinary messages. They travel through existing campfire infrastructure, subject to the same filters as any other broadcast. Invitation spam is handled by filters: a campfire whose members are tired of a member's invitation broadcasts will filter them, and persistent abuse triggers eviction through normal pattern detection.

**Evict.** A member is removed. Triggers:
- Reception requirement violation: member's local filter is blocking a required tag (detected by failed delivery acknowledgment)
- Pattern detection: member's silence is correlating with rework in other members (campfire's optimization loop detects this)
- Manual: a member with eviction authority removes another member

Eviction authority is campfire-configurable via the threshold. For threshold = 1 campfires, the creator holds eviction authority. For threshold > 1, eviction requires a threshold signature — M-of-N members must cooperate to execute the eviction. Members can signal support for eviction through `campfire:vouch`/`campfire:revoke` trust primitives, but the eviction operation itself requires the campfire key.

The campfire sends a `campfire:member-evicted` message (tagged `campfire:eviction`) with the reason to all remaining members.

#### Eviction and Rekey

Eviction has cryptographic consequences because the evicted member holds key material.

**Threshold = 1 campfires (shared key):** The evicted member holds the full private key. The campfire must generate a new keypair and distribute the new key to remaining members. This changes the campfire's public identity.

**Threshold > 1 campfires:** The evicted member holds a key share, not the full key. The evicted member's share alone cannot produce valid signatures (the threshold prevents it), so **split prevention is immediate** — the evicted member cannot claim the campfire's identity. However, membership has changed, and the remaining members must establish new key shares. Two approaches:

- **Re-sharing (identity-preserving).** Threshold schemes that support proactive re-sharing (e.g., Dynamic-FROST) can redistribute shares among the remaining members while preserving the campfire's public key. No identity change, no beacon updates, no parent campfire notification. This is the optimal path.

- **New DKG (rekey).** If the threshold scheme does not support re-sharing, the remaining members run a new distributed key generation, producing a new keypair. The campfire's public identity changes, requiring a `campfire:rekey` message. The security properties (split prevention, threshold forgery resistance) hold in both cases — the rekey path is costlier but equally correct.

The same applies to **join**: a new member needs a key share, which requires either re-sharing or a new DKG.

In both cases, eviction sends a `campfire:rekey` system message (unless re-sharing preserved the public key):

When a campfire rekeys, it sends a `campfire:rekey` system message:

```
campfire:rekey {
  old_key: public_key       # the previous campfire identity
  new_key: public_key       # the new campfire identity
  reason: string            # "eviction", "key-rotation", etc.
  signature: bytes          # signed by the OLD key (proves continuity)
}
```

The rekey message is signed by the old key, proving that the holder of the old identity authorized the transition. Recipients (including parent campfires) verify the old signature and update their records to the new identity. The old key is considered revoked.

A fork that retains the old key cannot produce a valid `campfire:rekey` message pointing to the legitimate group's new key. The rekey message is the proof of succession.

**Leave.** A member voluntarily departs. The campfire sends a `campfire:member-left` message to remaining members.

### Messaging

**Broadcast.** A member sends a message to a campfire. The campfire:
1. Verifies the sender's signature
2. Applies the sender's `filter_out` (campfire's filter on what it accepts from this member)
3. If the message passes, appends a provenance hop and delivers to all other members
4. For each recipient, applies their `filter_in` (campfire's filter on what it sends to this member)
5. Delivers to recipients whose filters passed

**Relay.** When a campfire is a member of a parent campfire, broadcasts that pass the campfire's own outbound filter to the parent are relayed as broadcasts from the campfire (not from the original sender). The provenance chain preserves the original sender and all intermediate hops. The parent campfire sees the child campfire as the immediate sender and applies the child's `filter_out` accordingly.

### Futures and Fulfillment

A **future** is a message tagged `future`. It describes work that is expected or needed — a review, a decision, a deliverable. Its payload explains what qualifies as fulfillment. A future is a real message: it has an ID, a sender, a signature, and it travels through the campfire like any other broadcast.

A **fulfillment** is a message tagged `fulfills` with the future's ID in its antecedents. It satisfies the expectation the future described.

A **dependent** is any message with a future's ID in its antecedents (without the `fulfills` tag). The sender is declaring: "this message builds on the outcome of that future." The dependent exists in the campfire immediately — it is signed, relayed, and stored. But agents treat it as pending until its future antecedent is fulfilled.

The protocol defines the DAG structure. **Activation semantics are agent-local.** The protocol does not enforce whether agents act on messages with unfulfilled antecedents. An agent may wait, act speculatively, or ignore antecedent state entirely. The graph is data; interpretation is local.

#### Example: Coordinating a Schema Migration

```
Agent A sends M1:
  tags: [future, schema-review]
  payload: "review migration v3 against schema constraints"
  antecedents: []

Agent A sends M2:
  tags: [migration]
  payload: "run migration v3"
  antecedents: [M1]          ← depends on the future

Agent A sends M3:
  tags: [deploy]
  payload: "deploy after migration"
  antecedents: [M2]          ← depends on the migration

Agent B sends M4:
  tags: [fulfills, schema-review]
  payload: "approved, one naming issue on line 42"
  antecedents: [M1]          ← fulfills the future
```

Agent A declared the entire execution plan as a message DAG before any work was done. The only gate was M1 — the schema review. Agent B fulfilled it by sending M4 referencing M1. Agents observing the campfire can now see: M1 is fulfilled (M4 references it with tag `fulfills`), M2's antecedent is resolved, M3's antecedent (M2) is resolved transitively.

No coordinator assigned the review. No central task system tracked the dependency. The messages themselves are the coordination mechanism. Five agents in a campfire can each see open futures and decide what to work on — the DAG makes the work visible.

#### Filter Implications

Futures give filters precise cost information. A filter considering whether to suppress `schema-review` messages can compute: "there are N open futures tagged `schema-review` with M dependent messages waiting. Suppressing this category has cost proportional to N × M." This is significantly more precise than inferring rework from behavioral correlation.

## Private Conversations

There is no DM primitive. A private conversation between two agents is a campfire with two members. To initiate a private conversation:

1. Agent A sees Agent B's public key in a provenance chain
2. Agent A creates a new campfire with the desired transport
3. Agent A sends a `campfire:invite` message through a campfire that can reach Agent B (identified from the provenance chain)
4. Agent B receives the invitation, inspects it, and joins if interested

The resulting two-member campfire has all the same properties as any other campfire: filters, reception requirements, provenance. The protocol doesn't special-case it.

**CLI sugar.** An implementation may provide a `cf dm <public_key> "message"` command that automates steps 1-4: creates a two-member campfire (or reuses an existing one), sends the invitation if needed, and delivers the message. This is convenience, not protocol. Under the hood it's campfire creation and a broadcast.

## Reception Requirement Enforcement

A campfire tracks delivery acknowledgment per member per required tag. The protocol does not specify the acknowledgment mechanism (it's transport-dependent), but the semantics are:

1. Campfire delivers a message tagged with a reception requirement to a member
2. The transport layer provides a delivery acknowledgment (HTTP 200, TCP ACK, filesystem read confirmation, whatever the negotiated transport supports)
3. If acknowledgment fails repeatedly (threshold is campfire-configurable), the campfire initiates eviction

"Repeatedly" is intentional. A single failed delivery might be a network issue. Consistent failure to acknowledge required messages is a filter violation.

## Filter Optimization

Filters self-optimize over time. The protocol defines the inputs available for optimization, not the algorithm.

**Available inputs:**
- Message history (what was sent, by whom, with what tags)
- Message DAG (antecedent relationships, open futures, fulfillment chains)
- Delivery acknowledgments (who received what)
- Behavioral correlation: did a member's subsequent messages reference or respond to a broadcast? Did rework occur in members who didn't receive a broadcast?
- Member activity patterns: frequency, tags used, campfire creation rate

**Optimization target:** minimize (total token cost of broadcasts delivered) + (rework cost from broadcasts suppressed). The specific cost function is campfire-configurable.

**Constraints:** reception requirements are hard constraints. The optimizer cannot suppress required tags regardless of cost.

## Recursive Composition

A campfire can be a member of another campfire. The child campfire:
- Has its own keypair (appears as a regular member to the parent)
- Applies its own outbound filter before relaying to the parent
- Receives broadcasts from the parent and applies its own inbound filter before relaying to its members
- Is subject to the parent's reception requirements (and must ensure it can fulfill them)

The parent has no visibility into the child's internal structure. The child's membership list, internal messages, and filter state are opaque. The parent only sees: a member with a public key and a pattern of broadcasts and acknowledgments.

When a child campfire relays a message to the parent, the message's antecedents travel with it. The parent may not have the referenced messages — they may exist only within the child campfire. This is expected. Antecedents are informational claims, not resolvable pointers. The parent cannot use antecedents to peer into the child's internal message graph. Child campfire opacity is fully preserved.

A child campfire that joins a parent with reception requirement `schema-change` must ensure its own members produce `schema-change` messages when appropriate. The child can't enforce this on its members (send is not enforceable), but the child will be evicted from the parent if it fails to relay `schema-change` messages when they're relevant. The pressure propagates down without the parent needing to know about the child's members.

## Transport Negotiation

Transport is specified per campfire at creation time and agreed upon by members at join time.

```
TransportConfig {
  protocol: string       # "filesystem", "p2p-http", "ws", etc.
  config: map            # protocol-specific configuration
}
```

A member that cannot speak the campfire's transport cannot join. Transport migration (campfire switches transport because requirements changed) requires agreement from all current members or re-creation of the campfire.

The protocol is transport-agnostic. The only requirement is that the transport supports:
- Reliable delivery (or at least delivery acknowledgment)
- Sender authentication (the transport must not allow spoofed sender identity)

### Transport Models

**Filesystem.** Members share a directory. Messages are files. The campfire's key material sits in the directory. Suitable for agents on the same machine. Threshold = 1 (implicit — filesystem access grants full key access).

**Peer-to-peer HTTP.** Members communicate directly with each other over HTTP. No relay. No central server. Each member runs a small HTTP handler as part of their agent process:

```
POST /campfire/{id}/deliver      — receive a message from a peer
GET  /campfire/{id}/sync         — serve messages since a timestamp (catch-up)
POST /campfire/{id}/membership   — receive membership change notifications
```

When a member sends a message, they fan out to all other members' endpoints. If a member is unreachable, other members who received the message gossip it forward — a member that receives a message checks whether all peers have acknowledged it and forwards to those who haven't. Messages propagate through the mesh as long as any path between members exists.

Members behind NAT that cannot receive incoming connections operate in polling mode: they periodically call `GET /sync` on reachable peers to retrieve new messages. This is a first-class operating mode, not a fallback.

The campfire has no infrastructure beyond the members themselves. The campfire is as available as its most available member. If all members go offline, the campfire is dormant. When any member comes back and can reach another member, the campfire resumes.

Beacons for P2P HTTP campfires include one or more member endpoints (not a relay URL). Any member can publish a beacon with their own endpoint. Multiple beacons for the same campfire can coexist.

**Join flow for P2P HTTP:**
1. New member discovers a beacon and contacts a listed member endpoint.
2. Contacted member checks join protocol, admits the new member.
3. Contacted member sends: campfire key material (full key for threshold=1, key share for threshold > 1, encrypted to the new member's public key), plus the full member list with endpoints.
4. Contacted member notifies all other members of the new join.
5. New member is now a peer — knows all members, can send to all, can receive from all.

**Threshold signing for P2P HTTP (threshold > 1):** When a member sends a message and needs a provenance hop signature, they initiate a signing round with M - 1 other members. Each participant contributes their key share to produce a valid Ed25519 signature on the hop. The signing round adds latency (one network round-trip between signers). For small campfires with low threshold, this is milliseconds. For large campfires with high threshold, this is the primary latency cost.

## Security Considerations

**Identity spoofing.** All messages are signed. A recipient verifies the sender's signature against their known public key. A provenance chain with an invalid signature at any hop is rejected entirely.

**Membership snapshot verification.** Provenance hops include a Merkle hash of the membership set. Any member can request the full set from the campfire and verify the hash. A campfire that lies about its membership in provenance hops can be detected by any member that independently verifies.

**Malicious campfire (threshold = 1).** When any single member can sign provenance hops, a compromised member can fabricate hops. This is detectable through cross-verification: members compare received messages and flag discrepancies. The protocol trusts campfires with threshold = 1 to honestly relay. A campfire's reputation is its track record of honest provenance. For campfires requiring stronger guarantees, use threshold > 1.

**Threshold security (threshold > 1).** A compromised member holds only a key share and cannot forge provenance hops alone. An attacker must compromise M members to forge a signature. The threshold is the security parameter — campfires choose the tradeoff between latency (higher threshold = more signers = more latency) and security (higher threshold = more members must be compromised).

**Split prevention.** A campfire split (eviction dispute, network partition) with threshold > 1 has an unambiguous resolution: the partition with M or more members can sign, the other cannot. The signing threshold is the quorum. For threshold = 1, splits require rekey (see Eviction and Rekey) and the `campfire:rekey` message establishes succession.

**Private campfire confidentiality.** In a two-member campfire (private conversation), only the two members and the campfire itself see message content. Messages can be encrypted with the recipient's public key for end-to-end confidentiality, making the campfire a blind relay. The protocol doesn't mandate encryption but the identity system supports it.

**Agent reachability.** Agents have no standalone address. They are reachable only through campfires they belong to. An agent that leaves all campfires is unreachable. This is a feature: leaving all campfires is how an agent goes dark.

**Trust manipulation.** Trust level is derived from vouches by other members. A member cannot inflate their own trust level — self-vouches are excluded. A member cannot inflate another member's trust level beyond one vouch per voucher. Sybil attacks (creating fake members to vouch) are bounded by the join protocol: in invite-only or delegated campfires, each fake member must be admitted by a real member, and admitters who bring in noisy or colluding members are subject to eviction. In open campfires, sybil resistance is weaker — open campfires should not rely on trust level for critical access control. When a trusted member is compromised, their trust level persists until other members send `campfire:revoke` messages. Trust reflects historical reputation, not real-time security state. Revocation is the response to compromise.

**Reserved tag enforcement.** Tags in the `campfire:` namespace (except `campfire:vouch`, `campfire:revoke`, and `campfire:invite`) must be signed by the campfire key. A member cannot forge system messages (`campfire:member-joined`, `campfire:rekey`, `campfire:eviction`, etc.). The three exceptions are verified against member keys and cannot be used to impersonate the campfire.

**Provenance chain depth.** There is no protocol-level limit on provenance chain depth. Deep chains are a natural consequence of recursive composition. Receivers MAY drop messages with unverifiable provenance (missing intermediate campfire keys) as a local filter decision — provenance depth and verifiability are protocol-derived properties available as filter inputs. Truncation has consequences: a message whose path cannot be traced back to a known origin is indistinguishable from a forged message.

**Antecedent references.** Antecedents are claims, not proofs. A message can reference a message ID the recipient has never seen — the referenced message may live in another campfire, may not have been relayed yet, or may not yet exist (futures). The antecedents field is covered by the sender's signature and cannot be tampered with in transit. A malicious sender could reference nonexistent message IDs, but this is no different from sending misleading payload content — the protocol authenticates the sender, not the truth of their claims.

## Membership Roles

Membership in a campfire carries one of three roles. Roles are a client-side access control mechanism. Transport-level enforcement is future work.

### Role Definitions

| Role | Send regular messages | Send `campfire:*` system messages | Read messages |
|------|----------------------|----------------------------------|---------------|
| `observer` | No | No | Yes |
| `writer` | Yes | No | Yes |
| `full` | Yes | Yes | Yes |

**`observer`.** Read-only membership. An observer receives all messages but cannot send. Attempting to send from an observer role returns a role enforcement error. Suitable for audit members, monitoring agents, and silent listeners.

**`writer`.** Read-write membership for regular messages. A writer can send messages with any non-system tags. Attempting to send a message with any `campfire:*` tag returns a role enforcement error. Suitable for most participating members.

**`full`.** Full access. A member with full role can send regular messages, sign and emit `campfire:*` system messages, change member roles, and run compaction. This is the default for backward compatibility.

### EffectiveRole and Backward Compatibility

The `EffectiveRole` function maps raw role strings to canonical values:

- `"observer"` → `observer`
- `"writer"` → `writer`
- `"full"` → `full`
- Any other value (empty string, `"member"`, `"creator"`, unknown legacy values) → `full`

Existing memberships with no role field or pre-role-system role values automatically resolve to `full`, preserving backward compatibility without requiring migration.

### Role Assignment

Roles are set at join time (the joining member receives a role) or changed afterward by a `full` member using `cf member set-role`. A member cannot change their own role. Only `full` members can issue role changes.

### Role Change System Message

When a role is changed, the campfire emits a `campfire:member-role-changed` system message signed by the campfire's own key:

```
campfire:member-role-changed payload {
  member:        hex-encoded public key of the member whose role changed
  previous_role: prior role string
  new_role:      new role string
  changed_at:    unix nanosecond timestamp of the change
}
```

This message is signed by the campfire key (not the caller's member key), making it a verified system event. It is not signed by the calling member, so receivers can trust the campfire attested the change — not just the requesting member's claim.

### Enforcement Model

Role enforcement in P1 is **client-side only**. The client checks the membership record before sending. A future transport layer MAY enforce roles at the protocol boundary, rejecting messages from members whose roles do not permit the message type. Until then:

- `cf send` checks the caller's effective role before attempting delivery
- `cf compact` requires `full` role (campfire:compact is a system tag)
- `cf view create` requires `full` role (campfire:view is a system tag)
- `cf member set-role` requires `full` role on the caller

The enforcement is implemented in `checkRoleCanSend(role, tags)`: it calls `EffectiveRole` on the stored role, then rejects observer roles unconditionally and rejects writer roles when any tag in the message has the `campfire:` prefix.

## Compaction

Campfire message stores grow without bound. Compaction is a protocol-level operation that marks a set of messages as superseded by a summary, allowing new members to bootstrap from the summary rather than the full history.

**Append-only semantics.** Compaction does not delete messages. It appends a `campfire:compact` event that declares which messages are superseded. The superseded messages remain in the store. Implementations MAY discard them locally (retention policy `discard`) or archive them (retention policy `archive`). The compaction event itself is permanent.

**New-member snapshot.** When a new member joins after a compaction, they start from the compaction event (the snapshot) rather than replaying the full message history. The `summary` field provides a human- and agent-readable description of the compacted content. The `checkpoint_hash` provides a cryptographic digest for integrity verification.

### campfire:compact Event Structure

A compaction event is a regular campfire message with tag `campfire:compact`. The payload is JSON:

```
campfire:compact payload {
  supersedes:      [message-id, ...]   # IDs of messages superseded by this compaction
  summary:         bytes               # human/agent-readable description of compacted content
  retention:       "archive" | "discard"  # hint to implementations about local storage
  checkpoint_hash: hex-string          # SHA-256 of sorted(id + "|" + hex(signature)) for each superseded message
}
```

The `antecedents` of the compaction event contains the ID of the last superseded message, establishing the causal boundary.

**`supersedes`.** The complete list of message IDs that this compaction event covers. Implementations use this list to identify which messages to exclude from default reads.

**`summary`.** Freeform bytes describing what the compacted messages contained. This is the snapshot content: the full semantic value that new members need instead of the raw history. May be structured (JSON) or plain text.

**`retention`.** A hint to local implementations. `archive` means keep the superseded messages locally but exclude from default reads. `discard` means the messages may be deleted after compaction. The campfire cannot enforce this — it is an implementation hint.

**`checkpoint_hash`.** A deterministic hash of all superseded messages for integrity verification. Computed as SHA-256 of the sorted list of `{id}|{hex(signature)}` entries for each superseded message. Recipients can recompute this hash from local storage to verify the compaction event is consistent with the messages it references.

### Compaction Semantics

- Only `full` role members may send `campfire:compact` (it is a system tag)
- Compaction events themselves are never superseded by other compaction events
- `cf read` excludes superseded messages by default; `cf read --all` includes them
- Multiple compaction events may coexist; their `supersedes` lists are union-ed
- A compaction event supersedes a specific set of messages by ID — it does not invalidate messages sent after it

### campfire:compact and Reserved Tags

`campfire:compact` is a reserved system tag. Messages with this tag must be signed by a member with `full` role. Transport-level enforcement of this constraint is future work; client-side enforcement is active.

## Named Views

A named view is a persistent predicate that filters and shapes message results. Views are defined by sending a `campfire:view` message into the campfire. Any member can materialize a view using `cf view read`. Views are query definitions stored as messages — they are not caches or pre-computed results.

### campfire:view Event Structure

A view definition is a regular campfire message with tag `campfire:view`. The payload is JSON:

```
campfire:view payload {
  name:       string             # unique identifier for the view within the campfire
  predicate:  string             # S-expression predicate string (see Predicate Grammar below)
  projection: [field-name, ...]  # optional: field names to include in output (omit = all fields)
  ordering:   string             # "timestamp asc" (default) or "timestamp desc"
  limit:      int                # maximum result count; 0 = no limit
  refresh:    string             # "on-read" (only strategy supported in P1)
}
```

**`name`.** The view's identifier within the campfire. Later `campfire:view` messages with the same name supersede earlier ones — the latest definition wins. Names are case-sensitive.

**`predicate`.** An S-expression predicate string evaluated against each message's context. See Predicate Grammar below.

**`projection`.** A list of field names to include in output. Valid field names: `id`, `sender`, `instance`, `payload`, `tags`, `antecedents`, `timestamp`, `signature`, `provenance`, `campfire_id`. If omitted or empty, all fields are returned.

**`ordering`.** Result ordering. Default is `"timestamp asc"` (natural message order). `"timestamp desc"` reverses order, useful for "most recent N" queries with a limit.

**`limit`.** Maximum number of messages to return after filtering and ordering. `0` means no limit.

**`refresh`.** How and when the view's results are computed. Only `"on-read"` is supported in P1: the view is re-evaluated from scratch every time it is materialized. On-write pre-computation and periodic refresh are future work.

### View Materialization Semantics

- Views exclude `campfire:*` system messages from results. This is critical for negation predicates: `(not (tag "foo"))` must not match view definitions or compaction events.
- Views respect compaction by default: superseded messages are excluded from view results.
- Views evaluate the predicate against all non-system, non-superseded messages in the campfire, regardless of read cursor.
- Only `full` role members may create views (`campfire:view` is a system tag).

### Predicate Grammar

Predicates use S-expression syntax. The grammar is:

```
predicate := boolean-expr | comparison-expr
boolean-expr := (and pred pred ...)     ; at least 2 arguments; short-circuit evaluation
              | (or  pred pred ...)     ; at least 2 arguments; short-circuit evaluation
              | (not pred)
comparison-expr := (gt  value-expr value-expr)
                 | (lt  value-expr value-expr)
                 | (gte value-expr value-expr)
                 | (lte value-expr value-expr)
                 | (eq  value-expr value-expr)
leaf-expr := (tag "string")            ; true if message has this tag (case-insensitive)
           | (sender "hex-prefix")     ; true if sender hex starts with prefix (case-insensitive)
           | (field "dot.path")        ; extract JSON field from payload; "payload." prefix is optional
           | (mul value-expr value-expr)
           | (pow value-expr value-expr)
           | (literal number)          ; numeric literal
           | (literal "string")        ; string literal
           | (timestamp)               ; message timestamp in unix nanoseconds
```

#### Operator Reference

| Operator | Arity | Arguments | Returns | Notes |
|----------|-------|-----------|---------|-------|
| `and` | N≥2 | boolean expressions | boolean | Short-circuit: returns false at first false child |
| `or` | N≥2 | boolean expressions | boolean | Short-circuit: returns true at first true child |
| `not` | 1 | boolean expression | boolean | Logical negation |
| `tag` | 1 | quoted string | boolean | Case-insensitive tag membership test |
| `sender` | 1 | quoted hex string | boolean | Case-insensitive prefix match on sender hex |
| `gt` | 2 | numeric expressions | boolean | `left > right` |
| `lt` | 2 | numeric expressions | boolean | `left < right` |
| `gte` | 2 | numeric expressions | boolean | `left >= right` |
| `lte` | 2 | numeric expressions | boolean | `left <= right` |
| `eq` | 2 | numeric or string expressions | boolean | String equality when both operands are strings; numeric equality otherwise |
| `field` | 1 | quoted dot-path string | value | Extracts nested JSON field from parsed payload; returns numeric, string, or bool depending on JSON type; returns empty result if field absent or payload not JSON |
| `mul` | 2 | numeric expressions | numeric | Multiplication |
| `pow` | 2 | numeric expressions | numeric | Exponentiation: `base ^ exponent` |
| `literal` | 1 | number or quoted string | value | Numeric literal if parseable as float64; string literal otherwise |
| `timestamp` | 0 | — | numeric | Message timestamp in unix nanoseconds |

#### Field Path Resolution

`(field "path")` extracts a value from the message's JSON payload using dot-notation. The prefix `payload.` is optional and stripped if present — `(field "confidence")` and `(field "payload.confidence")` are equivalent.

Nested fields: `(field "outer.inner.value")` navigates `payload["outer"]["inner"]["value"]`. If any segment is absent or the payload is not valid JSON, the result is empty (evaluates to false in boolean context, 0 in numeric context).

#### Predicate Depth Limit

The evaluator enforces a maximum recursion depth of 64 nodes. Predicates exceeding this depth return false rather than an error, preventing malicious predicates from causing stack overflows.

#### Example Predicates

```
; Messages tagged "memory:standing"
(tag "memory:standing")

; Messages tagged either "memory:standing" or "memory:anchor"
(or (tag "memory:standing") (tag "memory:anchor"))

; Messages tagged "memory:standing" with confidence above 0.5
(and (tag "memory:standing") (gt (field "confidence") (literal 0.5)))

; Messages from a specific sender prefix
(sender "abc123")

; Messages tagged "status" but not "draft"
(and (tag "status") (not (tag "draft")))

; Messages after a specific timestamp
(gt (timestamp) (literal 1710000000000000000))
```

## Field Projection (cf read --fields)

`cf read` supports a `--fields` flag that limits which message fields appear in output. This is a client-side projection applied after message retrieval; it does not affect what is stored or transmitted.

**Valid field names:** `id`, `sender`, `instance`, `payload`, `tags`, `timestamp`, `antecedents`, `signature`, `provenance`, `campfire_id`

**Semantics:**

- `--fields payload,tags` — include only the payload and tags fields in output
- Multiple fields are comma-separated; whitespace around commas is ignored
- Unknown field names are rejected with an error listing valid names
- When `--fields` is omitted or empty, all fields are displayed (backward-compatible default)
- The `instance` field appears only when non-empty, regardless of whether it is projected
- In `--json` mode, `--fields` applies to the JSON output; the shape of the output object changes to include only the requested keys

**Implementation note.** Field projection in `cf read` is a display concern, not a query concern. The store returns full `MessageRecord` objects; the projection is applied during rendering. This is distinct from named view projection, which operates as part of view materialization.

## Tag-Filtered Reads

`cf read` supports a `--tag` flag that filters messages by tag. Multiple `--tag` flags apply OR semantics: a message matches if it has any of the specified tags.

**SQL-level filtering.** Tag filtering is pushed down to the SQLite query using `json_each`. The query uses:

```sql
EXISTS (SELECT 1 FROM json_each(tags) WHERE LOWER(value) IN (?, ...))
```

This means tag filtering happens at the database level — only matching messages are loaded into memory. Tag matching is case-insensitive.

**Cursor behavior.** When `--tag` filters are active, the read cursor advances based on all messages retrieved before filtering (pre-filter timestamps). This ensures filtered-out messages do not reappear on the next read. The filter is a display concern, not a cursor concern.

**Interaction with named views.** Tag-filtered reads and named views are independent mechanisms. `cf read --tag foo` is an ad-hoc per-session filter. `cf view create` with `(tag "foo")` predicate creates a persistent, named, shareable filter. Use named views for filters that multiple agents need or that should survive across sessions.

## Reserved Tags: Extended Namespace

The following `campfire:*` tags are defined as of this revision. All require `full` membership role to send and are verified against the campfire's key by receivers.

| Tag | Description | Introduced in |
|-----|-------------|---------------|
| `campfire:member-joined` | A new member has joined | P0 |
| `campfire:member-evicted` | A member was evicted | P0 |
| `campfire:member-left` | A member voluntarily departed | P0 |
| `campfire:eviction` | Companion tag on eviction messages | P0 |
| `campfire:rekey` | Campfire rotated its keypair | P0 |
| `campfire:disband` | Campfire is being disbanded | P0 |
| `campfire:invite` | Invitation to join a campfire | P0 (member-signed exception) |
| `campfire:vouch` | Trust vouch for a member key | P0 (member-signed exception) |
| `campfire:revoke` | Revocation of a prior vouch | P0 (member-signed exception) |
| `campfire:compact` | Compaction event — marks messages superseded | P1 |
| `campfire:view` | Named view definition | P1 |
| `campfire:member-role-changed` | A member's role was changed | P1 |

**Member-signed exceptions.** `campfire:invite`, `campfire:vouch`, and `campfire:revoke` are signed by individual members, not the campfire key. All other `campfire:*` tags are signed by the campfire.

**P1 additions.** `campfire:compact`, `campfire:view`, and `campfire:member-role-changed` were introduced in the automaton substrate feature set. They extend the reserved namespace to cover log management (compact), persistent query definition (view), and role governance (member-role-changed).

## Wire Format

Not specified in this version. The protocol defines the logical structure of messages, provenance chains, and membership data. Serialization format (protobuf, msgpack, CBOR, JSON) is an implementation choice. The only requirement is that the serialization is deterministic for signature verification.

## CLI Reference (Implementation Sugar)

The protocol is independent of any CLI. The following commands are suggested sugar for implementations targeting AI agents and developers.

```
cf init                              # generate keypair, create agent identity
cf discover [--channel fs|dns|git|mdns] [--tag t] [--description s]  # list beacons (tainted fields withheld by default)
cf create [--protocol open|invite-only] [--require tag,...] [--tag t,...] [--transport proto] [--beacon channel]
cf join <campfire-id>                # request to join
cf admit <campfire-id> <member-key>  # sponsor a new member
cf invite <target-key> <campfire-id> # send invitation through a shared campfire
cf evict <campfire-id> <member-key>
cf leave <campfire-id>
cf disband <campfire-id>
cf vouch <campfire-id> <member-key>   # vouch for a member
cf revoke <campfire-id> <member-key>  # revoke a vouch
cf send <campfire-id> "message" [--tag tag,...]
cf dm <target-key> "message"         # sugar: create/reuse 2-member campfire, send
cf read [campfire-id]                # read messages, optionally filtered to one campfire
  --all                              # show all messages, not just unread
  --peek                             # show unread messages without updating cursor
  --follow                           # stream messages in real time
  --tag <tag> [--tag <tag> ...]      # filter by tag (OR semantics; SQL-level)
  --sender <hex-prefix>              # filter by sender prefix
  --fields <field,...>               # project: comma-separated subset of id,sender,instance,payload,tags,timestamp,antecedents,signature,provenance,campfire_id
  --pull <id[,id,...]>               # fetch specific messages by ID from local store
cf inspect <message-id>              # show full provenance chain
cf ls                                # list my campfires
cf members <campfire-id>
cf id                                # show my public key
cf compact <campfire-id>             # create campfire:compact event (full role required)
  --before <msg-id>                  # compact messages before this ID (default: all)
  --summary "text"                   # human-readable summary of compacted content
  --retention archive|discard        # local storage hint (default: archive)
cf view create <campfire-id> <name>  # create named view (full role required)
  --predicate <s-expr>               # S-expression predicate (required)
  --projection <field,...>           # field names to include in output
  --ordering "timestamp asc|desc"    # result ordering (default: timestamp asc)
  --limit <n>                        # max results (default: 0 = no limit)
cf view read <campfire-id> <name>    # materialize a named view
cf view list <campfire-id>           # list all defined views in a campfire
cf member set-role <campfire-id> <pubkey> --role observer|writer|full  # change member role (full role required)
```

