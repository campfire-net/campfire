# Design: Identity IS a Campfire

**Status:** Proposed
**Date:** 2026-04-02
**Item:** campfire-agent-ub4
**Process:** Adversarial design — four dispositions (adversary, creative, systems pragmatist, domain purist) deliberated via campfire, architect synthesized.

## Summary

The campfire protocol defines identity as a raw Ed25519 keypair, but the spec immediately declares that an agent has no standalone address and is reachable only through campfire memberships. This creates a lifecycle gap: after `cf init` an agent exists cryptographically but cannot communicate, be discovered, host conventions, or maintain state. Every capability that makes identity *useful* requires a campfire. This design collapses the distinction: an agent's identity IS a self-campfire. `cf init` becomes `cf create` for yourself. The campfire ID is the identity address. The keypair becomes an implementation detail of signing, not the external identity. This eliminates the only place in the protocol where something other than a campfire provides a coordination function, turning the design principle "all communication flows through campfires" from an aspiration with an exception into a structural invariant.

## Core Claim

**Identity IS a campfire.** Concretely:

- Every entity's identity is a self-campfire: a campfire where the entity is the sole member (member 0) and holds the campfire key. This applies uniformly to humans and agents — there is no protocol-level distinction between a human's home and an agent's home.
- The campfire ID (hex of the campfire's Ed25519 public key) is the agent's identity address. It replaces the agent keypair's public key as the externally-shared identifier.
- The agent still has an Ed25519 keypair for message signing. This keypair is an internal credential, not the external identity. It is the key that proves membership in the self-campfire, not the address others use.
- A self-campfire provides everything identity requires: address, message log, convention hosting, beacon advertising, trust establishment, and state. A naked keypair provides only signing.
- Every participant in the protocol — agent, bot, organization, service — is a campfire. The Member struct always wraps a campfire identity. Recursive composition becomes the only composition model.

## Design

### Self-Campfire

A **self-campfire** is a campfire that declares the `identity` convention as its genesis message. It is created atomically by `cf init` and serves as the agent's presence in the protocol.

**Creation.** `cf init` performs a single atomic operation:
1. Generate an Ed25519 keypair for the agent (the **agent key** — used for message signing).
2. Generate a separate Ed25519 keypair for the self-campfire (the **campfire key** — used for provenance hops, beacons, and as the campfire ID).
3. Create the campfire with `join_protocol: invite-only`, `threshold: 1`.
4. Admit the agent as member 0 (self-join).
5. Post the `identity` convention declaration as message 0, signed by the campfire key.
6. Publish a beacon with tag `identity:v1`.

**Typing mechanism.** A self-campfire is typed by its genesis message, not by a protocol flag. The identity convention declaration (message 0, signed by the campfire key, tagged `convention:operation`) IS the type assertion. Any agent inspecting a campfire can determine whether it is an identity campfire by checking: "Does message 0 contain a campfire-key-signed convention declaration for `identity`?"

This resolves **A7** (no protocol-level type distinction). The type is not a protocol field — it is a convention bootstrapping pattern. A campfire's first message is its own type declaration. This is verifiable from the campfire's message history alone, with no external registry and no tainted description field. The beacon tag `identity:v1` provides a discovery hint, but verification always goes back to the genesis message.

**Identity convention (minimum viable):**

```
convention: identity
version: 0.1
operations:
  - introduce-me      # genesis claim: pubkey, display_name, current_homes
  - verify-me         # challenge-response: proves key control
  - list-homes        # returns declared home campfires
  - declare-home      # declares a new home campfire (threads onto prior)
```

- `introduce-me`: signing=member_key, produces tag `identity:introduction`. Payload carries the agent's public key, display name (tainted), and list of home campfire IDs. This is a self-assertion, verifiable only by signature.
- `verify-me`: signing=member_key, args=[challenge:string]. Caller posts a nonce; the operator signs it. Proves the agent controls the key that is member 0 of this self-campfire.
- `list-homes`: signing=member_key, produces tag `identity:homes`. Returns all declared home campfires.
- `declare-home`: signing=member_key, args=[campfire_id, role:enum(primary,secondary,archive)]. Threads onto prior declaration, creating an audit trail.

### cf init Collapse

**Post-collapse, `cf init` does the following:**

1. Generates the agent keypair (Ed25519). Saves to `identity.json` as today. This is the agent's signing key — it never leaves the agent's control.
2. Creates the self-campfire. This generates a *separate* campfire keypair. The campfire's public key hex becomes the agent's identity address.
3. Admits the agent as member 0 with the agent's public key.
4. Posts the identity convention declaration as message 0 (signed by campfire key).
5. Posts `introduce-me` as message 1 (signed by agent key as member).
6. Sets the alias `home` to the self-campfire ID.
7. Publishes the beacon (tagged `identity:v1`).
8. Outputs: `Your identity campfire: <hex_id>. Share it like any beacon.`

**The hop-signing key problem (S2).** The campfire state file (`campfire.cbor`) stores the campfire private key in plaintext — this is by design for shared campfires where any member holding the state can sign provenance hops. For a self-campfire, the campfire private key is stored in the same state file. This is acceptable because:

- The self-campfire has exactly one member (the agent). There is no key-sharing concern.
- The campfire private key and the agent private key are *different keys*. Compromising the campfire state file does not compromise the agent's signing key (stored separately in `identity.json`).
- The agent private key is never written to `campfire.cbor`. It stays in `identity.json`, optionally passphrase-wrapped.

This is not a change to the key architecture. It is the existing architecture applied consistently: campfire keys go in campfire state, agent keys go in identity.json. The collapse does not merge these.

**Disband protection (A1).** Disbanding a self-campfire is identity destruction. The protocol must prevent this. Two mechanisms:

1. **Protocol guard:** `Disband()` checks for the identity convention declaration in message 0. If present, Disband returns an error: `"cannot disband identity campfire; use cf identity migrate to transfer identity"`. This is a protocol-level check (~15 LOC in `protocol/disband.go`), not a convention-level check. Justification: identity destruction is catastrophic and unrecoverable. A convention-level guard can be bypassed by any agent that ignores conventions. A protocol guard cannot.
2. **Recovery convention:** If an identity campfire becomes unreachable (transport failure, not disband), the agent can create a new self-campfire and post migration notices to campfires where it is a member. This is convention-level (the `declare-home` operation on the new campfire, cross-referencing the old campfire ID). It does not recover the old ID — it establishes a new one with a verifiable link to the old.

### Identity Address

The campfire ID is the identity address. At the wire level, this requires a transition — not a flag day.

**Dual-field transition (S4).** Add `SenderCampfireID` as a new field on Message:

```go
// message.go
type Message struct {
    ID                 uuid.UUID       `cbor:"1,keyasint"`
    Sender             ed25519.PublicKey `cbor:"2,keyasint"`           // agent pubkey (existing)
    // ... existing fields ...
    SenderCampfireID   []byte          `cbor:"10,keyasint,omitempty"` // self-campfire ID (new)
}
```

**Semantics:**
- New messages set both `Sender` (agent pubkey, for signature verification) and `SenderCampfireID` (self-campfire ID, for identity addressing).
- Old messages have `SenderCampfireID` empty. Readers fall back to `Sender` for identity.
- Signature verification always uses `Sender` (the agent pubkey signs the message). `SenderCampfireID` is informational — it tells the reader "the entity behind this agent pubkey is addressable at this campfire."
- `SenderCampfireID` is classified as **tainted** initially. To verify that a `SenderCampfireID` actually corresponds to the `Sender`, the verifier reads the self-campfire's member list and checks that `Sender` is member 0. This can be cached.

**Cryptographic binding (A2).** The campfire ID and agent keypair are linked by construction:
1. The self-campfire's genesis message (message 0) is signed by the campfire key.
2. Message 1 (`introduce-me`) is signed by the agent key (member 0).
3. The self-campfire's member list contains the agent's public key as member 0.
4. Proof chain: campfire ID -> campfire key -> genesis message proves the campfire exists. Member list -> agent pubkey -> message 1 signature proves the agent is member 0. The genesis message and message 1 are both in the same campfire, linked by the campfire's message log. This is the cryptographic link.

A third party verifying the binding: read the self-campfire, check that message 0 is a campfire-key-signed identity convention declaration, check that member 0's pubkey matches the `Sender` field. One campfire read, two signature checks. This can be cached per (campfire_id, agent_pubkey) pair with a TTL.

### Key Rotation

Key rotation must preserve the campfire ID (the identity address). Two tiers:

**Tier 1: Disposable identity (threshold=1, default).** The self-campfire has one member, threshold=1. The campfire key is held entirely by the agent. Key rotation for the *agent key* is straightforward: generate new agent keypair, post `campfire:vouch` for the new key signed by the old key, admit the new key, evict the old key. The campfire ID does not change — only the agent's signing key rotates.

However, if the *campfire key* needs rotation (compromise of the campfire state file), threshold=1 campfires must rekey. Rekey produces a new campfire key, which means a new campfire ID. For disposable identities, this is acceptable: the agent creates a new self-campfire and posts migration notices. The old ID is dead. Peers following the identity convention's `declare-home` audit trail can discover the new ID.

**Tier 2: Durable identity (threshold>=2, opt-in via `cf init --durable`).** The self-campfire is created with threshold=2. The agent holds one key share; a second "cold key" share is generated and stored offline (e.g., hardware key, printed recovery code). Under FROST (Dynamic-FROST re-sharing), campfire key rotation proceeds without changing the campfire's public key:

1. Agent generates new keypair K_new.
2. On the self-campfire, agent (using K_old) posts `campfire:vouch` for K_new.
3. Agent admits K_new as a member (now threshold=2 with K_old and K_new holding shares).
4. FROST re-sharing redistributes shares to K_new and the cold key.
5. Agent (using K_new) evicts K_old.
6. Re-sharing redistributes: K_new and cold key hold the shares. Campfire public key unchanged.

Result: campfire ID stable, agent signing key rotated, audit trail recorded in the campfire's message log. The cold key share is never used in normal operation — it exists solely to enable threshold re-sharing that preserves the campfire public key.

This resolves **A6** (key rotation = new campfire ID). For durable identities, rotation preserves the ID via threshold signatures. For disposable identities, rotation creates a new ID with a migration trail — this is a conscious design choice, not a deficiency.

### Multi-Home Identity

An operator may maintain multiple self-campfires (homes) — one per device, role, or organization. These are independent identity campfires with no protocol-level thread. Cross-home continuity is established via the **home-linking ceremony**, a pure convention operation.

**Home-linking ceremony (C3):**

1. Operator posts `declare-home(campfire_B)` on campfire A. Produces message M_A tagged `identity:home-declared`.
2. Operator posts `declare-home(campfire_A)` on campfire B. Produces message M_B tagged `identity:home-declared`. Payload includes M_A.id as reference.
3. Operator posts an echo message on campfire A:
   - Tags: `identity:home-echo`
   - Payload: `{ echo_of: M_B.id, signed_by_b: <signature over M_B.id using campfire_B's key> }`
   - This proves the operator of campfire B authorized the linking.

**Third-party verification:**
1. Read `list-homes` on campfire A — sees campfire B declared.
2. Read `list-homes` on campfire B — sees campfire A declared.
3. Verify the echo message: check `signed_by_b` against campfire B's public key.
4. Mutual declaration + cross-signed echo = operator controls both campfires.

**Forgery resistance (A3).** Under C3, a rogue campfire R cannot forge a link to campfire A because:
- R can post `declare-home(A)` on itself, but R cannot post `declare-home(R)` on campfire A (R is not a member of A).
- R cannot produce a valid echo on campfire A carrying a signature from campfire A's key (R does not hold A's campfire key).
- The ceremony requires write access to BOTH campfires and signing capability with BOTH campfire keys. A rogue that controls only one campfire can make a one-sided claim that fails verification at step 3.

