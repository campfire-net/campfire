# Identity Trust Resolution — Convention Layer v0.2

**Status:** draft
**Date:** 2026-04-10
**Item:** campfire-ead
**Depends on:** campfire-79e (approved) — provides the `identity:granted` message type this pattern reads
**Blocks:** campfire-ab7 (revocation subsystem)
**Input:** `resonant/docs/specs/identity-tree-constraints.md`, `campfire/docs/conventions/identity-delegation-v0.1.md`, `campfire/docs/identity-audit.md`

## 1. Purpose

Define the **convention-layer read pattern** by which a verifier decides whether to trust a given message sender, using only the existing `identity:granted` messages in a campfire's log plus a small local trust-anchor config. No protocol change. No new Message field. No new CBOR keys. This document is the authoritative contract any verifier implementation must honor; Go helpers in `pkg/convention/delegation` are one implementation.

## 2. Scope

**In:**
- The resolution algorithm (what a verifier does given a sender pubkey)
- The trust-anchor config shape
- Typed outcomes for convention handlers to interpret
- Caller-policy options (strict, permissive, audit-only)

**Not in:**
- Any change to `pkg/message`, `pkg/protocol`, or `pkg/transport`
- Any change to the Message wire format
- Any new convention operation or message type
- Any required Go API — this is a read-pattern contract
- Revocation (ab7)
- Trust-anchor sync across machines (follow-up)

## 3. Trust anchors

A trust anchor is a pubkey the verifier has decided to trust directly, without walking further. The operator curates the list.

**Config location:** the existing `~/.cf/config.toml` cascade defined in `pkg/protocol/config.go`. A new top-level section:

```toml
[identity.trust]
anchors = [
  "HiJMFLx5Wb9r7H1OVjOJtsPT6SVa0gbfG6YM3FIZf/0=",  # baron root
  "<baron@surface machine subkey>",
  "<baron@workshop machine subkey>",
  "<proj:campfire-agent identity>",
  "<service:cf-mcp identity>",
]
```

Each anchor is a base64- or hex-encoded ed25519 public key. The config cascade rules are unchanged: global `~/.cf/config.toml` → project `.cf/config.toml` → env → CLI flags. Project configs may extend (not override) the global anchor list.

**Why a list and not a single root:** the common case is multi-machine multi-service, with several directly-trusted identities at different tree depths. Forcing "the one true root" would require deep walks for every verification. A flat list lets the verifier stop as soon as it hits any trusted identity.

**How anchors get into the list:** manual curation by the operator, or written at `rd init` / machine-link time by whichever tool owns identity minting. This convention does not prescribe the write path; it only reads the config.

**Sync across machines:** out of scope for v0.2. A future convention can propagate anchors via messages in a dedicated trust campfire, but that is a separate design.

## 4. Resolution algorithm

Given a message `msg` received in campfire `C`, the verifier answers: "is `msg.Sender` authorized to speak for any trusted anchor?"

```
resolve(msg, C):
    sender = msg.Sender
    chain = []
    depth = 0
    current = sender

    while depth < MAX_CHAIN_DEPTH (= 10):
        if current in trust_anchors:
            return Resolved{chain, anchor: current}

        grant = find_most_recent_grant(C, child_pubkey = current)
        if grant is None:
            return DeadEnd{chain, last_resolved: current}

        if not validates(grant, C):
            return InvalidGrant{chain, bad_grant: grant}

        chain.append(grant)
        current = grant.Sender   # walk up one hop
        depth += 1

    return DepthExceeded{chain}
```

**`find_most_recent_grant(C, child_pubkey)`** queries the local log of campfire `C` for messages tagged `identity:granted` whose payload has `child_pubkey == <arg>`, and returns the one with the largest `msg.Timestamp`. If none exist, returns nil. This is a cheap read against the local campfire store — no network, no cross-campfire lookup.

**`validates(grant, C)`** runs all five validation rules from identity-delegation §4 against the grant message: signature verifies, payload.campfire_id equals C, not expired, not ceiling-violated, not revoked (once ab7 lands).

