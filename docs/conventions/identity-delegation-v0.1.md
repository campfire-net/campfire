# Identity Delegation Convention v0.1

**Status:** approved (campfire-79e)
**Date:** 2026-04-10
**Item:** campfire-79e
**Sibling items:** campfire-ead (trust-resolution read pattern), campfire-ab7 (revocation subsystem)
**Input:** `resonant/docs/specs/identity-tree-constraints.md`, `campfire/docs/identity-audit.md`

## 1. Purpose

Define the wire format and semantics of a delegation grant — a signed convention message by which one identity asserts that another identity may act on its behalf in a specific campfire, until a specific time. This is the minimum primitive needed to distinguish "baron acting via session A on surface" from "baron acting via the cf-mcp service" from "baron acting directly" — all of which currently sign as the same root key.

## 2. Scope

**In:**
- One convention operation: `identity-delegation:grant`
- One tag: `identity:granted`
- One declaration JSON with payload shape and validation rules

**Not in:**
- Trust resolution algorithm (campfire-ead — convention-layer read pattern)
- Revocation enforcement (campfire-ab7 — convention-layer validation layer + store)
- Per-actor keypair minting at call sites (harness, cf-mcp, dontguess, forge — that's follow-up implementation work, one item per call site)
- Client-side Go helpers for posting grants. Any call site that can construct a correctly-formed convention message has conformed to this convention; there is no mandatory API.
- Session-coordination tags (`session:alive` etc.). Dropped — sibling discovery is not a delegation concern.

## 3. Grant message

**Tag:** `identity:granted`
**Signing:** `member_key` (the parent identity, which must be a member of the target campfire)
**Antecedents:** `none`

**Payload (JSON):**

```json
{
  "child_pubkey": "<64-char hex>",
  "campfire_id":  "<64-char hex>",
  "expires_at":   1744286400
}
```

| Field | Required | Type | Description |
|---|---|---|---|
| `child_pubkey` | yes | hex string | ed25519 public key being granted authority |
| `campfire_id` | yes | hex string | the campfire ID this grant is valid in |
| `expires_at` | yes | int | unix seconds absolute expiry |

No scope, no display name, no issued_at, no ttl_seconds, no acting_as. A grant is the minimum assertion: "I (the signer) say that `child_pubkey` may act on my behalf in `campfire_id` until `expires_at`."

**Semantic meaning.** A grant is a signed statement by the parent (the message's `Sender`) that the child key is authorized to speak for the parent in the named campfire until the named expiry. It does not grant any specific capability; it grants attribution. Any downstream convention that wants to enforce scoped capabilities on the child's actions does so via its own rules, reading the grant chain via the trust-resolution pattern (campfire-ead).

## 4. Validation rules

Any verifier that reads a grant message from a campfire log MUST enforce:

1. **Message signature verifies** under `msg.Sender` — the parent's ed25519 public key. (Standard `pkg/message.Message.VerifySignature()`.)
2. **`payload.campfire_id` equals the ID of the campfire the message was read from.** Rejects cross-campfire replay of signed grant messages.
3. **`payload.expires_at > now - 60`.** 60-second clock-skew slack.
4. **`payload.expires_at ≤ msg.Timestamp/1e9 + 7*86400`.** Hard ceiling prevents a compromised parent from issuing decade-long grants.
5. **Grant is not revoked.** Convention-layer check; the actual revocation store lives in campfire-ab7. Verifiers that predate ab7 may skip this rule (fail-open) but must be retrofitted once ab7 lands.

A verifier that finds any of these rules violated treats the grant as non-existent for trust-resolution purposes.

## 5. Security properties

- **Forgery requires the parent's private key.** Standard ed25519 signature security.
- **Cross-campfire replay** is closed by rule 2.
- **Decade-long grants from a compromised parent** are closed by rule 4.
- **Revocation** is deferred to ab7.
- **Parent compromise** is out of scope; this convention does not prevent a compromised parent from issuing arbitrary grants until the parent key itself is revoked.
- **The convention does not read any local config file**, so there is no local-config-tamper vector at this layer. Call sites that choose when to post grants have their own threat models.

## 6. Ceremony audit checklist

| # | Question | Answer |
|---|---|---|
| 1 | Does the user type any new command? | No. `identity-delegation:grant` is posted programmatically by whatever call site decides to post it (harness, service, operator). No CLI surface in this convention. |
| 2 | Does `rd init` on machine-2 require any new flag or step? | No. This convention adds nothing to `rd init`. |
| 3 | Scenario where user must manually revoke a grant? | No (revocation is ab7). Normal session lifecycle is handled by `expires_at`. |
| 4 | Any failure mode "start blocked, please do X"? | No. Grant posting is a normal convention send; failures are caller-handled and never block a session from starting. |
| 5 | Any command fails when network is unreachable? | No. Grant signing is local computation; grant posting buffers via the existing pending-message path. |
| 6 | Steps to onboard a new machine to an existing project? | Unchanged — the convention adds no new machine-onboarding steps. |

All six pass.

## 7. Conformance

A call site is conformant with this convention if:

- Every grant message it posts has tag `identity:granted`, signed by a valid `member_key` of the target campfire, with a payload matching the schema in §3.
- Every grant message it accepts as authoritative has passed all five validation rules in §4.

No other requirements. No required API. No required integration with any Go package. A shell script that crafts and posts a correctly-formed grant message via the existing `cf` CLI is conformant.

## 8. Handoff

- **Declaration:** `pkg/convention/declarations/identity-delegation-grant.json` (ships alongside this doc).
- **Trust resolution:** defined in `campfire/docs/conventions/identity-v0.2-trust-resolution.md` (campfire-ead).
- **Revocation enforcement:** defined in a future doc for campfire-ab7.
- **Per-actor keypair minting at real call sites** (Claude Code harness, cf-mcp server, dontguess operator, forge operator, etc.): filed as separate items after these three designs land. One item per call site.
