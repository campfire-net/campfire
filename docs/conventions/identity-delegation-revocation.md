# Identity Delegation Revocation v0.1

**Status:** draft
**Date:** 2026-04-10
**Item:** campfire-ab7
**Depends on:** campfire-79e (grant convention — approved), campfire-ead (trust resolution — approved)
**Input:** `resonant/docs/specs/identity-tree-constraints.md` (especially C5), `campfire/docs/identity-audit.md` §5

## 1. Purpose

Define how a parent identity revokes a previously-issued grant, such that the trust-resolution algorithm from campfire-ead correctly treats the grant as invalid. One convention operation, one additional query in the resolution walk, no protocol changes, no new infrastructure.

## 2. Scope

**In:**
- One convention operation: `identity-delegation:revoke`
- One tag: `identity:revoked` (reuses the existing tag from `pkg/convention/identity.go:25`)
- Extension to the trust-resolution algorithm in identity-v0.2-trust-resolution.md
- Subtree cascade semantics (implicit in the walk — no new code)
- Cross-campfire posting guidance for `rd kill` / `cf revoke` tools

**Not in:**
- Any change to `pkg/message`, `pkg/protocol`, or `pkg/transport`
- A revocation store. Revocations are ordinary campfire messages in the log, queried the same way grants are. The log IS the store.
- A "validation layer" in protocol read/send paths. Revocation enforcement happens in the trust-resolution algorithm, which is convention-layer (ead). Protocol paths don't change.
- Subscription or push infrastructure for revocations. Nodes learn about revocations by reading the campfire log, same as grants.

## 3. Revoke message

**Tag:** `identity:revoked`
**Signing:** `member_key` (the parent identity that originally signed the grant being revoked)
**Antecedents:** `none` (revokes all grants from this signer to this child in this campfire)

**Payload (JSON):**

```json
{
  "child_pubkey": "<64-char hex>"
}
```

| Field | Required | Type | Description |
|---|---|---|---|
| `child_pubkey` | yes | hex string | ed25519 public key whose grants from this signer are revoked |

One field. The signer is the parent (implicit in `msg.Sender`). The scope is "all grants from this parent to this child in this campfire."

**Semantic meaning.** A revoke message is a signed statement by the parent: "I no longer vouch for `child_pubkey` in this campfire." All grants from this parent to this child that predate the revoke message become invalid. Grants from OTHER parents to the same child are unaffected — each parent can only revoke their own grants.

**Re-granting after revocation.** A parent can post a new `identity:granted` message for the same child after revoking. The trust resolver evaluates grants by timestamp relative to revocations: a grant posted AFTER a revoke is valid; a grant posted BEFORE a revoke is not. This enables key rotation and grant renewal without ceremony.

## 4. Trust-resolution extension

The ead trust-resolution algorithm gains one additional check per hop. In the `find_most_recent_grant` step:

```
find_valid_grant(C, child_pubkey):
    grants = all messages in C tagged "identity:granted"
             where payload.child_pubkey == child_pubkey
             ordered by msg.Timestamp descending

    for grant in grants:
        if not validates(grant, C):
            continue

        parent = grant.Sender

        revokes = all messages in C tagged "identity:revoked"
                  where payload.child_pubkey == child_pubkey
                  AND msg.Sender == parent
                  AND msg.Timestamp > grant.Timestamp

        if len(revokes) > 0:
            continue    # this grant has been revoked, try an older one

        return grant    # valid and not revoked

    return nil          # no valid unrevoked grant found
```

This replaces `find_most_recent_grant` in ead's §4 algorithm. Everything else in the walk is unchanged.

**Cost.** One additional log query per hop: filter `identity:revoked` by child_pubkey + Sender + timestamp. Same cost class as the existing grant lookup — a cheap local-store read.

## 5. Subtree cascade

Subtree cascade is **implicit** in the trust-resolution walk. No new code.

When baron revokes machine@surface's grant, any session whose trust chain passes through machine@surface fails resolution at that hop — the walk finds machine@surface's grant from baron, discovers the revoke, skips the grant, finds no other valid grant for machine@surface, and returns `DeadEnd`. The session's own grant from machine@surface is still technically present in the log, but the walk never reaches it because the walk fails one hop earlier.

Post one revoke at any node in the tree; everything below it fails trust resolution automatically.

**Multi-parent keys.** If a child key has valid grants from two different parents, revoking from one parent still leaves the other path intact. The child remains trusted via the unrevoked path. To kill a child completely, revoke from every parent. This is correct behavior — one parent cannot unilaterally kill authority granted by another parent.

## 6. Cross-campfire posting

Revocation is per-campfire (like everything else in the convention layer — no cross-campfire lookups). A revoke message posted in campfire A has no effect on trust resolution in campfire B.

**Tool responsibility.** The user command (`rd kill <session>` or `cf revoke <subtree>`) is responsible for posting revoke messages to all campfires where the child has grants. The tool:

1. Queries the child's grant history by reading `identity:granted` messages across the operator's campfires.
2. Posts `identity:revoked` to each campfire where a grant exists.
3. Reports which campfires were revoked and which were unreachable (pending retry).

