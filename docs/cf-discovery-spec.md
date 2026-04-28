# cf-discovery 1.0 — Snippet Schema Specification

> **Status:** Draft — cf 0.30.0
> **Resolves:** OPEN-014 (snippet schema standardization / beacon redesign alignment)
> **References:** 0.30-design.md §3, round-2/multi-level-snippet-chain.md

---

## Overview

cf-discovery defines how campfire namespaces expose child campfire metadata to
members of the parent namespace without requiring the browsing member to join
the child. The core artifact is the **snippet** — a parent-signed, schema-locked
summary record published as a `naming:preview` message in the parent campfire.

This document formalizes:
- The snippet wire format (JSON fields, types, constraints)
- Signing rules (who signs, what is signed, how to verify)
- Freshness semantics (declared window, per-hop composition, degradation)
- Conformance rules for producers and consumers

---

## 1. Snippet Wire Format

### 1.1 Message Envelope

A snippet is sent as a standard campfire message with the following envelope
fields set by the protocol:

```
tag:    "naming:preview"
signer: <parent campfire's Ed25519 key>
```

The message body is a JSON object. The protocol transport preserves the
signature over the concatenation of the tag and the JSON payload bytes
(UTF-8, no BOM, no trailing newline).

### 1.2 Snippet JSON Schema

```json
{
  "name":               "<string>",
  "description":        "<string>",
  "member_count_bucket":"<string>",
  "freshness_window":   "<string>",
  "parent_signature":   "<string>"
}
```

All five fields are **required**. A snippet missing any field MUST be rejected
by consumers as malformed.

---

### 1.3 Field Definitions

#### `name` — string, required

The registered child name, exactly as written in the parent's name registration
(`naming:name:<name>` tag). Single segment only — dots are not permitted.

**Constraints:**

- Non-empty.
- Valid naming segment: matches `^[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?$`.
- Must not contain a dot (`.`). A snippet represents a single-hop child, never
  a multi-segment path.

**Adversarial rule:** A consumer MUST reject a snippet whose `name` field
contains a dot. A multi-segment `name` would impersonate a grandchild, which
the parent campfire cannot vouch for (no transitive trust — see §4).

#### `description` — string, required

A human-readable summary of the child campfire. Length is advisory; producers
SHOULD keep descriptions under 512 bytes (UTF-8).

**Constraints:**

- Non-empty.
- No embedded newlines (LF or CRLF). Producers MUST strip newlines; consumers
  MAY reject snippets with embedded newlines.
- No null bytes.

**Privacy rule:** The description MUST NOT contain member identities, content
previews, or any information not approved by the child campfire's operator. The
parent operator writes the description at registration time; the child operator
reviews it before registration completes (see §2 signing flow).

#### `member_count_bucket` — string, required

A bucketed (coarse) indicator of the child campfire's approximate membership.
Exact counts are not published to avoid deanonymization through count timing.

**Permitted values (exhaustive):**

| Value    | Meaning                    |
|----------|----------------------------|
| `"1"`    | Solo / single-member       |
| `"2-5"`  | Small group                |
| `"6-25"` | Medium group               |
| `"26+"`  | Large group or public space |

**Constraints:**

- MUST be one of the four values above. Any other value is malformed.
- The bucket is declared by the **parent** at registration time, derived from
  whatever membership information the parent has access to (typically the child
  registration payload). It is not recomputed on read.

**Adversarial rule:** A consumer receiving a snippet with `member_count_bucket`
outside the permitted set MUST reject it as malformed. A hostile namespace could
attempt to embed signals in non-standard bucket strings.

#### `freshness_window` — string, required

The duration after which a consumer SHOULD treat this snippet as stale. Stale
snippets are flagged degraded; they are NOT auto-refreshed or hidden, but the UI
MUST display a degradation indicator.

**Format:** Go duration string — a sequence of decimal numbers with unit
suffixes. Permitted units: `s`, `m`, `h`. Examples: `"5m"`, `"1h"`, `"30s"`.

**Constraints:**

- Non-empty.
- Must parse as a Go `time.Duration` with `time.ParseDuration`.
- Minimum: `"1s"`. Maximum: `"24h"`.
- Duration MUST be positive.