**Bootstrap trust (A3a).** The first relying party learns the canonical self-campfire ID through the same mechanism as any campfire: beacon discovery, out-of-band sharing (QR code, published in a profile), or introduction through a shared campfire. This is not a regression — today's keypair identity has the same bootstrap problem (how do you learn someone's public key?). The self-campfire ID is simply a different opaque string shared through the same channels.

### Trust Graph Model

**Nodes** are campfires. **Edges** are typed trust relationships.

**Edge types:**
- **Membership:** campfire A is a member of campfire B. Semantics: A participates in B's coordination. Not transitive — B's members don't automatically trust A's members.
- **Delegation:** campfire A delegates authority to campfire B for a scoped purpose. Transitive within scope: if A delegates "deploy" to B and B delegates "deploy" to C, C can deploy on A's behalf. Scope is convention-defined, not protocol-defined.
- **Federation:** campfire A federates with campfire B. Semantics: discoverability propagates. A's members can discover B. B's members can discover A. Not access — federation does not grant membership or write access.
- **Vouch:** campfire A vouches for campfire B. Semantics: trust endorsement. Transitivity is determined by the consumer's trust policy, not by the protocol.

**Cycle handling (A4).** Trust graph cycles (A admits B, B admits C, C admits A) are not prohibited at the protocol level. Prohibiting them would require global graph knowledge, which violates the decentralized model. Instead:

