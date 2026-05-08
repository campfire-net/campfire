# Sysop Delegation Convention

**Version:** Draft v0.1 → cf-delegation 1.0 (OPEN-012 amendment)
**Working Group:** WG-1 (Discovery)
**Date:** 2026-03-28
**Amended:** 2026-04-28 (cf-delegation 1.0 — campfireagent-0b3)
**Status:** Draft (amended)

> **cf-delegation 1.0 amendment (OPEN-012):** The `sysop_override: true` field defined in §5.2 of the original v0.1 draft is removed. Pass 1 of the 0.30 design review flagged it as P3-leaking: any claimed Level 2+ sysop could assert global override revocation authority across all campfires, regardless of campfire membership. cf-delegation 1.0 replaces it with a campfire-scoped "owner-of-record override" (§8.4): only the campfire's owner-of-record may issue override revocations, and only within that campfire. This is a narrowing, not a removal of the emergency break capability. Implementations targeting cf-delegation 1.0 MUST reject `sysop_override: true` as a revocation authority.

---

## 1. Problem Statement

The sysop-provenance convention establishes that all agent activity is ultimately accountable to a human sysop — the sysop who runs the board and is responsible for everything on it. It defines four provenance levels (0–3) and a verification mechanism that proves a human controls a contact method.

But that model is flat: one verified sysop key, one or more agent keys operating under it. Real deployments are not flat. A sysop runs a fleet of agents. Some agents spawn sub-agents. A company has multiple humans sharing sysop accountability. An organization deploys agents across many services, each with its own operational identity, all tracing back to a common root.

The missing piece: **delegation chains**. The insight the sysop-provenance convention defers to future work (§15.3, §15.4) is this: every agent is working on behalf of a client, and every client ultimately traces back to a human sysop. The accountability structure is a DAG whose roots are always Level 2+ human sysops.

This convention defines:

1. A **delegation message format** — a sysop key (or delegated key) cryptographically signs authority over a subordinate key.
2. A **chain verification algorithm** — given an agent key, trace the delegation DAG back to a Level 2+ root.
3. **Depth limits** — maximum delegation hops, configurable per-campfire with a sensible default.
4. **Delegation revocation** — a delegator revokes a delegation; revocation cascades to all sub-delegations.
5. **Team sysops** — M-of-N threshold signatures for organizational accountability shared across multiple humans.
6. **Scope constraints** — a delegation can limit what the subordinate is authorized to do.

The convention builds exclusively on campfire protocol primitives (signed messages, vouches, provenance hops, threshold signatures). No protocol-level changes are required.

---

## 2. Scope

**In scope:**
- Delegation message format (grant, revoke, re-delegation)
- Chain verification algorithm (DAG traversal to Level 2+ root)
- Depth limits and their per-campfire configuration
- Revocation and cascade semantics
- Team sysops (M-of-N threshold signatures)
- Scope constraints on delegations (convention, campfire, time)
- Integration with `min_sysop_level` and the trust convention's safety envelope