**Semantics:** The window is declared by the **parent** operator — not by the
child. The parent knows how frequently it refreshes its registration data. A
child declaring its own freshness window is not accepted.

**Multi-hop composition:** When a resolver walks a chain of snippets across
multiple namespace hops, the freshness window displayed to the user is the
**minimum** across all hops — not the window at the final hop. This collapses
the freshness-stacking attack where a hostile intermediate could pin a stale
entry by advertising a long window. See §3.2.

#### `parent_signature` — string, required

The Ed25519 signature produced by the **immediate parent** campfire's key,
over the canonical signing payload (see §2.2).

**Format:** Base64 standard encoding (RFC 4648), URL-safe alphabet
(`-` and `_` in place of `+` and `/`), no padding (`=`). Length after decode:
64 bytes (Ed25519 signature).

**Constraints:**

- Non-empty.
- Decodable as URL-safe base64 without padding.
- Decoded length: exactly 64 bytes.
- Signature verifies against the parent campfire's current Ed25519 key using
  the canonical signing payload (§2.2).

**Adversarial rule:** `parent_signature` MUST be produced by the **immediate**
parent in the registration tree. A resolver MUST NOT accept a snippet whose
signer is a grandparent, a root authority, or any campfire other than the direct
parent of the named child. Depth-2+ chains present separate per-hop signatures;
there is no co-signing across hops. See §4.

---

## 2. Signing Rules

### 2.1 Who Signs

The parent campfire signs every snippet it publishes. "Parent campfire" means
the campfire whose `naming:name:<name>` registration contains the child's
campfire ID — i.e., the immediate parent in the registration tree.

The parent uses its current Ed25519 private key (the one associated with its
campfire identity). Key rotation (`campfire:rekey`) invalidates all previously
signed snippets; a consumer receiving a snippet that no longer verifies against
the parent's current key MUST treat it as stale/degraded, not as forged.

### 2.2 Canonical Signing Payload

The signing payload is the UTF-8 encoding of:

```
naming:preview\n<json-object>
```

Where `<json-object>` is the snippet JSON with:
- Keys in the canonical field order: `name`, `description`, `member_count_bucket`,
  `freshness_window`. (The `parent_signature` field is excluded from the payload
  — it is the signature of the other four fields.)
- No trailing whitespace.
- No trailing newline after the JSON object.

Producers MUST serialize in canonical field order. Consumers MUST verify using
the same canonical form, not the wire-received JSON (which may have re-ordered
fields due to transport or tooling).

Example canonical payload (showing the separator newline between tag and JSON):

```
naming:preview
{"name":"lobby","description":"General discussion","member_count_bucket":"6-25","freshness_window":"5m"}
```

### 2.3 Verification Steps

A consumer MUST verify a snippet in this order, rejecting on any failure:

1. **Field presence:** all five fields present and non-empty.
2. **Field type:** all fields are JSON strings.
3. **Field constraints:** `name` matches the segment regex and contains no dot;
   `member_count_bucket` is one of the four permitted values; `freshness_window`
   parses as a duration in `[1s, 24h]`; `parent_signature` decodes to 64 bytes.
4. **Parent identity:** the signer of the enclosing campfire message is the
   **immediate parent** campfire's key (not a grandparent, not a foreign key).
5. **Signature:** the Ed25519 signature in `parent_signature` verifies over the
   canonical signing payload (§2.2) using the parent's current public key.
6. **Freshness:** the snippet is not stale (current time is within
   `freshness_window` of the message timestamp). If stale, mark degraded — do
   not reject.

Step 6 produces degradation, not rejection. Steps 1–5 produce rejection.

---

## 3. Freshness Semantics

### 3.1 Single-Hop Freshness

A snippet is **fresh** if:

```
current_time ≤ message_timestamp + parse(freshness_window)
```

A snippet is **stale** (degraded) if this condition is false.

Consumers MUST display a degradation indicator for stale snippets. Stale
snippets MUST NOT be silently dropped or auto-refreshed on the consumer side —
degradation is informational and is the expected behavior when the parent
campfire has not re-published recently.

### 3.2 Multi-Hop Freshness Composition