1. **Detection at graph-walk time.** Any agent walking the trust graph to evaluate a trust claim MUST track visited nodes and terminate on cycle detection. The walk returns "cycle detected" as a trust evaluation result — the consumer decides whether cycles are acceptable for their use case.
2. **Depth limits.** Trust graph walks have a configurable maximum depth (default: 6 hops). This bounds the cost of cycle detection and prevents unbounded graph traversal.
3. **Convention-level acyclicity.** Specific conventions (e.g., delegation) MAY declare acyclicity as a convention constraint. A delegation convention can reject a delegation edge that would create a cycle within the delegation subgraph. This is enforced by the convention handler, not the protocol.

**Verification cost (A5).** Identity verification under campfire-as-identity requires a campfire read (network round-trip) instead of a local signature check. Mitigations:

1. **Verification caching.** Once a verifier has confirmed that agent pubkey P is member 0 of self-campfire C, this binding is cached with a TTL (default: 1 hour). Within the TTL, signature verification remains O(1) — verify the signature against P, look up C in the cache.
2. **Inline proof.** The `SenderCampfireID` field carries the campfire ID alongside the message. Combined with a cached membership snapshot, verification requires no additional network call for repeated interactions.
3. **Lazy verification.** For high-frequency agent-to-agent interactions within a shared campfire, the shared campfire's own membership verification is sufficient. The self-campfire lookup is only needed when establishing identity for a new peer.

