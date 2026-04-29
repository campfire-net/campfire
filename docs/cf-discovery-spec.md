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

## 11. Post-Join Verification (Tier 2 — Honeypot Defense)

### 11.1 Overview

When a client auto-joins a campfire via Tier-2 scoped auto-join (§3.2 of
0.30-design.md), the beacon's `join_protocol: "open"` claim is
**tainted-by-construction** — the signature proves the campfire operator
published this claim, but does NOT prove the campfire currently honors it.
A hostile operator could advertise `join_protocol: "open"` in a beacon,
wait for a non-member to auto-join, and then harvest the mandatory rekey
that fires on member admission ("honeypot-rekey harvest").

Post-join verification closes this attack by requiring the joiner to
**observe** the campfire's behavior before committing to membership.

### 11.2 Chosen Mechanism: probe-write-then-observe

**Decision (OPEN-015):** The chosen post-join verification mechanism is
**probe-write-then-observe**.

The alternative considered and rejected was **member-set Merkle-compare on
first sync** — computing a Merkle root of the returned member set and
comparing it against a value the inviter encoded in the invite token. That
approach was rejected for three reasons:

1. **Invite-token coupling.** Member-set Merkle-compare requires the inviter
   to encode a member-set commitment in the token at invite time. Tier-2
   scoped auto-join is designed for configured namespace roots, not invite
   flows — there is no inviter embedding a token, so there is no commitment
   to compare against.
2. **State staleness.** The member set returned on first sync may differ
   from the commitment if members joined or left between invite issuance and
   the joiner's first sync. This produces false-positive verification
   failures in normal operation.
3. **Complexity.** Merkle-compare adds ~150 LOC of hash-tree machinery plus
   a commitment-encoding scheme in the invite token format. The
   probe-write-then-observe mechanism reuses the existing send/read
   primitives already present in the protocol client.

**Probe-write-then-observe** was chosen because:
- It exercises the same send/read path every member uses — no new
  primitives.
- It directly detects enforcement-mode campfires (campfires that silently
  drop non-member writes or queue them for operator review rather than
  delivering them openly).
- It fires per-hop in multi-hop chain resolution, providing the same
  protection depth-1 and depth-n (see OPEN-005 round-2 doc,
  multi-level-snippet-chain.md).
- LOC budget: ~30 LOC addition to the `AutoJoinFunc` scaffold at
  `naming/resolve.go:121-126`.

### 11.3 Mechanism Steps

After the transport-level join completes (identity admitted, rekey
received), but before the joiner records the campfire as a trusted
member in local state, the client executes the following probe:

1. **Send a probe message.** The joiner sends a benign, schema-neutral
   message to the campfire. The message MUST be tagged `discovery:probe`
   and MUST contain no content other than the probe tag and the joiner's
   signature. The probe message is transient — it carries no meaning and
   MUST NOT be interpreted as content by other members.

2. **Read back the probe.** The joiner reads the campfire's message stream
   and checks whether the probe message appears. A campfire that is
   genuinely open MUST deliver the joiner's own message back to them on
   read (all members see all messages, including their own).

3. **Compare.** If the probe message appears in the read result within the
   verification timeout (default: 5 seconds), verification passes. If the
   probe message does NOT appear — because the campfire silently dropped
   it, queued it for moderation, or filtered it — verification fails.

4. **On pass:** The joiner records the campfire as a trusted Tier-2
   member. Normal reads and convention dispatches proceed.

5. **On fail (unjoin trigger):** The joiner executes the unjoin protocol
   (§11.4).

#### §11.3.1 Probe Timeout False-Positives on High-Latency Campfires

The 5-second verification timeout conflates two **distinct** timeout failure
modes that require different responses:

**Timeout due to suppression (honeypot):** The campfire received the probe
write but silently dropped or queued it — the probe never entered the
message stream. This is the enforcement-mode honeypot scenario (§11.6).
The probe message will never appear regardless of how long the joiner waits.
→ **Correct response: unjoin-declaration + leave.**