When a resolver walks a namespace chain of depth ≥ 2, the displayed freshness
window is the **minimum** of the freshness windows at each hop:

```
displayed_window = min(w_1, w_2, …, w_n)
```

Where `w_i` is the parsed `freshness_window` of the snippet at hop `i`.

A resolver MUST flag the entire resolved entry as degraded if **any** hop's
snippet is stale — not just the final hop. This prevents a hostile intermediate
from hiding staleness by advertising a long freshness window at an upper hop
while a lower hop is already expired.

**Example:**
- `freeso` publishes a snippet for `metropolis` with `freshness_window: "5m"`
- `metropolis` publishes a snippet for `lot42` with `freshness_window: "1h"`
- Displayed freshness window: `5m`
- If the `freeso→metropolis` snippet's timestamp is more than 5 minutes old, the
  entire `freeso.metropolis.lot42` entry is shown as degraded — regardless of the
  `metropolis→lot42` snippet's freshness.

### 3.3 Degradation is Not Removal

A degraded snippet remains visible. The consumer displays a staleness indicator
(e.g., "last updated 12 minutes ago — may be stale"). The consumer MUST NOT
remove the snippet from the browse list or treat it as a resolution failure.

---

## 4. Trust Model

### 4.1 Parent Signs for Immediate Children Only

A parent campfire MUST publish snippets only for its **immediate** children —
campfires it directly registers via `naming:name:<name>`. It MUST NOT publish
snippets for grandchildren or deeper descendants.

**Why:** The parent operator cannot observe the state of grandchildren. A snippet
signed by a grandparent would imply a vouch-for-state the grandparent cannot
verify. Per §1.3.1 ("Adversarial rule" for `name`), any snippet with a
dotted `name` field is malformed and MUST be rejected.

### 4.2 No Transitive Trust

The `parent_signature` in a snippet always means: "the immediate parent in the
registration tree asserts this child exists with this metadata." It does NOT mean:

- The root authority vouches for the child.
- The parent vouches for what the child publishes about its own children.
- Trust at one hop implies trust at the next.

In a depth-2 chain (`A → B → C`), the user's trust in `B`'s snippet of `C`
derives from the user's TOFU pin on `B`'s key — formed independently at the
moment the user joined `B`. `A`'s signature on the `A→B` snippet says nothing
about `C`.

### 4.3 Resolver Must Check Signer Identity

When verifying `parent_signature`, the resolver MUST check that the **signer of
the enclosing campfire message** is the known parent campfire key — not just that
the signature bytes are valid under some key.