### Migration Path

The collapse is implemented in three phases. Each phase is independently shippable and valuable. No phase requires the subsequent phases to be useful.

**Phase 1: Home-linking convention (~300 LOC, fully additive, ship first).**

What ships:
- Identity convention declaration (YAML/JSON).
- `cf home link <campfire-id>` CLI command.
- Convention handler for `declare-home`, `list-homes`, and the echo ceremony.
- Beacon tag `identity:v1` for discovery.

What breaks: Nothing. This is purely additive. Existing agents ignore the convention. New agents can declare home links. No wire changes, no store schema changes, no existing code modifications.

Who updates: Only agents that want multi-home identity. All others are unaffected.

**Phase 2: Collapse init/create (~200-300 LOC, 1 week).**

What ships:
- `cf init` creates a self-campfire instead of a separate home campfire.
- Self-campfire genesis message carries the identity convention declaration.
- `cf init --durable` creates a threshold=2 self-campfire.
- `cf init` output changes from keypair path to campfire ID.

What breaks:
- New `cf init` produces a different identity structure than old `cf init`. Existing identities are unaffected (they still work with their existing home campfires).
- `protocol.Init()` API changes: returns a Client whose `PublicKeyHex()` is the self-campfire ID, not the agent pubkey. SDK consumers that store `PublicKeyHex()` output need to be aware of the semantic change.
- Migration for existing agents: `cf identity upgrade` command converts an existing identity to a self-campfire by creating one with the existing home campfire's ID preserved (if possible) or creating a new self-campfire and linking it.