The convention does not prescribe how the tool discovers the relevant campfires — that's a tool-level concern. The convention only defines the single-campfire revoke semantics.

**C5 compliance.** The constraints spec says: "Revocation is one command... propagates automatically via campfire messages." Under this design: `rd kill <session>` is the one command. The tool posts multiple revoke messages (one per campfire), and each campfire's trust resolver picks them up on the next read. The user never types multiple commands or names specific campfires. C5 satisfied.

## 7. Validation rules

Any verifier that reads a revoke message from a campfire log MUST enforce:

1. **Message signature verifies** under `msg.Sender`. (Standard.)
2. **`payload.child_pubkey` is a valid 64-char hex ed25519 public key.** Malformed revokes are ignored.
3. **`msg.Sender` has previously signed an `identity:granted` message for this child in this campfire.** A revoke from a parent who never issued a grant is a no-op (not an error — the revoke is accepted into the log but has no effect on trust resolution).

Notably: there is no `campfire_id` field on the revoke payload (unlike grants). The revoke is always scoped to the campfire it's posted in — there's no replay concern because a revoke from parent P for child C in campfire A only affects grants from P to C in A. Replaying it in B has no effect unless P also granted C in B. This is safe by construction.

## 8. Audit-trail preservation

Per identity-tree-constraints.md §C5: "messages already accepted before the revoke remain in history (audit trail preservation)."

Satisfied by construction. The campfire log is append-only. Neither grants nor revokes are ever deleted. A message that was "trusted" before the revoke is still in the log; its trust status changes to `DeadEnd` when re-evaluated by a trust resolver that has read the revoke. Audit tools (`rd show --audit`, dashboards) can render the full history including the transition from trusted to revoked.

## 9. Security properties

- **Revoke forgery** requires the parent's private key. Only the parent can revoke its own grants.
- **Unauthorized subtree kill** by a non-ancestor is impossible — the trust-resolution walk only consults revokes from the specific parent at each hop. An unrelated key posting `identity:revoked` for a child it never granted has no effect (rule 3).
- **Revoke suppression** (an attacker prevents the revoke message from reaching the log) is bounded by the grant's TTL — even if the revoke is suppressed, the grant expires naturally within 7 days (79e's ceiling). The attacker gains at most one TTL period.
- **Re-grant after revoke** is allowed and is the intended way to rotate keys or restore access. The convention does not prevent it.
- **Revocation of a trust anchor** (a key in `[identity.trust] anchors`) is NOT handled by this convention — trust anchors are trusted by config, not by grants. To "revoke" a trust anchor, remove it from the config. This is an out-of-band operation.

## 10. Ceremony audit checklist

| # | Question | Answer |
|---|---|---|
| 1 | Does the user type any new command? | One: `rd kill <session>` (already listed in the constraints spec as the target UX). No additional commands. |
| 2 | Does `rd init` on machine-2 require any new flag or step? | No. |
| 3 | Scenario where user must manually revoke a grant? | Only the explicit `rd kill` / `cf revoke` escape hatches. Normal lifecycle is handled by `expires_at`. |
| 4 | Any failure mode "start blocked, please do X"? | No. Revoke posting is fire-and-forget; unreachable campfires are retried by the tool. |
| 5 | Any command fails when network is unreachable? | `rd kill` may fail to post to remote campfires; it reports which were unreachable and retries. The user is not blocked — local campfires are revoked immediately. |
| 6 | Steps to onboard new machine? | Unchanged. |

All six pass.

## 11. Conformance

A call site is conformant if:

- Every revoke message it posts has tag `identity:revoked`, signed by a valid `member_key` of the target campfire, with a payload matching §3.
- Its trust resolution implementation extends `find_most_recent_grant` with the revoke check described in §4.
- It never deletes grant or revoke messages from the log.

No required Go API. A shell script that posts a correctly-formed revoke via `cf` CLI is conformant.

## 12. Open questions

### Q1. Should `identity:revoked` support revoking a specific grant (by antecedent) as well as all-grants-from-parent?

**My call:** v0.1 revokes all grants from the signer to the child. Narrower revocation (per-grant) can be added by introducing an optional `grant_msg_id` field. Not needed for the current multi-machine/agent/service use case where "kill the session" means "revoke everything from the parent to that key."

### Q2. Should the revoke message have an `expires_at` field (temporary revocation)?

**My call:** no. Revocation is permanent until a re-grant is posted. Temporary revocation is just "revoke now, re-grant later."

Both questions have defaults. No protocol changes, no store, no infrastructure. One operation, one query, one doc.

## 13. Handoff

1. **Declaration:** `pkg/convention/declarations/identity-delegation-revoke.json`.
2. **Trust-resolution update:** modify the `find_valid_grant` function documented in identity-v0.2-trust-resolution.md §4 to include the revoke check per §4 above.
3. **`rd kill` command:** implement in the ready/rd repo (or campfire CLI, depending on where session management lives) as a tool that enumerates a child's campfires and posts revokes.
4. **Tests:** trust-resolve a sender whose grant has been revoked → returns DeadEnd. Trust-resolve a sender re-granted after revoke → returns Resolved. Subtree cascade: revoke grandparent, trust-resolve grandchild → DeadEnd.
