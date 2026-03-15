# Campfire Protocol Specification

**Status:** Draft
**Date:** 2026-03-15
**Author:** Baron + Claude

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
  id: uuid
  sender: public_key
  payload: bytes
  tags: [string]             # category labels, used by filters and reception requirements
  antecedents: [uuid]        # message IDs this message builds on (may reference messages not yet sent)
  timestamp: uint64          # sender's wall clock, not authoritative
  signature: bytes           # sender signs (id + payload + tags + antecedents + timestamp)
  provenance: [ProvenanceHop]
}
```

Tags are freeform strings. The protocol doesn't define a tag vocabulary. Campfires define which tags are reception-required. Filters operate on tags. Agents apply tags when sending. Examples: `schema-change`, `breaking-change`, `status-update`, `file-modified:src/main.rs`.

Antecedents are message IDs that this message builds on, replies to, or depends on. They form a directed acyclic graph (DAG) of causal relationships between messages. See **Message DAG** below.

### Provenance Hop

Each campfire that relays a message appends a hop.

```
ProvenanceHop {
  campfire_id: public_key
  membership_hash: bytes     # Merkle root of sorted member public keys at time of relay
  member_count: uint
  join_protocol: string      # "open", "invite-only", etc.
  reception_requirements: [string]   # required tags at time of relay
  timestamp: uint64
  signature: bytes           # campfire signs (message.id + all fields above)
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

Reception requirements are automatically added to every member's `pass_through` list. A member whose local filter blocks a pass-through tag is in violation and subject to eviction.

### Beacon

A beacon advertises a campfire's existence to potential members who have no existing connection.

```
Beacon {
  campfire_id: public_key
  join_protocol: string
  reception_requirements: [string]
  transport: TransportConfig
  description: string          # human/agent-readable purpose
  signature: bytes             # campfire signs all fields above
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

## Operations

### Campfire Lifecycle

**Create.** Any agent can create a campfire. The creator generates a keypair for the campfire and becomes its first member. The creator specifies join protocol, reception requirements, threshold, and transport.

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

**Antecedent references.** Antecedents are claims, not proofs. A message can reference a message ID the recipient has never seen — the referenced message may live in another campfire, may not have been relayed yet, or may not yet exist (futures). The antecedents field is covered by the sender's signature and cannot be tampered with in transit. A malicious sender could reference nonexistent message IDs, but this is no different from sending misleading payload content — the protocol authenticates the sender, not the truth of their claims.

## Wire Format

Not specified in this version. The protocol defines the logical structure of messages, provenance chains, and membership data. Serialization format (protobuf, msgpack, CBOR, JSON) is an implementation choice. The only requirement is that the serialization is deterministic for signature verification.

## CLI Reference (Implementation Sugar)

The protocol is independent of any CLI. The following commands are suggested sugar for implementations targeting AI agents and developers.

```
cf init                              # generate keypair, create agent identity
cf discover [--channel fs|dns|git|mdns]  # list beacons visible from here
cf create [--protocol open|invite-only] [--require tag,...] [--transport proto] [--beacon channel]
cf join <campfire-id>                # request to join
cf admit <campfire-id> <member-key>  # sponsor a new member
cf invite <target-key> <campfire-id> # send invitation through a shared campfire
cf evict <campfire-id> <member-key>
cf leave <campfire-id>
cf disband <campfire-id>
cf send <campfire-id> "message" [--tag tag,...]
cf dm <target-key> "message"         # sugar: create/reuse 2-member campfire, send
cf read [campfire-id]                # read messages, optionally filtered to one campfire
cf inspect <message-id>              # show full provenance chain
cf ls                                # list my campfires
cf members <campfire-id>
cf id                                # show my public key
```

## Open Questions

1. **Message ordering within a campfire.** Does the campfire guarantee delivery order? Probably yes for single-campfire broadcasts, probably no across campfires. Lamport timestamps? Vector clocks? Or just "good enough" wall clock ordering?

2. **Message TTL and history.** How long does a campfire retain messages? For filter optimization, some history is needed. For offline members, buffering is needed. Unbounded retention is a resource problem. Campfire-configurable TTL?

3. **Eviction authority.** Who can manually evict? Creator only? Any member? Majority vote? Campfire-configurable, probably.

4. **Beacon spam.** Open campfires advertising on public beacon channels (DNS, HTTP well-known) could attract unwanted joins. Delegated admittance mitigates this (the delegate filters before admission), but open campfires on public channels may need additional defense. The cost of the delegate's time is the campfire's admission price.

5. **Key rotation.** If an agent's private key is compromised, how do they rotate? They need to announce a new public key through all their campfires, and all members need to update their records. The old key must be revoked. The protocol doesn't currently address this.

6. **Filter transparency.** Should a member be able to inspect the campfire's filter on their edge? Knowing "the campfire is suppressing my status-update messages" would be useful feedback. But exposing filter internals might allow gaming.

7. **Cost accounting.** Who pays for campfire operation (compute, storage, bandwidth)? At internet scale this matters. The campfire operator? Split among members? The protocol doesn't address economics.

8. **Maximum provenance chain depth.** Should there be a protocol-level limit? Or is this self-regulating (deeply nested messages are filtered out by intermediate campfires because they're expensive to process)?