Who updates: SDK consumers (check `PublicKeyHex()` semantics). CLI users (new `cf init` output). Existing agents optionally via `cf identity upgrade`.

**Phase 3: Dual-field wire transition (~400-600 LOC, deprecation period required).**

What ships:
- `SenderCampfireID` field (field 10) on Message.
- All `NewMessage()` callsites set `SenderCampfireID` when the agent has a self-campfire.
- All Sender-reading callsites prefer `SenderCampfireID` when present.
- Operator account billing re-keyed to support campfire ID lookup alongside pubkey lookup.
- CLI display shows campfire ID as primary identity, pubkey as secondary.

What breaks:
- Old readers ignore `SenderCampfireID` (omitempty, CBOR forward-compatible). No breakage for old readers.
- New readers seeing old messages without `SenderCampfireID` fall back to `Sender`. No breakage.
- Billing layer needs dual-keying during transition: lookup by pubkey OR campfire ID. After deprecation period, pubkey-only lookups are removed.

Who updates: All protocol consumers eventually. But forward compatibility means old consumers keep working indefinitely. The dual-field approach means there is no flag day.

**Deprecation timeline:** `Sender`-only identity (no `SenderCampfireID`) is deprecated one major version after Phase 3 ships. Removal (if ever) is two major versions after that.

## Spec Changes Required

Based on Domain Purist findings (P4), the following spec sections need new or rewritten language:

1. **Primitives > Identity.** Rewrite: "An agent's identity is a self-campfire created at initialization. The self-campfire's ID is the agent's address. The self-campfire provides address, message log, convention hosting, and cryptographic identity. There is no identity without a campfire." The `Identity { public_key: bytes }` struct becomes a signing credential, not the identity primitive.

2. **CLI > cf init.** Rewrite: "Create a self-campfire. This is the agent's identity. The self-campfire's ID is the agent's address. Options: `--durable` creates a threshold>=2 self-campfire for key-rotation-stable identity."

3. **New section: Self-Campfire.** Define what a self-campfire is at the protocol level: a campfire with exactly one member (the agent), `join_protocol: invite-only`, the identity convention declaration as message 0. Specify default `reception_requirements` and transport.

4. **New section: Multi-Home Identity.** Non-normative note acknowledging that agents may maintain multiple self-campfires. Cross-identity continuity is a convention concern (home-linking ceremony), not a protocol concern.

5. **Primitives > Member.** Clarify that `Member.identity` may reference a self-campfire ID (a campfire acting as a member) or a raw keypair (legacy). The recursive composition section already handles campfire-as-member but the Member struct definition does not reflect this.

6. **Security > Reachability.** Make explicit: "An agent is reachable at its self-campfire. To send a message to an agent, join a campfire the agent is a member of, or send to the agent's self-campfire directly (if transport allows)." The implicit model becomes explicit.

## Open Questions

| # | Question | Owner | Blocker Condition |
|---|----------|-------|-------------------|
| Q1 | Should `cf init --durable` use a generated cold key or require the user to provide one (e.g., hardware key)? | Protocol team | **Resolved:** Generate cold key by default, output as BIP-39 recovery phrase, store offline. Hardware key support added later as `cf identity upgrade --cold-key <hardware>`. |
| Q2 | How does the naming layer (`pkg/naming/`) resolve a human-readable name to a campfire ID? The current TOFU name -> pubkey mapping needs to become name -> campfire ID. | Naming team | **Resolved:** Value type changes from pubkey bytes to campfire ID bytes. Existing names re-registered. TOFU pin changes from pubkey to campfire ID. Not a blocker for Phases 1-2; required before Phase 3 is complete. |
| Q3 | Should the trust-graph meta-campfire (C4) be part of this design or a separate proposal? | Architect | **Resolved:** Separate design. Builds on this design AND the existing trust convention (`agentic-internet/docs/conventions/trust.md`), which already defines the local-root model and federation rules. |
| Q4 | What is the verification caching TTL for production deployments? 1 hour is a starting point. | Operations | **Resolved:** Default 1h, configurable per deployment. Ship with 1h, tune from telemetry after Phase 3 deployment. |
| Q5 | Should shared/organizational identities (threshold > 1 self-campfires with multiple human members) be explicitly supported or deferred? | Protocol team | **Resolved:** Deferred. Protocol supports it mechanically (FROST). Policy and UX questions (who speaks as the org, recovery paths) are deep and belong in a separate design. Noted in spec as architectural consequence. |
| Q6 | How do existing hosted-service operator accounts (keyed on pubkey in Azure Table Storage) migrate to campfire-ID keying? | Hosting team | **Resolved:** Dual-key the table during transition — lookup by pubkey OR campfire ID, whichever is present on the message. Clean up pubkey-only lookups after deprecation period. |