**Timeout due to latency (false-positive risk):** The campfire accepted the
probe write and the message is in the stream, but the joiner's read call
returned before the message was delivered — because the campfire is on a
high-latency network path (e.g., a relay with > 5 s round-trip, a campfire
hosted in a distant region, or a backpressured HTTP transport). The probe
message would appear if the joiner waited longer. This failure mode is
indistinguishable from suppression at the transport level within the default
timeout window.
→ **False-positive risk: the joiner unjoins a legitimate open campfire.**

**The spec does not currently provide a mechanical way to distinguish the two
modes.** A suppressed probe and a delayed probe look identical to the joiner
within the timeout window. The design accepts this ambiguity with the following
acknowledged trade-off:

- The 5-second default is intentionally aggressive. It optimizes for
  fast honeypot detection at the cost of false-positive risk on high-latency
  campfires.
- Operators of high-latency campfires (or clients connecting across
  high-latency paths) SHOULD configure a longer probe timeout via the
  `discovery.probe_timeout` config key (e.g., `30s` or `60s`). The default
  of 5 seconds is appropriate for co-located or low-latency deployments.
- A false-positive unjoin-declaration is forensically benign: the joiner
  leaves, the snippet is degraded, and the operator can inspect the
  declaration and identify the timeout as a latency event (probe_msg_id
  will be present in the campfire's message log, proving the probe was
  delivered). The joiner may manually re-join outside the auto-join path.

**If a future implementation can distinguish the modes mechanically** (e.g.,
by checking a transport-level acknowledgement from the send step before
starting the observe timeout), the behavior SHOULD be updated to:
- On send-not-acknowledged: treat as suppression → unjoin immediately.
- On send-acknowledged but observe-timeout: treat as latency → retry with
  backoff up to a configurable maximum, then degrade (not unjoin).

Until such a transport-level send acknowledgement is available, the 5-second
timeout with operator-configurable override is the correct posture. File a
follow-up item if you are implementing the mechanical distinction (it requires
transport changes and is out of scope for this spec clarification).

### 11.4 Unjoin Trigger and Protocol

Verification failure means the campfire is behaving inconsistently with
its advertised `join_protocol: "open"` claim — this is the **honeypot
scenario**: a campfire that advertises open membership but enforces
non-open semantics to harvest joiner identity from the admission rekey.

When verification fails, the client MUST:

1. **Leave the campfire.** Issue a protocol-level leave, removing the
   campfire from the local member list and preventing further message
   reads.

2. **Sign and post an unjoin-declaration.** Before leaving, the joiner
   signs and posts an `unjoin-declaration` message to the campfire with
   the following fields:
   - `campfire_id` — the campfire being unjoined.
   - `reason` — the string `"probe-verification-failed"`.
   - `observed_inconsistency` — a human-readable description of the
     inconsistency observed (e.g., `"probe message not visible on read
     after join"`).
   - `probe_msg_id` — the message ID of the probe that was not returned.
   - `joiner_pubkey` — the joiner's Ed25519 public key hex.

   The unjoin-declaration is signed by the **joiner's own Ed25519 key**.
   The signed claim is: "I joined this campfire, observed inconsistent
   state (probe message not returned), and am unjoining." The campfire
   operator can read this declaration; the campfire cannot suppress it
   because the joiner is still a member at the moment of posting.

3. **Degrade the discovery entry.** Mark the campfire's snippet in the
   parent namespace as degraded with reason `"post-join-verification-failed"`.
   The degraded entry remains visible to the user with a warning indicator;
   it is NOT silently removed. Future auto-join attempts for the same
   campfire are suppressed until the user explicitly clears the degraded
   status or the parent namespace republishes a fresh snippet.

4. **Do not retry automatically.** Automatic retry after unjoin is
   prohibited. A campfire that fails verification once is not retried by
   the auto-join path. The user may manually attempt a direct join
   (outside the auto-join path), at which point verification runs again.

