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
  timestamp: uint64          # sender's wall clock, not authoritative
  signature: bytes           # sender signs (id + payload + tags + timestamp)
  provenance: [ProvenanceHop]
}
```

Tags are freeform strings. The protocol doesn't define a tag vocabulary. Campfires define which tags are reception-required. Filters operate on tags. Agents apply tags when sending. Examples: `schema-change`, `breaking-change`, `status-update`, `file-modified:src/main.rs`.

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
  filters: [MemberFilter]           # per-member, bidirectional
  transport: TransportConfig         # how this campfire moves bytes
  created_at: uint64
}
```

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

## Operations

### Campfire Lifecycle

**Create.** Any agent can create a campfire. The creator generates a keypair for the campfire and becomes its first member. The creator specifies join protocol, reception requirements, and transport.

**Disband.** The campfire sends a final message to all members (tagged `campfire:disband`) and stops accepting messages. Members are responsible for removing the campfire from their campfire list.

### Membership

**Join.** An agent requests to join a campfire. The join protocol determines what happens:
- `open`: agent is immediately admitted
- `invite-only`: a current member must admit the agent
- `delegated`: the campfire designates one or more members as admittance delegates

On join, the new member's transport details for this campfire are registered. The campfire sends a `campfire:member-joined` message to all existing members.

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

**Leave.** A member voluntarily departs. The campfire sends a `campfire:member-left` message to remaining members.

### Messaging

**Broadcast.** A member sends a message to a campfire. The campfire:
1. Verifies the sender's signature
2. Applies the sender's `filter_out` (campfire's filter on what it accepts from this member)
3. If the message passes, appends a provenance hop and delivers to all other members
4. For each recipient, applies their `filter_in` (campfire's filter on what it sends to this member)
5. Delivers to recipients whose filters passed

**Relay.** When a campfire is a member of a parent campfire, broadcasts that pass the campfire's own outbound filter to the parent are relayed as broadcasts from the campfire (not from the original sender). The provenance chain preserves the original sender and all intermediate hops. The parent campfire sees the child campfire as the immediate sender and applies the child's `filter_out` accordingly.

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

A child campfire that joins a parent with reception requirement `schema-change` must ensure its own members produce `schema-change` messages when appropriate. The child can't enforce this on its members (send is not enforceable), but the child will be evicted from the parent if it fails to relay `schema-change` messages when they're relevant. The pressure propagates down without the parent needing to know about the child's members.

## Transport Negotiation

Transport is specified per campfire at creation time and agreed upon by members at join time.

```
TransportConfig {
  protocol: string       # "unix-socket", "http-webhook", "ws", "nats", "filesystem", etc.
  config: map            # protocol-specific configuration
}
```

A member that cannot speak the campfire's transport cannot join. Transport migration (campfire switches from unix socket to HTTP because a remote member joined) requires agreement from all current members or re-creation of the campfire.

The protocol is transport-agnostic. The only requirement is that the transport supports:
- Reliable delivery (or at least delivery acknowledgment)
- Sender authentication (the transport must not allow spoofed sender identity)

## Security Considerations

**Identity spoofing.** All messages are signed. A recipient verifies the sender's signature against their known public key. A provenance chain with an invalid signature at any hop is rejected entirely.

**Membership snapshot verification.** Provenance hops include a Merkle hash of the membership set. Any member can request the full set from the campfire and verify the hash. A campfire that lies about its membership in provenance hops can be detected by any member that independently verifies.

**Malicious campfire.** A campfire could fabricate provenance hops, claiming messages passed through it when they didn't. Since the campfire signs its own hops, this is detectable only if members independently verify with the claimed upstream campfire. The protocol trusts campfires to honestly relay. A campfire's reputation is its track record of honest provenance.

**Private campfire confidentiality.** In a two-member campfire (private conversation), only the two members and the campfire itself see message content. Messages can be encrypted with the recipient's public key for end-to-end confidentiality, making the campfire a blind relay. The protocol doesn't mandate encryption but the identity system supports it.

**Agent reachability.** Agents have no standalone address. They are reachable only through campfires they belong to. An agent that leaves all campfires is unreachable. This is a feature: leaving all campfires is how an agent goes dark.

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