**Why only the local campfire:** cross-campfire walks would require the verifier to fetch from campfires it may not be a member of, introducing a lookup path we explicitly want to avoid. Instead, the trust-anchor list is expected to contain any identity that a verifier legitimately needs to trust without a local grant chain — typically an identity at 1-2 hops of depth from the leaf. If the chain reaches an untrusted identity, the verifier returns `DeadEnd` and the caller decides.

## 5. Outcomes

The resolution returns one of four typed outcomes:

| Outcome | Meaning | Typical caller action |
|---|---|---|
| `Resolved{chain, anchor}` | The sender traces back to a trust anchor via a valid grant chain of length `len(chain)`. | Trust the sender for this action. Display the chain in `--audit` views. |
| `DeadEnd{chain, last_resolved}` | The walk stopped because no grant was found in the local log for `last_resolved`, and `last_resolved` is not a trust anchor. | Decide per caller policy: strict rejects, permissive treats as unattributed, audit-only logs and allows. |
| `InvalidGrant{chain, bad_grant}` | A grant was found but failed a validation rule (signature, campfire_id, expired, ceiling, revoked). | Always reject. Log the bad grant for investigation. |
| `DepthExceeded{chain}` | The walk exceeded `MAX_CHAIN_DEPTH = 10`. | Reject. A legitimate chain is never this deep; this indicates either a pathological chain or a cycle. |

**Cycle detection:** included implicitly by the depth cap. An explicit check (seen-set) is optional; the cap is sufficient for v0.2.

**MAX_CHAIN_DEPTH = 10:** the target model's deepest legitimate chain is 4-5 hops (baron → machine → project → service → worker). 10 is generous; tightening to 6 or 8 is also defensible. See §9.

## 6. Caller policy

The convention defines the **resolution** but does not dictate the **policy** a caller applies to the outcome. Callers choose one of three modes per operation:

- **Strict:** accept only `Resolved`. Reject `DeadEnd`, `InvalidGrant`, `DepthExceeded`. Appropriate for security-critical operations (revocation propagation, billing, cross-tenant writes).
- **Permissive:** accept `Resolved` and `DeadEnd`; reject `InvalidGrant` and `DepthExceeded`. Appropriate for legacy message compatibility and during migration.
- **Audit-only:** accept all outcomes but record the result. Appropriate for display paths (`rd show --audit`, dashboards) where rejection would hide information the operator needs to see.

Caller policy is a property of the call site, not the convention. A single verifier can apply different modes to different operations. This document does not pick a default — that's the call site's call.

## 7. Failure modes

1. **No trust anchors configured.** Every resolution returns `DeadEnd` immediately on sender check. Callers running strict mode reject everything. Callers running permissive mode accept everything. Appropriate default behavior depends on the call site — this convention does not decree one.
2. **The campfire's message store is unreadable.** Resolution returns `DeadEnd` with `chain == []`; callers treat as unattributed. No network retries — the store is either local and fast or missing.
3. **Grant message found but fails signature verification.** Returns `InvalidGrant`. The caller should log the bad grant for investigation; it is evidence of either corruption or tampering.
4. **Multiple grants exist for the same child_pubkey.** Use the most recent by `msg.Timestamp`. Older grants are superseded, not aggregated. A re-grant is a normal lifecycle event (e.g., a session extending its own grant before expiry).
5. **Clock skew** beyond 60 seconds invalidates grants that would otherwise be valid. Fixing the verifier's clock is an ops concern; this convention does not work around misconfigured clocks.

## 8. Ceremony audit checklist

| # | Question | Answer |
|---|---|---|
| 1 | Does the user type any new command? | No. Resolution is a read pattern invoked programmatically by readers. |
| 2 | Does `rd init` on machine-2 require any new flag or step? | No. The config section is optional; if absent, verifiers operate in whatever default policy they pick. If a project wants to specify trust anchors at init time, it adds them to `.cf/config.toml` — same one-time write as any other project config. |
| 3 | Scenario where user must manually revoke a grant? | No (revocation is ab7). |
| 4 | Any failure mode "start blocked, please do X"? | No. All failure modes have a typed outcome; callers decide. |
| 5 | Any command fails when network is unreachable? | No. Resolution reads the local campfire log only. No cross-campfire lookups, no network. |
| 6 | Steps to onboard new machine? | Trust anchors must be present on the new machine for strict-mode verification to work. For permissive-mode verification, no setup is required. Anchor sync across machines is a follow-up; for v0.2, it is either a 1Password sync of `~/.cf/config.toml`, a copy-paste step, or an operator-scripted write at rd init time. |