### 11.5 Unjoin Signature Contract

The unjoin-declaration message has the following signing contract:

- **Who signs:** The **joiner** — the identity that attempted to join and
  observed the inconsistency. The campfire operator does NOT sign the
  unjoin-declaration.
- **What is signed:** The canonical signing payload is the UTF-8 encoding
  of `discovery:unjoin-declaration\n<json-object>`, where `<json-object>`
  is the JSON serialization of the five unjoin fields
  (`campfire_id`, `reason`, `observed_inconsistency`, `probe_msg_id`,
  `joiner_pubkey`) in canonical field order.
- **Why the joiner signs:** The joiner's signature makes the inconsistency
  claim attributable and non-repudiable. A third party reading the
  unjoin-declaration can verify (a) the declaration came from the key that
  joined, (b) the declared probe_msg_id matches the probe message on
  record, and (c) the joiner's pubkey matches the admitted member — all
  without trusting the campfire operator.
- **What the signature does NOT prove:** The signature proves the joiner
  sent the unjoin-declaration, not that the campfire is definitively
  hostile. The campfire operator may have a legitimate explanation (e.g.,
  a race condition during rekey). The unjoin-declaration is a forensic
  record, not a verdict.

### 11.6 Honeypot Scenario: Concrete Detection

The following concrete scenario MUST be detectable by the
probe-write-then-observe mechanism:

**Scenario: Enforcement-mode campfire with open beacon.**

1. Operator publishes a campfire with beacon advertising
   `join_protocol: "open"`.
2. The campfire is internally configured in enforcement mode: new members
   are admitted at the identity layer (rekey fires, joiner gets the
   shared key), but the campfire silently drops or queues messages from
   members who have not been explicitly approved by the operator.
3. A Tier-2 auto-join fires for the campfire (e.g., it is a registered
   namespace root in roll-up config).
4. The joiner is admitted (identity rekey fires — the operator has
   harvested the joiner's rekey event and the joiner's public key from
   the admission flow).
5. The joiner sends the `discovery:probe` message.
6. The campfire drops the probe (enforcement mode suppresses it).
7. The joiner reads the campfire and does NOT see the probe message.
8. **Verification fires:** unjoin-declaration is posted and signed by the
   joiner. The joiner leaves. The snippet is degraded.

**Detection is guaranteed** because in an open campfire, the sender
always sees their own messages on read — this is a protocol invariant
(all members receive all messages, including their own, in delivery
order). An enforcement-mode campfire that suppresses member messages
necessarily violates this invariant, which is exactly what the probe
detects.

**A second concrete scenario: write-invisible campfire.**

A campfire that delivers reads normally but silently drops all writes
from new members (to prevent participation while harvesting join events)
is detected identically: the probe write is dropped, the read returns
no probe, verification fails.

### 11.7 Verification at Every Hop

In multi-hop namespace resolution (OPEN-005), post-join verification
fires at **every hop** where the resolver auto-joins an intermediate
namespace campfire, not just at the top hop. A hostile intermediate
namespace campfire (e.g., `metropolis` in `freeso.metropolis.lot42`)
cannot evade verification by being positioned at depth 2 — the
`AutoJoinFunc` in `naming/resolve.go` runs probe-write-then-observe
every time it joins a new campfire in the chain.

If verification fails at any hop, the chain truncates at that hop and
the failure is propagated up: the resolver returns an error indicating
which namespace failed verification, and the entire resolution is
treated as degraded.

### 11.8 Conformance Requirements

Implementers of cf-discovery Stage 0 (post-join verification) MUST:

1. Implement probe-write-then-observe as described in §11.3.
2. Execute verification after every Tier-2 auto-join before recording the
   campfire as trusted in local state.
3. Post the signed unjoin-declaration (§11.5) before leaving on failure.
4. Degrade the discovery entry on failure; never silently suppress it.
5. Suppress automatic retry after a verification failure.
6. Run verification at every hop in multi-hop chain resolution (§11.7).
7. Cover the honeypot scenario (§11.6) in automated tests.