## Layer 2: Social Graph UX Model (Separate Design)

This design covers the protocol layer. A second design — the social graph UX model — sits on top of it and is filed as a separate item. Key claims from that design:

- **Home is uniform.** Every entity (human or agent) has a home. Same concept, no asterisk. Human-control is a graph property (delegation chain traces back to a home with no parent), not a home type.
- **Two-word vocabulary.** Home (your self-campfire) and campfire (any shared space). Scope lives in the name, not a type label. No "project center", "org center", "system center" — just names.
- **Social verbs for humans, protocol verbs for agents.** connect, post, follow, trust, delegate vs. send, subscribe, await, fulfill.
- **Human provenance is key custody + convention.** Protocol cannot establish it. Established through: who holds the private key (handoff moment), sysop-provenance attestation levels, out-of-band binding. Bootstrap is always out-of-band.
- **Scope model is uniform.** System, project, org, global — all campfires, nested by membership. Navigate by name. Manage your neighborhood, not the full tree.

See: design item campfire-agent-c92, design campfire c40221b457039a0d42e24498c367bdeba6609a16585b006e7d66579b2944db87.

## Adversary Attack Disposition

| Attack | Status | Resolution |
|--------|--------|------------|
| **A1** — Disband destroys identity | **Resolved** | Protocol guard in `Disband()`: check for identity convention in message 0, reject disband of identity campfires. ~15 LOC. Recovery via migration convention for transport failures. |
| **A2** — Campfire ID and keypair cryptographically unlinked | **Resolved** | Linked by construction: genesis message (campfire-key-signed) + member list (agent pubkey as member 0) + introduce-me (agent-key-signed). Three-step proof chain verifiable from the self-campfire's message log and member list. |
| **A3** — Multi-home forgery trivial without root key | **Resolved** | Home-linking ceremony requires write access to BOTH campfires and signing with BOTH campfire keys. A rogue controlling only one campfire produces a one-sided claim that fails cross-verification. |
| **A4** — Trust graph cycles | **Permanent Constraint** | Cycles are not prohibited (would require global graph knowledge, violating decentralization). Mitigated by: detection at walk time, depth limits (default 6), convention-level acyclicity constraints for specific edge types (e.g., delegation). |
| **A5** — Identity verification cost O(n) vs O(1) | **Resolved** | Verification caching (TTL-based), inline proof via SenderCampfireID field, lazy verification within shared campfires. Amortized cost approaches O(1) for repeated interactions. First-contact cost is one campfire read — comparable to any beacon-based discovery. |
| **A6** — Key rotation produces new campfire ID | **Resolved** | Two tiers: disposable (threshold=1, rotation = new ID + migration trail) and durable (threshold>=2, FROST re-sharing preserves campfire ID). Operator chooses tier at init time. |
| **A7** — No protocol-level type distinction for identity campfires | **Resolved** | Type is the genesis message. Message 0 carrying a campfire-key-signed identity convention declaration IS the type proof. Verifiable from message history alone. Beacon tag `identity:v1` aids discovery. No protocol flag needed. |
| **A8** — Migration breaks everything simultaneously | **Resolved** | Three-phase migration with no flag day. Phase 1 is fully additive. Phase 2 changes only new init flows. Phase 3 uses dual-field wire transition (SenderCampfireID alongside Sender, omitempty). Old consumers keep working. Deprecation over two major versions. |