All six pass, with one caveat at question 6: trust-anchor sync across machines is not solved by this convention. It is a deliberate follow-up.

## 9. Security properties

- **Grant tampering** is detected by signature verification in the validation rules; tampered grants return `InvalidGrant`.
- **Cross-campfire grant replay** is closed by identity-delegation's rule 2 (campfire_id binding).
- **Anchor list tampering** (an attacker modifies `~/.cf/config.toml` to add their own pubkey to `identity.trust.anchors`) grants them trust on the affected machine. This is the same threat model as any local-config-file tamper — if an attacker has write access to the config, the machine is already compromised. This convention does not harden against that threat; a follow-up could sign the anchor list with the baron root key and reject unsigned edits.
- **Cycle attacks** (A grants B, B grants A) are bounded by `MAX_CHAIN_DEPTH = 10`; the walk terminates even on adversarial input. Explicit cycle detection is a cheap additional safeguard and can be added without changing the outcome type.
- **Depth-exhaustion DoS** is bounded by the cap; the walker cannot be tricked into unbounded work.
- **Grant preemption** (an attacker posts a fresh grant for a child pubkey they don't control) requires them to hold the parent's private key. Standard ed25519 security.
- **Revoked-key bypass** is deferred to ab7; until ab7 lands, a revoked key remains valid until expiry.

## 10. Conformance

An implementation conforms with this document if:

- Given a message and a trust-anchor list, it produces the outcome defined by §4/§5, treating grants per identity-delegation §4 validation rules.
- It does not perform cross-campfire lookups as part of resolution.
- It respects the `MAX_CHAIN_DEPTH` cap.
- It exposes the four outcome types to callers so that policy mode is the caller's choice.

There is no required Go API surface. A shell script that reads the campfire log, filters for `identity:granted`, and follows the algorithm is conformant.

## 11. Open questions

### Q1. `MAX_CHAIN_DEPTH` value

Default: 10. Tightening to 6 or 8 is defensible — the target model's deepest legitimate chain is 4-5 hops. Looser caps tolerate pathological input that an operator might not immediately notice.

**My call:** 10.

### Q2. Cycle detection

Implicit via depth cap, or explicit seen-set?

**My call:** implicit for v0.2. Explicit can be added later with no outcome-type change.

### Q3. Default caller policy

Each call site picks its own. But it would be convenient to have a recommended default per layer:

- **Protocol read path:** permissive (don't break legacy senders during migration).
- **Convention handlers:** strict for write operations, permissive for read operations.
- **UI / audit:** audit-only.
- **Revocation subsystem (ab7):** strict.

**My call:** state these recommendations as non-binding guidance in the doc; callers override as needed.

That's it. Three open questions, all with defaults picked. No protocol change, no message format change, no new CBOR keys, no new convention operations.

## 12. Handoff

1. **Config schema.** Add `[identity.trust]` section to `pkg/protocol/config.go` and document in `docs/cli-conventions.md`. Single field: `anchors []string`.
2. **Go implementation.** `pkg/convention/delegation/trust.go` (new package) implementing `Resolve(ctx, client, campfireID, sender, anchors) Outcome`. One function, four outcome types, tests for each.
3. **Integration:** wire `Resolve` into the convention server's `IdentityResolver` chain as an optional pass — if a `GrantChainResolver` is installed, it populates `IdentityInfo` with the resolved chain and anchor. Callers read `req.Identity.Chain` and `req.Identity.Anchor`.
4. **Audit path:** add a minimal `rd show --audit <item-id>` that calls `Resolve` for each contributing message and displays the chain.

Revocation enforcement (ab7) layers on top of this: the ab7 validation layer calls `Resolve` and rejects messages whose chain hits a revoked key at any hop.