---

## 12. Config-vs-Beacon Endpoint Precedence

> **Status (PLANNED — 0.30.x):** This rule is designed and frozen at the spec
> level. Code enforcement in `pkg/naming/client_transport.go` is scheduled for a
> 0.30.x patch. Until that patch lands, auto-join derives the transport endpoint
> from beacon data exclusively.
>
> **Resolves:** OPEN-025

### 12.1 The Rule

If the local roll-up config (`.cf/config.toml` or its project-level override)
declares an alias or an explicit endpoint for a campfire, that declaration
**overrides** any beacon advertising a different transport or endpoint for the
same campfire.

The rule is stated as a priority ordering:

```
config-declared endpoint  >  beacon-advertised endpoint
```

A beacon is an **untrusted hint**: it tells the client where a campfire claims to
be reachable, signed only by the campfire itself. The campfire operator could
rotate the endpoint at any time, advertise a honeypot address, or let the beacon
expire. Local config, by contrast, was explicitly written by the user or the
repository operator and is trusted at the same level as the local identity.

### 12.2 Conflict Scenario

The following concrete scenario names the case where config and beacon **disagree**
on the transport endpoint:

**Scenario: operator changes transport, beacon is stale.**

1. A repository's `.cf/config.toml` contains `behavior.auto_join` with a beacon
   string for campfire `A` pointing to endpoint `old.host:8080`.
2. The campfire operator migrates to a new host and publishes a fresh beacon
   pointing to `new.host:9090`.
3. The client scans beacons and finds the new beacon (`new.host:9090`).
4. The client also reads `.cf/config.toml` and finds the explicit alias/endpoint
   for campfire `A` pointing to `old.host:8080` (derived from the committed
   beacon in config).

**Under this rule:** the config-declared endpoint (`old.host:8080`) wins. The
client uses it — even though the beacon advertises a newer address. The user must
update `.cf/config.toml` explicitly to adopt the new endpoint. This is intentional:
config-declared endpoints are a trust anchor; silent beacon-driven endpoint
substitution is the attack vector that post-join verification (§11) is designed
to catch.

**Why config wins and not beacon:** A beacon is signed by the campfire itself,
which means a compromised or hostile campfire can advertise any endpoint it
chooses. A config-declared endpoint was explicitly approved by the local operator.
The precedence rule prevents a beacon from silently redirecting the client to a
new transport without local operator consent.

### 12.3 Adversarial Case

**Attack:** A hostile campfire operator publishes a beacon advertising a new
transport endpoint (`exfil.attacker.example`) after the user has joined. The
user's local beacon file (`~/.campfire/beacons/`) is updated by a background scan.
On the next auto-join (e.g., name resolution walk), the client picks up the new
beacon endpoint and connects to the attacker's relay.

**Expected behavior under this rule:** If the campfire was originally joined via
config-declared entry (`.cf/config.toml` `behavior.auto_join`), the config
endpoint is used and the updated beacon is ignored. If the campfire was joined
via beacon only (no config entry), the post-join verification mechanism (§11)
provides the safety net — the probe message is required to confirm the campfire
behaves consistently with its advertised protocol.

### 12.4 Conformance Requirements (PLANNED)

When implemented, resolvers MUST:

1. Before using a beacon transport endpoint for auto-join, check whether the
   local roll-up config declares an alias or endpoint for the same campfire ID.
2. If a config entry exists, use its transport — discard the beacon transport.
   Log the override at DEBUG level: `"config endpoint overrides beacon for
   campfire <short-id>"`.
3. If no config entry exists, proceed with the beacon transport and run
   post-join verification (§11) before recording the campfire as trusted.
4. Never silently substitute a beacon-advertised endpoint for a config-declared
   endpoint without user action (manual `cf join` with an explicit beacon
   argument is the only bypass).

---

*End of cf-discovery 1.0 snippet schema specification.*