Concretely: a resolver that has joined `freeso` and is reading its snippets MUST
verify that the message signer matches `freeso`'s known public key. A snippet
injected by a member of `freeso` who is not the campfire itself (i.e., not the
campfire's own key) MUST be rejected.

---

## 5. Producer Requirements

A campfire operator publishing snippets MUST:

1. Publish snippets only for names it has registered via `naming:name:<name>`.
2. Use the canonical field order (§2.2) when serializing the JSON payload.
3. Sign with the campfire's current Ed25519 key.
4. Declare a `freshness_window` that reflects the actual re-publish cadence.
5. Not include dotted names (multi-segment paths).
6. Not embed member identities, message content, or private data in `description`.
7. Re-publish snippets before the `freshness_window` expires to keep them fresh.
8. Emit a fresh snippet immediately after key rotation (`campfire:rekey`) to
   replace all now-invalid signatures.

---

## 6. Consumer Requirements

A consumer reading snippets MUST:

1. Perform all six verification steps (§2.3) in order.
2. Reject (not display) malformed snippets (failed steps 1–5).
3. Display (with degradation indicator) stale snippets (failed step 6).
4. Apply multi-hop freshness composition (§3.2) when resolving chains.
5. Not auto-join a child campfire based solely on snippet presence — snippets are
   for browsing; joining requires an explicit user action or Tier-2 scoped auto-join.
6. Not expose snippet `description` content to non-members of the parent (the
   snippet is a parent-namespace artifact; it is readable only by parent members).

---

## 7. Adversarial Cases

These cases MUST be tested by conformance harnesses and demo scripts.

### 7.1 Dotted Name Injection

**Attack:** A hostile parent publishes a snippet with `name: "child.grandchild"`,
attempting to make the resolver believe it is vouching for a two-hop path.

**Expected behavior:** Consumer rejects the snippet as malformed (§1.3 name
constraint: no dot permitted).

### 7.2 Invalid Bucket Value

**Attack:** A hostile parent publishes `member_count_bucket: "10000"` or
`member_count_bucket: ""` to embed a signal or evade bucket enforcement.

**Expected behavior:** Consumer rejects the snippet as malformed — `member_count_bucket`
must be one of the four permitted values.

### 7.3 Out-of-Range Freshness Window

**Attack:** A hostile parent publishes `freshness_window: "999h"` to prevent
degradation for an extended period after the parent goes offline.

**Expected behavior:** Consumer rejects the snippet as malformed — `freshness_window`
must be in the range `[1s, 24h]`.

### 7.4 Freshness-Stacking Attack

**Attack:** At chain depth 2, the intermediate namespace publishes
`freshness_window: "24h"` while the root snippet has a much shorter window.
The attacker hopes the resolver uses the bottom-hop window, masking staleness
at the top.

**Expected behavior:** The resolver applies MIN-composition (§3.2). The displayed
window and the degradation gate are both governed by the shortest window in the
chain.

### 7.5 Grandparent Signer

**Attack:** A snippet claims to be signed by a root authority rather than the
immediate parent, attempting to establish broad trust by impersonating a
well-known key.

**Expected behavior:** Consumer verifies that the enclosing message signer
matches the known immediate parent key. A mismatch causes rejection (§4.3).

### 7.6 Empty Required Field

**Attack:** A snippet is published with `description: ""` or any other required
field set to an empty string.

**Expected behavior:** Consumer rejects the snippet as malformed (§1.3: all
fields non-empty).

---

## 8. Relation to Other Artifacts

| Artifact | Signer | Location | Purpose |
|---|---|---|---|
| Snippet (`naming:preview`) | Parent campfire key | Parent namespace campfire | Browse-before-join metadata |
| Beacon | Child campfire key | Distributed / passed out-of-band | Cold-start dialing (endpoint, campfire ID) |
| Name registration (`naming:name:<name>`) | Member (registrar) key | Parent campfire | Authoritative name → campfire ID mapping |

A snippet SHOULD include the child's beacon string (for cold-start dialing) as
an additional JSON field named `beacon`. This field is **optional** and is not
part of the five required fields. Consumers that do not recognize `beacon` MUST
ignore it — unknown fields are tolerated.

The beacon field, if present, is:
- Signed by the parent along with the other four fields (included in the
  canonical signing payload between `freshness_window` and before the closing
  brace).
- Subject to the same signing constraints as the other fields.

---

## 9. Wire Example

### 9.1 Valid Snippet

```json
{
  "name": "lobby",
  "description": "General discussion for social members",
  "member_count_bucket": "6-25",
  "freshness_window": "5m",
  "parent_signature": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
}
```

*(The `parent_signature` above is a placeholder; real signatures are 64-byte
Ed25519 signatures encoded in URL-safe base64 without padding.)*

### 9.2 Invalid Snippets (rejection expected)

| Snippet issue | Field | Why rejected |
|---|---|---|
| `"name": "child.grand"` | `name` | Contains dot — multi-segment path |
| `"member_count_bucket": "10000"` | `member_count_bucket` | Not a permitted value |
| `"freshness_window": "999h"` | `freshness_window` | Exceeds 24h maximum |
| `"freshness_window": "0s"` | `freshness_window` | Not positive |
| `"description": ""` | `description` | Empty required field |
| Missing `parent_signature` | `parent_signature` | Required field absent |

---

## 10. Stage 3 Reference

Implementers of cf-discovery Stage 3 (the Tier-1 snippet producer and consumer)
MUST implement this specification in full. The following conformance invariants
apply:

1. Every snippet produced passes a local validation that runs §2.3 steps 1–3
   before signing.
2. Every snippet consumed is validated through §2.3 steps 1–6 before display.
3. Multi-hop freshness composition is implemented per §3.2.
4. All seven adversarial cases in §7 are covered by automated tests.

---

*End of cf-discovery 1.0 snippet schema specification.*