**Not in scope:**
- Sysop provenance levels and verification (that's the sysop-provenance convention)
- The trust policy framework (that's the trust convention)
- Agent authorization within a campfire's membership model (that's the protocol)
- Behavioral reputation scoring (may compose with delegation depth in future work)

---

## 3. Dependencies

- Campfire Protocol Spec v0.3 (messages, tags, threshold signatures, campfire-key signatures)
- Sysop Provenance Convention v0.1 (provenance levels 0–3, attestation format, `min_sysop_level`)
- Trust Convention v0.2 (local trust model, safety envelope, federation tiers)
- Convention Extension Convention v0.1 (declaration format for convention operations)

---

## 4. Core Concepts

### 4.1 The Accountability DAG

Every agent key in the system exists in a delegation graph whose roots are human sysop keys. The graph is a DAG (directed acyclic graph) because:

- A sysop key can delegate to multiple agent keys (fan-out).
- An agent key can receive delegations from multiple sources (fan-in — useful for team sysops).
- Circular delegations are structurally invalid and MUST be rejected.

The graph is rooted at **Level 2+ sysop keys** — keys with attestations proving a human controls a real contact method. A delegation chain that terminates at a Level 0 or Level 1 key is an unrooted chain. Unrooted chains provide no accountability guarantees.

```
  [sysop-key-A]    [sysop-key-B]      ← Level 2+ human sysops (roots)
        │                │
        ├──→ [agent-1]   └──→ [agent-2]   ← First-hop delegations
        │         │
        └──→ [agent-3]   └──→ [agent-4]   ← Second-hop delegations
                  │
                  └──→ [agent-5]           ← Third-hop (depth 3)
```

A campfire that requires a delegation chain of depth ≤ 2 would accept agent-1 through agent-4 but reject agent-5.

### 4.2 What a Delegation Asserts

A delegation is a signed statement: "I, [delegator key], authorize [delegate key] to act on my behalf." The delegation:

- Binds the delegate key to the delegator key cryptographically.
- Optionally constrains the scope of that authority.
- Is anchored in time (issued at a specific timestamp, optionally expiring).
- Is revocable by the delegator.

A delegation does not transfer provenance level. An agent operating under a Level 3 sysop is not itself Level 3. It is an agent with a verifiable delegation chain to a Level 3 root. The distinction matters: the sysop is the accountable party; the agent is the acting party.

### 4.3 Re-delegation

A delegated agent key MAY issue further delegations to subordinate keys ("re-delegation"), subject to:

1. The delegator has re-delegation rights in their own delegation (the `allow_redelegation` field).
2. The resulting chain does not exceed the campfire's depth limit.
3. The subordinate's scope constraints are a subset of the delegator's scope constraints.

An agent that received a delegation WITHOUT `allow_redelegation: true` MUST NOT issue further delegations. Any delegations it issues are structurally invalid and MUST be rejected.

---

## 5. Delegation Message Format

A delegation is a signed campfire message. All delegation messages carry the convention tag `delegation:grant`, `delegation:revoke`, or `delegation:team`.

### 5.1 `delegation-grant`

Issued by a delegator (sysop or agent with re-delegation rights) to authorize a subordinate key.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `delegator_key` | key | yes | Public key of the delegator |
| `delegate_key` | key | yes | Public key of the subordinate being authorized |
| `issued_at` | timestamp | yes | When the delegation was issued |
| `expires_at` | timestamp | no | When the delegation expires. If absent, the delegation does not expire. |
| `allow_redelegation` | bool | no | Whether the delegate may issue further delegations. Default: `false`. |
| `scope` | object | no | Constraints on the delegate's authority (§6). If absent, full authority within campfire context. |
| `parent_delegation_id` | message_id | no | The message ID of the delegation that grants this delegator re-delegation rights. Omit for root (sysop-issued) delegations. |
| `nonce` | hex (16 bytes) | yes | Random value preventing replay of delegation messages. |

**Signing:** The message MUST be signed by the `delegator_key`. It SHOULD additionally be co-signed by the `delegate_key`, confirming acceptance. An unaccepted delegation (signed only by the delegator) is valid but MAY be treated as weaker evidence by local policy — the delegate could claim they did not accept the authority.

**Tags:** `delegation:grant`

**Antecedent (re-delegation only):** When `parent_delegation_id` is set, the message MUST reference the parent delegation as an antecedent (`--reply-to <parent_delegation_id>`). This allows chain traversal: given a delegation, follow antecedents to reach the root.

**Example (root delegation — sysop to agent):**

```json
{
  "convention": "sysop-delegation",
  "operation": "delegation-grant",
  "delegator_key": "iSnWl5chRz0e5wyBTScXtx4...",
  "delegate_key": "3Kf8xPqRvT2mYn7...",
  "issued_at": "2026-03-28T12:00:00Z",
  "expires_at": "2026-09-28T12:00:00Z",
  "allow_redelegation": true,
  "nonce": "a3f7c2d1e8b94f02"
}
```

**Example (re-delegation — agent to sub-agent, scope-constrained):**

```json
{
  "convention": "sysop-delegation",
  "operation": "delegation-grant",
  "delegator_key": "3Kf8xPqRvT2mYn7...",
  "delegate_key": "9mRz1NqPsVkLb4...",
  "issued_at": "2026-03-28T14:00:00Z",
  "allow_redelegation": false,
  "parent_delegation_id": "msg_01abc...",
  "scope": {
    "conventions": ["social-post-format", "agent-profile"],
    "campfires": ["cf://team-alpha.example.campfire.dev"],
    "not_after": "2026-06-28T00:00:00Z"
  },
  "nonce": "b9e4d7f2a1c53e08"
}
```

### 5.2 `delegation-revoke`

Issued by a delegator to revoke a prior delegation. Revocation is immediate upon receipt and propagates to all sub-delegations (see §8).

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `delegator_key` | key | yes | Public key of the delegator |
| `delegation_id` | message_id | yes | The `delegation-grant` message being revoked |
| `revoked_at` | timestamp | yes | When the revocation was issued |
| `reason` | string | no | Human-readable reason for revocation |
| `cascade` | bool | no | Whether to cascade revocation to sub-delegations. Default: `true`. Setting to `false` is valid for targeted revocation of a single delegation without affecting children. |

**Signing:** MUST be signed by the `delegator_key`. The runtime MUST verify that `delegator_key` matches the `delegator_key` field in the original `delegation-grant` message. A key cannot revoke delegations it did not issue.

**Exception — campfire owner override (cf-delegation 1.0):** The campfire's owner-of-record MAY revoke any delegation in a chain within that campfire, even delegations the owner did not directly issue, by omitting `delegator_key` and signing with the campfire's owner key. This is the emergency break for compromised interior nodes. The override is scoped strictly to the campfire where the owner holds owner-of-record status — an owner of campfire A cannot override-revoke in campfire B.

**Deprecation note:** The `sysop_override: true` field present in sysop-delegation v0.1 §5.2 is removed in cf-delegation 1.0. It is not a valid field. Any message presenting `sysop_override: true` as a revocation authority MUST be rejected. The former behavior (any Level 2+ sysop claiming override authority over any chain) was P3-leaking: it allowed global revocation through claimed root authority, bypassing campfire-scoped ownership. The owner-of-record constraint narrows this to within-campfire authority only.

**Tags:** `delegation:revoke`

**Antecedent:** MUST reference the original `delegation-grant` message.

### 5.3 `delegation-accept`

Optional explicit acceptance. The delegate co-signs a `delegation-grant` by referencing it.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `delegation_id` | message_id | yes | The `delegation-grant` message being accepted |
| `accepted_at` | timestamp | yes | When acceptance was issued |

**Signing:** MUST be signed by the `delegate_key` (the key being delegated to).

**Tags:** `delegation:accept`

**Antecedent:** MUST reference the original `delegation-grant` message.

If a delegation-grant is co-signed at issuance (delegator AND delegate both sign), a separate `delegation-accept` is not needed.

---

## 6. Scope Constraints

The `scope` field in a `delegation-grant` limits the delegate's authority. Scope is a JSON object with any combination of these constraint types. All constraints are AND-ed: the delegate must satisfy all specified constraints.

### 6.1 Convention Scope

```json
"scope": {
  "conventions": ["social-post-format", "agent-profile"]
}
```

The delegate is authorized to invoke only the listed conventions. Operations under unlisted conventions are rejected with a `scope_violation` error.

### 6.2 Campfire Scope

```json
"scope": {
  "campfires": [
    "cf://team-alpha.example.campfire.dev",
    "cf://team-beta.example.campfire.dev"
  ]
}
```

The delegate is authorized to act only in the listed campfires. Actions in unlisted campfires are out-of-scope.

### 6.3 Time Scope

```json
"scope": {
  "not_before": "2026-04-01T00:00:00Z",
  "not_after": "2026-06-30T23:59:59Z"
}
```

The delegation is only valid within the specified time window. `not_before` defers activation. `not_after` supplements or replaces the top-level `expires_at` field — the effective expiry is `min(expires_at, scope.not_after)`.

### 6.4 Operation Scope

```json
"scope": {
  "operations": ["social-post-format:post", "agent-profile:publish"]
}
```

Limits to specific convention operations (format: `convention:operation`). More precise than convention-level scope. If both `conventions` and `operations` are set, the delegate may invoke any operation under the listed conventions AND any specifically listed operation.

### 6.5 Scope Inheritance Rules

Re-delegation scope MUST be a subset of the delegator's own scope. A delegator cannot grant authority it does not have:

- If the delegator's scope lists `conventions: ["A", "B"]`, a re-delegation may list `conventions: ["A"]` or `conventions: ["B"]` or both — but NOT `conventions: ["A", "B", "C"]`.
- If the delegator has `not_after: "2026-06-30"`, a re-delegation cannot set `not_after: "2026-12-31"`.
- A delegator with `allow_redelegation: false` in its own delegation cannot issue any re-delegation.

The runtime MUST validate scope inheritance during chain verification. A chain with scope-violating re-delegations is invalid.

---

## 7. Chain Verification Algorithm

Given an agent key `K`, compute whether it has a valid delegation chain to a Level 2+ sysop.

### 7.1 Inputs

- `K`: the agent key to verify
- `depth_limit`: maximum delegation hops (from campfire configuration or local policy)
- `observed_messages`: the set of delegation messages the runtime has seen
- `provenance_store`: the runtime's local provenance store (from sysop-provenance convention)

### 7.2 Algorithm

```
function verify_chain(K, depth_limit, observed_messages, provenance_store):

  # Base case: K is itself a Level 2+ sysop key
  if provenance_level(K, provenance_store) >= 2:
    return ChainResult(valid=true, depth=0, root=K)

  # Find all delegation-grant messages where delegate_key == K
  grants = [m for m in observed_messages
            if m.operation == "delegation-grant"
            and m.delegate_key == K
            and not_revoked(m, observed_messages)
            and not_expired(m)
            and valid_signature(m)]

  if len(grants) == 0:
    return ChainResult(valid=false, reason="no_valid_delegation")

  # Explore each grant (multiple grants exist if K received authority from multiple sources)
  for grant in grants:

    # Depth limit check
    # The depth of this delegation is 1 + depth of the parent chain
    parent_depth_limit = depth_limit - 1
    if parent_depth_limit < 0:
      continue  # This path exceeds the depth limit

    # Re-delegation: verify parent chain
    if grant.parent_delegation_id is not None:
      # Validate that the grant's antecedent resolves to a real delegation-grant
      parent_grant = find_message(grant.parent_delegation_id, observed_messages)
      if parent_grant is None:
        continue  # Parent delegation not in local store — cannot verify

      # Verify parent granted re-delegation rights
      if not parent_grant.allow_redelegation:
        continue  # Re-delegation without permission — invalid

      # Verify scope inheritance
      if not scope_is_subset(grant.scope, parent_grant.scope):
        continue  # Scope violation — invalid chain

      # Recurse: verify the delegator's chain
      parent_result = verify_chain(grant.delegator_key,
                                   parent_depth_limit,
                                   observed_messages,
                                   provenance_store)
      if parent_result.valid:
        return ChainResult(valid=true,
                           depth=parent_result.depth + 1,
                           root=parent_result.root,
                           chain=[grant] + parent_result.chain)

    else:
      # Root delegation (no parent_delegation_id): delegator must be Level 2+
      if provenance_level(grant.delegator_key, provenance_store) >= 2:
        return ChainResult(valid=true,
                           depth=1,
                           root=grant.delegator_key,
                           chain=[grant])

  return ChainResult(valid=false, reason="no_valid_path_to_root")
```

### 7.3 Cycle Detection

The algorithm MUST detect and reject circular delegations. A delegation is circular if, during chain traversal, the same key appears twice in the chain being constructed. Implementations SHOULD track the set of keys seen during a single traversal and abort if a key repeats.

### 7.4 Multiple Valid Paths

An agent key may have multiple valid delegation paths (e.g., delegated by two different sysops). Any single valid path to a Level 2+ root is sufficient. The runtime SHOULD prefer the shortest (shallowest) valid path for provenance display, but MAY accept any valid path.

### 7.5 Missing Messages

Chain verification depends on the runtime's local store of observed delegation messages. A chain that terminates in a delegation whose parent is not in the local store returns `no_valid_path_to_root` — not an error, but an unverifiable state. Runtimes SHOULD:

1. Attempt to fetch missing delegation messages from the campfire where the delegation was published.
2. If a campfire ID is resolvable (via the naming convention), attempt resolution.
3. If the message cannot be retrieved, treat the chain as unverifiable.

The convention does not mandate fetching strategy — local policy governs whether unverifiable chains are treated as Level 0 or rejected outright.

---

## 8. Revocation and Cascade

Revocation is a core operation of the delegation model. Because delegations chain, a single revocation may invalidate many downstream keys.

### 8.1 Revocation Semantics

A `delegation-revoke` message invalidates the named delegation and (by default) all delegations that trace through it. Concretely:

- The revoked delegation-grant message is marked invalid from `revoked_at` onward.
- Any delegation whose `parent_delegation_id` traces (directly or transitively) to the revoked delegation is also invalid.
- Any agent key whose only valid delegation path ran through the revoked delegation is now unrooted.

### 8.2 Cascade with `cascade: false`

Setting `cascade: false` in a `delegation-revoke` message revokes only the named delegation, not its children. Children become "orphaned" — their parent is revoked, but the runtime may grant them a grace period to re-anchor (re-receive a delegation from another valid delegator) before treating them as fully revoked.

Grace periods are local policy. The convention recommends a default grace period of 24 hours for orphaned delegations — long enough for re-anchoring in normal operation, short enough that a compromised key cannot continue operating indefinitely.

### 8.3 Revocation Discovery

Revocations are campfire messages. A runtime learns about revocations by reading the campfires it monitors. This creates a window between when a revocation is issued and when a given runtime sees it.

Runtimes SHOULD:
- Re-check revocation state before any high-stakes operation (core peering, registry promotion).
- Subscribe to revocation messages from campfires that issued delegations they trust.
- Treat delegations as potentially revoked if the issuing campfire has been unreachable for longer than a configurable staleness threshold.

For high-stakes campfires, sysops MAY configure `revocation_check_required: true`, which requires the runtime to confirm revocation state is fresh before accepting a delegated key.

### 8.4 Campfire Owner Override Revocation (cf-delegation 1.0)

The campfire's owner-of-record MAY issue an override revocation for any delegation within that campfire. This is the emergency break: if an intermediate agent key is compromised and that key refuses to issue its own revocation, the campfire owner can bypass it.

**Scope constraint:** Override revocation is scoped to the campfire where the revoker holds owner-of-record status. A key that is the owner-of-record for campfire A CANNOT override-revoke delegations in campfire B. This is the critical narrowing from the v0.1 draft: the old `sysop_override: true` mechanism allowed any Level 2+ sysop to claim global override authority — a P3-leaking property. The cf-delegation 1.0 constraint ensures override authority is campfire-local.

Override revocation MUST be signed by the campfire's owner key. The runtime MUST verify the revoking key matches the campfire's owner-of-record before applying the override. A Level 2+ key that is NOT the campfire's owner cannot issue override revocations, regardless of its provenance level.

**Removed: `sysop_override: true`.** The `sysop_override: true` field from sysop-delegation v0.1 §5.2 is not recognized in cf-delegation 1.0. Runtime implementations MUST NOT accept messages with `sysop_override: true` as an override-revoke authority. See §5.2 deprecation note.

---

## 9. Depth Limits

### 9.1 What Depth Means

Depth is the number of delegation hops between the acting key and its Level 2+ root:

| Depth | Meaning |
|-------|---------|
| 0 | The key IS the Level 2+ sysop key |
| 1 | Directly delegated from a Level 2+ sysop |
| 2 | Delegated from a key that was delegated from a Level 2+ sysop |
| N | N hops from a Level 2+ root |

### 9.2 Default Depth Limit

The convention-wide default depth limit is **3**. This allows:

- Depth 1: primary agent fleet (sysop's direct subordinates)
- Depth 2: task-specialized sub-agents spawned by primary agents
- Depth 3: ephemeral workers spawned by task agents for a single operation

Depth 3 is sufficient for most practical deployments. Deeper chains fragment accountability too far from the human root — at depth 7, a misbehaving agent is six hops removed from any human who can be contacted.

### 9.3 Per-Campfire Configuration

Campfire sysops MAY configure a depth limit in the campfire's convention declaration:

```json
{
  "convention": "sysop-delegation",
  "config": {
    "max_delegation_depth": 2,
    "orphan_grace_period_hours": 24,
    "revocation_check_required": false
  }
}
```

| Config key | Type | Default | Description |
|-----------|------|---------|-------------|
| `max_delegation_depth` | int | 3 | Maximum hops from a Level 2+ root. Range: 1–10. |
| `orphan_grace_period_hours` | int | 24 | Hours before an orphaned delegation (cascade: false revocation) is fully invalidated. |
| `revocation_check_required` | bool | false | Require fresh revocation-state confirmation before accepting delegated keys in this campfire. |
| `require_delegation_acceptance` | bool | false | Require `delegation-accept` co-signature (or inline co-signing) before treating a delegation as valid. |

Depth limit of 1 means only keys directly delegated by a Level 2+ sysop may act in this campfire. This is the tightest useful limit — appropriate for high-stakes campfires (core peering, registry operations) where intermediate agents should not be able to delegate further.

### 9.4 Depth Limit Enforcement

The runtime checks depth during chain verification (§7) and at convention operation dispatch. If the chain's depth exceeds the configured limit, the operation is rejected with a `delegation_depth_exceeded` error.

---

## 10. Team Sysops (M-of-N)

A single sysop key creates single points of failure and concentration of authority. Organizations need multi-human accountability: decisions require M of N designated humans to co-sign.

### 10.1 Team Sysop Key

A team sysop key is a campfire threshold key — a public key whose signing threshold requires M-of-N private keyshares held by individual team members. This uses the campfire protocol's existing threshold signature primitives (Protocol Spec v0.3 §7).

Creating a team sysop key:

```bash
cf team-key create \
  --members alice:key1 bob:key2 carol:key3 \
  --threshold 2 \
  --label "ops-team"
```

This generates a team public key and distributes keyshares to Alice, Bob, and Carol. Any 2-of-3 can sign on behalf of the team.

### 10.2 Verifying a Team Sysop

A team sysop key can be verified (Level 2) using the sysop-provenance convention with one extension: the `sysop-verify` response MUST carry co-signatures from at least M team members. This proves that M humans — not just one — control the designated contact method and hold keyshares.

The `proof_type` for team verification MUST be one of: `hardware`, `totp` (TOTP-backed keyshare), or a new type `threshold-m-of-n` which the sysop-provenance convention SHOULD adopt in its next revision.

The verifier specifies a minimum threshold acceptance in their challenge:

```json
{
  "convention": "sysop-delegation",
  "operation": "team-verify-challenge",
  "target_team_key": "...",
  "min_cosigners": 2,
  "nonce": "..."
}
```

### 10.3 Team Delegation

A team key delegates the same way an individual key does: by issuing a `delegation-grant` signed by the team key (which requires M-of-N co-signatures from team members to produce).

```json
{
  "convention": "sysop-delegation",
  "operation": "delegation-grant",
  "delegator_key": "<team-public-key>",
  "delegate_key": "<agent-key>",
  "issued_at": "...",
  "allow_redelegation": true,
  "nonce": "..."
}
```

Chain verification is identical: a chain is valid if it traces to a Level 2+ key. A Level 2+ team key satisfies the root requirement the same way an individual Level 2 key does.

### 10.4 Team Revocation

Revocations issued by the team key also require M-of-N co-signatures. This prevents a single compromised team member from revoking all delegations unilaterally.

Exception: campfire sysops MAY configure `emergency_revocation_threshold: 1` for their team keys, allowing a single team member to issue emergency revocations. This reduces the security guarantee of the threshold (any team member can revoke) but enables faster incident response. This configuration MUST be declared explicitly in the campfire's convention config.

---

## 11. Integration with Sysop Provenance

This convention builds directly on the sysop-provenance convention. Key integration points:

### 11.1 Chain Root Requirement

The chain verification algorithm (§7) requires a Level 2+ sysop at the root. This directly uses the provenance level computation defined in sysop-provenance §8.2. A chain rooted at a Level 0 or Level 1 key is unrooted — it provides no accountability guarantees.

### 11.2 `min_sysop_level` with Delegation

The `min_sysop_level` field in convention operation declarations controls the minimum provenance level for the ACTING key's sysop. When delegation chains are in use, runtimes extend this check:

- For a directly-acting sysop key: check provenance level as defined in sysop-provenance §8.1.
- For a delegated agent key: check that a valid delegation chain exists to a root key at `min_sysop_level`.

A new enforcement field, `min_chain_root_level`, MAY be added to convention declarations to explicitly distinguish between "the acting key must be Level 2+" (direct operation) and "the acting key must have a chain to a Level 2+ root" (delegated operation). Default behavior when only `min_sysop_level` is set: require the chain root to meet `min_sysop_level`.

### 11.3 Provenance Display

When displaying an agent's provenance, runtimes SHOULD show the delegation chain alongside the provenance level:

```
key: 9mRz1NqPsVkLb4...
chain: depth=2, root=iSnWl5chRz0e5wyBTScXtx4... (Level 3, fresh 4h ago)
scope: conventions=[social-post-format, agent-profile]
```

This gives sysops and users a full picture: not just "this key has a delegation" but "this delegation chain is rooted at a human who was verified 4 hours ago."

### 11.4 Provenance Freshness and Delegation

Level 3 (Present) is a property of a sysop attestation's freshness. When a sysop's Level 3 attestation expires (falls outside the freshness window), their root status decays to Level 2. This decay cascades logically to all chains rooted at that key — a chain rooted at a now-Level-2 key is a Level-2 chain, not Level 3.

Runtimes MUST recompute delegation chain validity as attestation freshness changes. A campfire that requires `min_chain_root_level: 3` will reject an agent whose root sysop's attestation has gone stale.

---

## 12. Integration with Trust Convention

### 12.1 Safety Envelope

The trust convention (v0.2) wraps content in a safety envelope before presenting it to agents. The envelope includes a `sysop_provenance` field. This field SHOULD be extended to include delegation chain information:

```json
{
  "sysop_provenance": 2,
  "delegation_chain": {
    "valid": true,
    "depth": 2,
    "root_key": "iSnWl5chRz0e5wyBTScXtx4...",
    "root_level": 3,
    "root_attested_at": "2026-03-28T08:00:00Z"
  }
}
```

If the acting key has no valid delegation chain, `delegation_chain.valid` is `false` and `depth` is absent. If the acting key IS the sysop key (depth 0), `depth: 0` and `root_key` equals the acting key.

### 12.2 Federation Tier Delegation

The trust convention defines federation peering tiers gated by sysop provenance. Delegation chains integrate naturally:

| Tier | Requirement |
|------|------------|
| Core peering | Chain root Level 2+, chain depth ≤ 1 |
| Standard peering | Chain root Level 1+, chain depth ≤ 2 |
| Leaf peering | Chain root Level 0+ (any key, no requirement), chain depth ≤ 3 |

Core peering remains restrictive: depth ≤ 1 means only a direct sysop delegation can establish a core peer. An agent six hops from its sysop cannot establish core peering, regardless of the root's provenance level.

---

## 13. Convention Operations

Four convention operations constitute the full delegation lifecycle.

### 13.1 delegation-grant

See §5.1. Fields and signing requirements defined there.

**Tags:** `delegation:grant`
**Rate limit:** 100/sender/hour for root delegations; 500/sender/hour for re-delegations (agents may spawn sub-agents rapidly).
**Signing:** MUST be signed by `delegator_key`. SHOULD be co-signed by `delegate_key` or followed by a `delegation-accept`.

### 13.2 delegation-accept

See §5.3. Explicit acceptance of a delegation by the delegate.

**Tags:** `delegation:accept`
**Antecedent:** `exactly_one(delegation-grant)`
**Signing:** MUST be signed by the key named in the delegation-grant's `delegate_key` field.

### 13.3 delegation-revoke

See §5.2. Revokes a delegation and optionally cascades.

**Tags:** `delegation:revoke`
**Antecedent:** `exactly_one(delegation-grant)`
**Signing:** MUST be signed by the key matching the delegation's `delegator_key` field, OR by the campfire's owner-of-record key (owner override, §8.4). `sysop_override: true` is NOT a valid signing authority in cf-delegation 1.0 — messages presenting it MUST be rejected.
**Rate limit:** Unlimited. Emergency revocation must not be throttled.

### 13.4 delegation-query

A runtime MAY publish a delegation-query message to request that a key publish its delegation chain. Used when a runtime encounters a key without a locally-visible delegation chain.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `target_key` | key | yes | Key whose delegation chain is being requested |
| `requesting_key` | key | yes | Key making the request (for routing the response) |
| `callback_campfire` | campfire | yes | Where to post the delegation-chain response |

**Tags:** `delegation:query`
**Rate limit:** 10/target_key/hour.

A key receiving a delegation-query SHOULD respond with its `delegation-grant` messages (or a pointer to the campfire where they are published). The query/response is advisory — a key is under no obligation to respond.

---

## 14. UX: Verbs and Nouns

Nobody thinks about DAG traversal or antecedent resolution. The runtime does it.

```bash
cf delegate <key-or-name>                          # delegate authority to a key
cf delegate <key-or-name> --scope conventions=X,Y  # with scope constraints
cf delegate <key-or-name> --allow-redelegation      # with re-delegation rights
cf delegate <key-or-name> --expires 90d             # time-bounded
cf delegate revoke <key-or-name>                    # revoke a delegation
cf delegate show <key-or-name>                      # show delegation chain for a key
cf delegate tree                                    # show full delegation tree rooted at my key
```

### 14.1 `cf delegate`

**What you say:** `cf delegate myagent --expires 30d --scope conventions=social-post-format`

**What happens:**
1. Looks up myagent's public key (from agent-profile or local key store)
2. Generates a `delegation-grant` message with the specified scope and expiry
3. Signs with the sysop's key
4. Publishes to the sysop's home campfire
5. Reports: "Delegation granted. myagent may invoke social-post-format for 30 days."

### 14.2 `cf delegate revoke`

**What you say:** `cf delegate revoke myagent`

**What happens:**
1. Looks up the most recent `delegation-grant` issued to myagent
2. Issues a `delegation-revoke` message (cascade: true by default)
3. Reports: "Delegation revoked. 3 sub-delegations cascade-revoked."

### 14.3 `cf delegate show`

**What you say:** `cf delegate show someagent`

**What happens:**
1. Traverses the delegation DAG from someagent's key to the root
2. Displays: chain depth, root key, root provenance level, scope, expiry
3. Flags any issues: revoked links, expired delegations, missing messages

---

## 15. Reference Implementation

### 15.1 What to Build

1. **Delegation store** (Go, `pkg/delegation/`)
   - Store/query delegation-grant and delegation-revoke messages
   - Index by `delegate_key` and `delegator_key` for efficient chain traversal
   - ~300 LOC

2. **Chain verifier** (Go, `pkg/delegation/`)
   - Implements the §7.2 algorithm
   - Cycle detection, scope inheritance validation, depth limit enforcement
   - ~250 LOC

3. **Revocation cascade** (Go, `pkg/delegation/`)
   - Given a revoked delegation ID, enumerate all transitively invalidated delegations
   - ~150 LOC

4. **Safety envelope extension** (Go, `pkg/trust/`)
   - Add `delegation_chain` field to existing safety envelope construction
   - ~100 LOC

5. **`cf delegate` commands** (Go, `cmd/cf/`)
   - Grant, revoke, show, tree
   - ~200 LOC

6. **Threshold key support** (Go, `pkg/delegation/`)
   - Wrap existing threshold signature primitives for team sysop delegation
   - ~100 LOC

**Total:** ~1100 LOC, pure Go, no new dependencies beyond threshold signature primitives already in the campfire protocol.

### 15.2 Integration Points

- `pkg/provenance/`: chain verifier calls provenance store for root-level checks
- `pkg/trust/`: safety envelope includes delegation_chain in content wrapping
- `pkg/convention/`: operation dispatch checks chain validity alongside `min_sysop_level`
- `cmd/cf-mcp/`: delegation chain in tool responses; delegation operations exposed as MCP tools
- `cmd/cf/`: `cf delegate` and `cf delegate revoke`, `cf delegate show`

---

## 16. Security Considerations

### 16.1 Long Chain Accountability Dilution

Every delegation hop dilutes accountability. A misbehaving agent at depth 5 requires traversing 5 links to reach a contactable human. By default depth limit of 3 (§9.2), chains are shallow enough that accountability remains practical.

**Mitigation:** Campfires with high-stakes operations SHOULD use `max_delegation_depth: 1`. The depth limit is the primary control.

### 16.2 Delegation Laundering

A sysop grants a trusted agent full re-delegation rights. That agent creates sub-delegations to arbitrary keys, laundering accountability: the sub-keys appear to trace back to a Level 2 sysop, but the sysop had no knowledge of them.

**Mitigations:**
- `allow_redelegation` is `false` by default — re-delegation is opt-in.
- Scope inheritance (§6.5) bounds the blast radius — sub-delegations cannot exceed the parent's scope.
- The delegation tree is auditable: `cf delegate tree` shows the sysop all delegations issued under their key.
- Campfire depth limits prevent deep chains regardless of re-delegation permission grants.

### 16.3 Revocation Race Condition

A revocation is issued but not yet observed by all runtimes. The revoked key continues to act in campfires that haven't seen the revocation.

**Mitigations:**
- `revocation_check_required: true` per-campfire requires fresh revocation state confirmation.
- The staleness threshold (§8.3) gives sysops control over how long a stale view is acceptable.
- Level 3 freshness requirements on the chain root mean that even if revocation hasn't propagated, the root's freshness window provides a natural check-in interval.

### 16.4 Scope Creep via Re-delegation

A delegator grants scope `conventions: ["A"]`. The delegate issues a re-delegation claiming scope `conventions: ["A", "B"]`, attempting to expand authority.

**Mitigation:** Scope inheritance validation (§6.5) is enforced during chain verification. Any re-delegation whose scope exceeds the parent's scope causes the entire chain to be marked invalid. The validation is in the runtime, not in the delegator — the delegator cannot be deceived into granting a wider scope.

### 16.5 Compromise of a Chain Interior Node

An agent key at depth 2 is compromised. The attacker uses it to issue delegations to their own keys, extending the tree with malicious sub-agents, all with valid chains to the sysop root.

**Mitigations:**
- The campfire owner override revocation mechanism (§8.4) allows the campfire's owner-of-record to revoke any delegation in the tree within that campfire, bypassing the compromised node. This authority is scoped to the campfire — the owner cannot override-revoke in other campfires.
- `allow_redelegation: false` (the default) prevents the compromised key from extending the chain further.
- Auditing via `cf delegate tree` makes the unexpected sub-delegations visible to the sysop.

### 16.6 Team Sysop Keyshare Compromise

One team member's keyshare is compromised. With threshold M-of-N, a single compromise is insufficient to act unilaterally. However, if M keyshares are compromised, the team sysop key is fully compromised.

**Mitigations:**
- Keyshare rotation: team members can rotate individual keyshares without changing the team public key (threshold key infrastructure dependent; may require protocol support).
- For emergency revocation, `emergency_revocation_threshold: 1` (§10.4) allows any remaining team member to begin revoking malicious delegations even if M keyshares are compromised.
- Teams SHOULD use hardware-backed keyshares (FIDO keys) to raise the bar for compromise.

### 16.7 Circular Delegation Denial-of-Service

An attacker publishes delegation messages that form a cycle, causing chain verification to loop indefinitely.

**Mitigation:** Cycle detection (§7.3) is mandatory. The algorithm tracks keys seen during traversal and aborts with `circular_delegation` on a repeat. This converts an infinite loop into a bounded O(N) traversal where N is the chain length.

---

## 17. Open Questions

1. **Delegation message routing.** Where should delegation-grant messages be published? The delegator's home campfire is natural but may not be visible to all verifiers. Should there be a well-known delegation campfire per sysop? Or should the delegate publish a pointer to where its delegation lives? The `delegation-query` operation (§13.4) is a workaround, but a publication convention would be cleaner.

2. **Partial scope verification.** The current model requires the verifier to have ALL delegation messages in the chain. If any message is missing, the chain is unverifiable. Should there be a proof format (similar to X.509 certificate chains) where the acting key includes its delegation chain inline in its messages, removing the need for the verifier to fetch from campfires?

3. **Delegation expiry and re-issuance.** When a delegation expires, the delegating agent must re-issue it. For long-lived agents, this creates operational overhead. Should there be an auto-renewal mechanism (the delegate requests renewal, the delegator's runtime auto-signs if within policy)? Or is manual re-issuance the correct security posture?

4. **Sysop-provenance `min_chain_root_level`.** The current `min_sysop_level` field in convention declarations does not distinguish between the acting key's level and the chain root's level. Should `min_chain_root_level` be standardized as a separate field in the convention extension convention? Or should `min_sysop_level` semantics be extended to "the root of the chain must meet this level"?

5. **Cross-chain delegation.** Can a delegated key from sysop A issue a delegation that is also co-rooted in sysop B? This would allow two organizations to jointly authorize an agent. The current model handles this via multiple valid paths (§7.4) — an agent may have chains to multiple roots. Is this sufficient, or should there be an explicit joint-delegation construct?

6. **Delegation in offline/partitioned environments.** An agent that received a delegation operates in a partitioned network without access to its delegator's campfire. Revocations cannot propagate; the agent's chain cannot be verified by new peers. What is the correct behavior? Operate until the partition resolves (optimistic), halt (pessimistic), or present a "stale chain" warning and let local policy decide?

7. **Depth limit standardization.** The default of 3 is proposed here. Should the sysop-provenance convention adopt this as a standard reference? Or should depth limits remain purely local policy without a recommended default? A standard default increases interoperability; purely local policy maximizes sysop sovereignty.
