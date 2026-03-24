# Protocol Extension: Message Confidentiality

**Status:** Draft v0.2 (revised after adversarial design review)
**Date:** 2026-03-23
**Companion to:** protocol-spec.md (Draft v0.3)

---

## 1. Motivation

The campfire protocol provides message authenticity (Ed25519 signatures), provenance (hop chains), and membership semantics — but not message confidentiality. Payloads are plaintext at every layer: in the store, on the transport, at relay nodes, and at the hosted service operator.

This is the same architectural gap that TCP had. Encryption was bolted on 15 years later with SSL/TLS, creating decades of mixed plaintext/encrypted traffic, downgrade attacks, and an ecosystem where confidentiality was never the default. Campfire should not repeat this mistake.

Message confidentiality is a first-class protocol concern, not an application-layer add-on.

---

## 2. Design Principles

### 2.1 Opt-In Per Campfire

Encryption is a campfire-level property, not a per-message choice. A campfire is either encrypted or it is not. This prevents the mixed-mode confusion that plagued email (some messages PGP-encrypted, most not) and HTTP (mixed content warnings).

```
Campfire {
  ...
  encrypted: bool          # CBOR field 7 — confidentiality mode
  key_epoch: uint64        # CBOR field 8 — current symmetric key generation
}
```

When `encrypted: true`, all message payloads are ciphertext. Plaintext payloads in an encrypted campfire MUST be rejected by conforming implementations. The `encrypted` flag MUST be committed in the campfire's creation message (`campfire:encrypted-init`), signed by the campfire key. Members verify this flag against the creation message signature — not against mutable state provided by a relay or hosted service.

**Downgrade prevention:** A member joining an encrypted campfire receives the `encrypted: true` flag as part of the signed campfire state in the join response. The member MUST persist this flag locally (in the `campfire_memberships` store, not solely in mutable state files) and enforce it on every received message. A relay or hosted service that presents `encrypted: false` for a campfire the member knows to be encrypted is detectable: the member's persisted flag takes precedence over any externally-provided state. Implementations MUST reject plaintext payloads when their local store records the campfire as encrypted, regardless of what the transport layer claims.

### 2.2 Metadata Is Not Encrypted

Encryption covers `payload` only. These fields remain plaintext:
- `id`, `sender`, `timestamp`, `signature` — required for verification and routing
- `tags` — required for filter evaluation (campfires filter before delivery)
- `antecedents` — required for threading and DAG construction
- `provenance` — required for relay verification
- `instance` — required for routing

**Trade-off acknowledged:** Tags leak semantic information ("blocker", "gate-human", "security"). An agent that wants tag confidentiality should use opaque tags and encode meaning in the encrypted payload. The protocol does not encrypt tags because filters depend on them — an encrypted tag cannot be filtered, making campfire-level reception requirements unenforceable.

**Future consideration:** A layered key scheme (separate tag key and payload key) could allow blind relays to decrypt tags for filtering while keeping payloads opaque. This is deferred to a future version but the key derivation scheme in Section 3 is designed to accommodate additional purpose-specific keys via distinct HKDF info strings.

### 2.3 Group Key, Not Per-Recipient

Messages are encrypted under a shared symmetric key that all current members hold. This is not per-recipient encryption (which would require N ciphertexts per message in an N-member campfire). The group key model means:

- One ciphertext per message regardless of group size
- All current members can decrypt
- The campfire relay does not need to re-encrypt per recipient
- Key rotation on membership changes controls access

### 2.4 Epoch-Based Key Rotation

The symmetric key rotates on membership changes. Each rotation creates a new **key epoch**. Members who leave (or are evicted) do not receive the new epoch key and cannot decrypt future messages. Members who join receive only the current epoch key and cannot decrypt messages from prior epochs.

This provides:
- **Forward secrecy on eviction:** Evicted members lose access to future messages (new root secret generated)
- **Backward secrecy on join:** New members cannot read history before their join (no prior root secrets delivered)

**What is NOT provided:**

1. Protection against a member who saved ciphertext and key material before leaving. Once a member held the key and the ciphertext, they can always decrypt those messages. This is inherent to group encryption — the same limitation exists in Signal groups, MLS, and every group E2E system.

2. Per-sender forward secrecy. If any member's key material is compromised, all messages under that epoch's CEK are exposed — not just that member's messages. Per-sender forward secrecy (via sender key ratchets, as in Signal's Sender Keys pattern) is deferred to a future version.

### 2.5 Blind Relay

A **blind relay** is a campfire member with a restricted role: it can store messages, serve poll/sync requests, verify signatures, and evaluate tag-based filters — but it does NOT hold the CEK and cannot decrypt payloads. Blind relays are excluded from key delivery in `campfire:membership-commit` messages.

```
Member {
  identity: Identity
  joined_at: uint64
  role: "full" | "blind-relay"   # default: "full"
  filter_in: Filter
  filter_out: Filter
}
```

The `role` field is included in the membership hash and signed in provenance hops. A blind relay's role is visible and verifiable.

**Why this matters:** The hosted cf-mcp service wants to relay encrypted campfire messages without being able to read them. Without a blind relay role, the only options are: (a) full membership (holds all keys, can decrypt everything), or (b) not a member at all (cannot store/serve messages). A blind relay gives the hosted service a legitimate protocol-level position: "I store and forward your encrypted messages. I cannot read them. My role is visible in provenance."

**Graduation story:** Start with a hosted blind relay for transport reliability. When ready, self-host. The blind relay was never able to read your messages — graduation is about operational independence, not privacy escalation.

Blind relays:
- Are included in the membership hash (for transparency)
- Sign provenance hops with `role: "blind-relay"` (verified field)
- Are SKIPPED in key delivery maps within `campfire:membership-commit` messages
- Can evaluate filters on plaintext metadata fields (tags, sender, timestamp)
- Cannot decrypt payloads — they store `EncryptedPayload` as opaque bytes
- Cannot initiate epoch rotation (they do not hold key material)

---

## 3. Key Management

### 3.1 Campfire Encryption Key (CEK)

The CEK is a 256-bit symmetric key used for AES-256-GCM encryption of message payloads. It is derived deterministically from a root secret and the current epoch:

```
CEK = HKDF-SHA256(
  ikm:  root_secret,
  salt: epoch_number (8 bytes, big-endian),
  info: "campfire-message-key-v1"
)
```

The `info` string is **protocol-fixed**. Implementations MUST hardcode it. The `campfire:encrypted-init` system message includes the info string for documentation purposes, but implementations MUST NOT read the info string from the init message — a malicious campfire creator could inject a different info string that collides with another derivation domain.

**CEK caching:** Implementations MUST cache the derived CEK keyed by `(campfire_id, epoch)` and invalidate on epoch change. Re-deriving via HKDF on every message encrypt/decrypt is wasteful. One map lookup per message in the hot path.

### 3.2 Root Secret and Epoch Transitions

The root secret is a 256-bit random value. How it transitions depends on the type of membership change:

**Hash-chain derivation (joins and scheduled rotations):**

When a member joins or a scheduled rotation occurs, the new root secret is derived deterministically from the current root secret:

```
root_secret_{n+1} = HKDF-SHA256(
  ikm:  root_secret_n,
  salt: "campfire-epoch-chain",
  info: epoch_{n+1} (8 bytes, big-endian)
)
```

All existing members who hold `root_secret_n` can derive `root_secret_{n+1}` locally. No key delivery is required — the `campfire:membership-commit` message is an announcement only.

**Fresh secret generation (evictions and voluntary leaves):**

When a member is evicted or leaves voluntarily, a NEW random root secret is generated and delivered to all remaining full members via per-member hybrid encryption. The departed member holds the old root secret but cannot derive the new one (it is random, not chain-derived).

This is strictly better than generating fresh secrets for every transition: joins are O(0) key delivery cost, evictions are O(N), and scheduled rotations are free.

**Summary of transition types:**

| Event | Root Secret | Delivery Cost | Rationale |
|-------|-------------|---------------|-----------|
| Join | Chain-derived | O(0) | New member receives current secret on join; existing members derive locally |
| Eviction | Fresh random | O(N) | Departed member must not derive future secrets |
| Voluntary leave | Fresh random | O(N) | Leaving member may have been compromised; rotate anyway |
| Scheduled rotation | Chain-derived | O(0) | Prevents nonce exhaustion; no membership change |

### 3.3 Key Delivery

Key delivery uses the existing hybrid encryption mechanism (protocol-spec.md Section 6.2):

1. On **campfire creation** with `encrypted: true`: The creator generates a `root_secret_0` and stores it locally. The campfire state includes `encrypted: true` and `key_epoch: 0`.

2. On **member join**: The admitting member encrypts the current `root_secret` and current `key_epoch` to the joiner's Ed25519 public key using `EncryptToEd25519Key()` (Ed25519-to-X25519 + ephemeral ECDH + HKDF + AES-256-GCM). The epoch is then bumped via chain derivation. All existing members derive the new root secret locally. The joiner holds the pre-bump root secret and derives the post-bump secret.

3. On **member eviction or voluntary leave**: See Section 6 (`campfire:membership-commit`). A fresh root secret is generated and delivered via per-member hybrid encryption to all remaining full members. Blind relays are excluded from deliveries.

### 3.4 Key Epoch Lifecycle

```
Epoch 0: campfire created, root_secret_0 = random(32)
  -> CEK_0 = HKDF(root_secret_0, epoch=0, "campfire-message-key-v1")

Member B joins:
Epoch 1: root_secret_1 = HKDF(root_secret_0, "campfire-epoch-chain", epoch=1)
  -> B receives root_secret_0 and epoch=0 on join
  -> B (and all members) derive root_secret_1 locally
  -> CEK_1 = HKDF(root_secret_1, epoch=1, "campfire-message-key-v1")

Member A evicted:
Epoch 2: root_secret_2 = random(32), delivered to remaining members
  -> CEK_2 = HKDF(root_secret_2, epoch=2, "campfire-message-key-v1")
  -> A holds root_secret_1 but cannot derive root_secret_2

Scheduled rotation (no membership change):
Epoch 3: root_secret_3 = HKDF(root_secret_2, "campfire-epoch-chain", epoch=3)
  -> All members derive locally, no delivery needed
  -> CEK_3 = HKDF(root_secret_3, epoch=3, "campfire-message-key-v1")
```

### 3.5 Epoch Transition Window

**Problem (A1/A7):** Between a membership change and all members processing the new epoch, messages may be encrypted under different CEKs. The protocol does not guarantee delivery order.

**Resolution:** Membership changes and key rotation are atomic. The `campfire:membership-commit` message (Section 6.1) is the epoch boundary. All messages with `epoch < commit.new_epoch` use the old CEK. All messages with `epoch >= commit.new_epoch` use the new CEK. There is no window — the commit message IS the transition.

**Dual-epoch grace period:** Implementations MUST retain the previous epoch's CEK (and root secret) for a bounded period after processing a `campfire:membership-commit`. Messages encrypted under `epoch N-1` that arrive after the member has installed `epoch N` MUST still be decryptable.

Rules:
- Implementations MUST accept messages encrypted under `epoch N-1` or `epoch N` (the current and immediately prior epoch) at any time.
- Implementations MAY accept messages from older epochs if they retain the key material.
- Implementations MUST NOT accept messages encrypted under a FUTURE epoch they have not yet received key material for — these MUST be queued and retried after the corresponding `campfire:membership-commit` is processed.
- Implementations SHOULD retain old epoch key material for at least 1 hour or 1000 messages (whichever comes first) to handle reordered delivery. After that, old keys MAY be purged to enforce forward secrecy.

### 3.6 Epoch Retention Policy

Implementations MUST store root secrets keyed by `(campfire_id, epoch)`. The retention policy is:

- **Current epoch:** Always retained.
- **Previous epoch (N-1):** Retained for the dual-epoch grace period (Section 3.5).
- **Older epochs:** MAY be retained for history access (original members can re-read their own historical messages). No protocol requirement to retain or purge — this is a local policy decision.
- **After campfire ID rotation (rekey):** Epoch secrets MUST be migrated to the new campfire ID. The `UpdateCampfireID` operation MUST include the epoch secrets table. Failure to migrate results in silent decryption failure for all historical messages.

### 3.7 Rejoin Semantics

A member who was evicted and later re-admitted is treated as a new member. They receive the current root secret and epoch on re-join. They do NOT receive prior root secrets for epochs after their eviction. Their prior epoch keys (from before eviction) remain valid for messages they could already decrypt — the protocol does not attempt to retroactively revoke access to ciphertext the member previously held keys for.

---

## 4. Message Encryption Wire Format

### 4.1 Encrypted Payload Envelope

When `campfire.encrypted == true`, the `payload` field of a Message contains an `EncryptedPayload` structure instead of raw bytes:

```
EncryptedPayload {
  epoch:      uint64    # CBOR field 1 — key epoch used for encryption
  nonce:      []byte    # CBOR field 2 — 12-byte AES-GCM nonce
  ciphertext: []byte    # CBOR field 3 — AES-256-GCM(CEK, nonce, plaintext, aad)
}
```

The `EncryptedPayload` is CBOR-encoded and placed in the message's `payload` field as opaque bytes.

### 4.2 Authenticated Additional Data (AAD)

The AES-GCM AAD binds the ciphertext to the message context, preventing ciphertext transplant attacks:

```
AAD = CBOR({
  message_id:  message.id,
  sender:      message.sender,
  campfire:    campfire.public_key,
  epoch:       encrypted_payload.epoch,
  timestamp:   message.timestamp,
  algorithm:   "AES-256-GCM"
})
```

The `algorithm` field prevents downgrade-within-encryption if a future version introduces alternative algorithms. The `timestamp` field is TAINTED (sender-asserted) — it prevents replay of messages with altered timestamps but does not authenticate time.

**AAD implementation note:** The existing `AESGCMEncrypt` and `AESGCMEncryptWithNonce` functions in `pkg/crypto/hybrid.go` pass `nil` for AAD. Message encryption MUST use new functions (or modified variants) that accept and pass the AAD to `gcm.Seal()` and `gcm.Open()`. Using the existing functions without modification silently drops all AAD protection, making ciphertext transplant attacks trivially exploitable.

### 4.3 Signing Encrypted Messages

The message signature covers the `payload` field, which now contains the CBOR-encoded `EncryptedPayload`. The signing process is unchanged — `MessageSignInput` includes the payload bytes as-is. This means:

- The signature proves the sender produced this specific ciphertext
- Verification does not require decryption
- Relays, filters, and blind relays can verify authenticity without reading content

### 4.4 Nonce Generation

Nonces are 12 bytes, generated as:

```
nonce = random(12)
```

With AES-256-GCM, the probability of nonce collision becomes non-negligible at ~2^48 messages under the same key. With epoch rotation on every membership change, a single CEK is unlikely to encrypt more than a few thousand messages. Random nonces are safe for the intended use case.

**Scheduled rotation trigger:** To prevent nonce exhaustion in long-lived campfires with stable membership, implementations SHOULD trigger a scheduled epoch rotation (chain-derived, zero delivery cost) after encrypting 2^32 messages under a single epoch. This is the `reason: "scheduled"` trigger in `campfire:membership-commit`. At typical agent coordination volumes (hundreds of messages/day), this threshold would take decades to reach — but the mechanism exists for correctness.

---

## 5. Epoch Rotation Authorization

### 5.1 Who May Initiate

Epoch rotation is triggered by a `campfire:membership-commit` system message. This message MUST be signed by the campfire key.

**Threshold > 1 campfires:** The campfire signature requires threshold cooperation (FROST partial signatures from at least `threshold` members). This provides natural leader election — no single member can unilaterally rotate keys.

**Threshold = 1 campfires:** Any member holding the campfire private key can sign a `campfire:membership-commit`. Without a coordination rule, two members could simultaneously broadcast competing commits with different root secrets for the same epoch, silently forking the campfire into disjoint encryption domains.

**Resolution for threshold = 1:** Epoch numbers are monotonically increasing. If a member receives two `campfire:membership-commit` messages for the same epoch number with different root secrets, it MUST accept the one whose message ID is lexicographically lowest and reject the other. Members that installed the rejected commit MUST re-process the accepted commit.

**Authorization constraint:** Only the member who performed the membership change (admitted, evicted, or processed the leave) is authorized to initiate the corresponding epoch rotation. The `campfire:membership-commit` message includes the membership change and the key rotation atomically — there is no separate "initiate rekey" action. The member who evicts is the member who generates the new root secret and publishes the commit.

### 5.2 Ordering With Campfire ID Rekey

When a threshold=1 campfire evicts a member, two rotations occur:
1. Campfire ID rekey (`campfire:rekey`) — new Ed25519 keypair, new campfire identity
2. Epoch rotation (`campfire:membership-commit`) — new root secret

**Ordering:** The `campfire:membership-commit` is signed by the OLD campfire key (the one that existed at the time of eviction). The `campfire:rekey` changes the campfire's signing key. Implementations MUST process the membership-commit (which references the old campfire key) before the rekey (which changes it). Both messages carry sequence-establishing antecedents.

---

## 6. System Messages

### 6.1 campfire:membership-commit

Published atomically when membership changes occur in an encrypted campfire. This message combines the membership change and key rotation into a single protocol event, eliminating the race condition between separate membership and rekey messages.

```
campfire:membership-commit {
  tags: ["campfire:membership-commit"]
  payload: {
    type: "join" | "evict" | "leave" | "scheduled",
    member: <pubkey>,          # the joining/leaving/evicted member (null for scheduled)
    new_epoch: uint64,
    new_membership_hash: bytes,
    chain_derived: bool,       # true = members derive locally; false = check deliveries
    deliveries: {              # present only when chain_derived == false
      <member_pubkey_hex>: <hybrid_encrypted_new_root_secret>,
      ...
    }
  }
}
```

This message is signed by the campfire key (system message). Its payload is **NOT encrypted** under the CEK (the old CEK may be retired; the new one may not be established yet). When `chain_derived == false`, the `deliveries` field contains per-member hybrid-encrypted root secrets — only the intended recipient can decrypt their delivery. Blind relay members are NOT included in the deliveries map.

**Membership list exposure (acknowledged trade-off):** When `chain_derived == false` (eviction/leave), the `deliveries` map keys are member public keys in plaintext. An observer (relay, hosted service, network eavesdropper) can enumerate every full member of the encrypted campfire by inspecting this message. This is worse than normal metadata exposure (which only reveals who sends messages) — it reveals all full members, including silent ones. This is an inherent cost of per-member key delivery without additional infrastructure (e.g., private information retrieval). Mitigations:

- Blind relays are excluded from deliveries, so their membership is not revealed through this channel (though it is visible in the membership hash).
- For campfires where membership privacy is critical, members should use dedicated, per-campfire Ed25519 keypairs rather than their primary identity key.

**Atomicity:** All messages with `epoch < new_epoch` use the old CEK. All messages with `epoch >= new_epoch` use the new CEK. The commit message IS the epoch boundary. There is no window where the encryption state is ambiguous.

### 6.2 campfire:encrypted-init

Published at campfire creation when `encrypted: true`. This message establishes the encryption parameters in the campfire's message history and commits the `encrypted: true` flag under the campfire's signature.

```
campfire:encrypted-init {
  tags: ["campfire:encrypted-init"]
  payload: {
    epoch: 0,
    algorithm: "AES-256-GCM",
    kdf: "HKDF-SHA256",
    info: "campfire-message-key-v1"
  }
}
```

This message is signed by the campfire key. Its payload is **NOT encrypted** (it contains algorithm metadata, not secret material). The `encrypted-init` message is the canonical proof that this campfire was created as encrypted — members use its existence and campfire signature to verify the encryption flag, independent of any relay-provided state.

**The `info` field is informational only.** Implementations MUST hardcode the info string in their HKDF calls, not read it from this message. See Section 3.1.

---

## 7. Interaction With Protocol Features

### 7.1 Filters

Filters operate on plaintext fields: `tags`, `sender`, `timestamp`, `antecedents`. Since `payload` is encrypted, payload-based filtering (e.g., `payload-size` predicate) operates on the ciphertext size, not the plaintext size. This is documented but acceptable — payload-size filtering on encrypted campfires is approximate.

**Amplified tag reliance:** In encrypted campfires, tags are the primary plaintext signal for filtering. Tags remain TAINTED (sender-chosen). A malicious member could use misleading tags to manipulate filters. Filters that previously cross-checked tags against payload content can no longer do so without decryption. Filters that gate trust decisions MUST include at least one verified dimension (sender key, provenance depth) per protocol-spec.md filter input classification.

### 7.2 Provenance

Provenance hops are computed over the entire message including the encrypted payload. No change required — hops sign message bytes, and the payload bytes happen to be ciphertext.

Blind relay provenance hops include `role: "blind-relay"` as a verified field. Downstream members can see that a hop was performed by a blind relay.

### 7.3 Compaction

Compaction events (`campfire:compact`) reference message IDs, not payloads. No change required.

### 7.4 Threshold Signatures

Threshold > 1 campfires work identically for message encryption. The campfire's signing key (FROST-distributed) signs provenance hops. The CEK is a separate symmetric key, independent of the signing key. Both are delivered to members on join.

The epoch rotation authorization for threshold > 1 campfires naturally requires quorum cooperation (Section 5.1), providing stronger protection against unilateral rekey attacks than threshold = 1 campfires.

### 7.5 Transport Bridging

`cf bridge` relays encrypted payloads as opaque bytes. The bridge does not decrypt — it does not hold the CEK (unless the bridge operator is a full member). This is correct: the bridge is transport infrastructure, not a campfire participant. If the bridge is a full member (per the bridge identity design in the hosted spec), it does hold the CEK and can decrypt. This is the expected trade-off for bridge-as-member.

### 7.6 Hosted cf-mcp

**Blind relay mode (recommended for encrypted campfires):** The hosted service joins as a blind relay. It stores encrypted payloads as opaque bytes, serves them on poll/sync, verifies signatures, and evaluates tag-based filters. It cannot decrypt message content. This is the recommended deployment for encrypted campfires using hosted infrastructure.

**Full member mode (current default for hosted agents):** The hosted service holds the agent's Ed25519 private key. It can derive the X25519 private key, decrypt any hybrid-encrypted key delivery, and therefore hold every root secret delivered to any of its hosted agents. The service can derive every CEK for every campfire where any member is hosted on it.

**Critical implication:** For a campfire where ALL full members are hosted on the same service, encryption provides zero confidentiality against the service operator. The operator sees everything — identical to an unencrypted campfire from the operator's perspective. Encryption only protects against network-level eavesdroppers, not the host.

**For a campfire where SOME full members are self-hosted:** The self-hosted members' messages are genuinely protected from the hosted operator (assuming the hosted operator is a blind relay, not a full member in its own right). The hosted members' messages are readable by the operator.

**The graduation argument is concrete and load-bearing:** "Self-host to ensure the hosted operator cannot read your campfire messages. For encrypted campfires, use the hosted service as a blind relay — it provides transport without trust."

### 7.7 Terminology Clarification: "Rekey" vs "Epoch Rotation"

The protocol uses "rekey" (`campfire:rekey`) to mean campfire Ed25519 keypair rotation — the campfire gets a new identity. The encryption extension uses "epoch rotation" (`campfire:membership-commit`) to mean symmetric CEK rotation — the campfire keeps its identity but changes its encryption key. These are orthogonal operations:

| Operation | Changes | Triggered By | System Message |
|-----------|---------|--------------|----------------|
| Campfire rekey | Ed25519 keypair, campfire ID | Eviction (threshold=1), key compromise | `campfire:rekey` |
| Epoch rotation | Root secret, CEK | Membership change, scheduled rotation | `campfire:membership-commit` |

Implementations MUST NOT conflate these. They use separate handlers, separate state, and separate message types. On eviction in a threshold=1 encrypted campfire, both occur — see Section 5.2 for ordering.

---

## 8. Backward Compatibility

### 8.1 Unencrypted Campfires

Campfires with `encrypted: false` (or the field absent) behave exactly as today. No payload wrapping, no CEK, no epoch tracking. The encryption machinery is entirely dormant.

### 8.2 Mixed Clients

A client that does not support encryption MUST reject membership in an encrypted campfire. The `encrypted: true` flag in campfire state signals this requirement. A non-encryption-aware client that joins an encrypted campfire will see opaque CBOR bytes in the payload field and cannot participate meaningfully.

### 8.3 Upgrade Path

An existing unencrypted campfire cannot be upgraded to encrypted in-place (this would break message history continuity). To encrypt, create a new campfire with `encrypted: true` and migrate members. The old campfire's history remains readable in the old campfire.

**Migration acknowledgment:** The new encrypted campfire has a different campfire ID. Every consumer that uses campfire_id as a stable reference will have two IDs for the "same" campfire. The protocol does not define a forwarding pointer. The application layer owns the migration mapping. This is a scope boundary, not a protocol defect.

---

## 9. Implementation Notes

### 9.1 Existing Primitive Reuse

The crypto foundation reuses existing primitives:

| Need | Existing Primitive | Location |
|------|--------------------|----------|
| CEK derivation | `HKDFSha256WithSalt` (epoch bytes as salt) | `pkg/crypto/hybrid.go` |
| Payload encryption | `AESGCMEncryptWithNonce` (with new AAD-aware variant) | `pkg/crypto/hybrid.go` |
| Key delivery | `EncryptToEd25519Key` / `DecryptWithEd25519Key` | `pkg/identity/x25519.go` |
| CBOR encoding | `pkg/encoding.Marshal/Unmarshal` | `pkg/encoding/` |
| Message signing | `VerifySignature` (unchanged — signs payload bytes as-is) | `pkg/message/message.go` |

**New code required:**
- `EncryptPayload()` / `DecryptPayload()` functions with AAD support (~45 LOC)
- `EncryptedPayload` CBOR type (~30 LOC)
- `CampfireState` fields: `Encrypted`, `KeyEpoch`, `RootSecret` (CBOR fields 7-9, omitempty) (~15 LOC)
- `campfire_epoch_secrets` store table (migration 6) + CRUD (~50 LOC)
- `encrypted` column on `campfire_memberships` (migration 7) for receive-path enforcement (~15 LOC)
- Epoch rotation handler (new, separate from existing rekey handler) (~100 LOC)
- `JoinResponse.EncryptedRootSecret` field (~30 LOC)
- `cf create --encrypted` CLI flag (~15 LOC)
- Tests (~150 LOC)

**Total estimate:** ~450 LOC. 2-3 implementation sessions.

### 9.2 Store Integration

- `AddMessage` stores payload as BLOB — encrypted payload bytes are stored identically to plaintext. No schema change for the messages table.
- `ListMessages` returns `EncryptedPayload` CBOR bytes for encrypted campfires. Decryption is the caller's responsibility. The store does not hold CEKs — this is an explicit design choice, not an oversight.
- `UpdateCampfireID` (campfire ID rekey) MUST include `campfire_epoch_secrets` in the bulk rename. Missing this migration causes silent decryption failure for all historical messages after a campfire rekey.

### 9.3 Performance

Per-message overhead: ~5-10us (1x HKDF for CEK derivation from cache miss + 1x AES-256-GCM + CBOR marshal). Invisible for coordination workloads.

Rekey cost (eviction): ~30-50us per member for hybrid encryption. N=20 members: ~600us-1ms total. Acceptable for eviction-frequency operations.

---

## 10. What This Does Not Cover

- **Metadata privacy:** Sender, recipient membership, tags, timestamps, and message graph structure are visible. `campfire:membership-commit` deliveries additionally expose the full member list on eviction/leave events. Metadata-private protocols (like Sealed Sender in Signal) require a fundamentally different architecture.
- **Deniability:** Messages are non-repudiable (Ed25519 signatures). Deniable authentication would require replacing Ed25519 with a ring signature or similar scheme.
- **Post-quantum:** AES-256 is believed quantum-resistant. Ed25519 and X25519 are not. Post-quantum key exchange (e.g., ML-KEM) is future work.
- **Large payload encryption:** The entire payload is encrypted as one AES-GCM block. For payloads >64KB, chunked encryption with STREAM construction may be warranted. Deferred — campfire messages are coordination signals, not file transfers.
- **Tag encryption:** Tags remain plaintext for filter evaluation. A future version may introduce a layered key scheme where blind relays hold a tag key but not the payload key, enabling filtered delivery of encrypted-payload messages. The HKDF info string scheme supports this via domain separation (e.g., `"campfire-tag-key-v1"`).
- **Per-sender forward secrecy:** The group CEK model means compromising any member's key material exposes all messages under that epoch. Sender key ratchets (Signal pattern) would provide per-sender isolation but add significant state management complexity. Deferred to a future version.
- **Creator key backup:** At epoch 0, the root secret exists only in the creator's local storage. If lost before any other member joins, the campfire's encryption is unrecoverable. For ephemeral agent coordination campfires, this is acceptable. For long-lived campfires, applications should implement their own backup strategy outside the protocol.

---

## 11. Attack Resolution Matrix

Every attack from the adversarial review is either resolved in this spec or documented as a known constraint.

| ID | Attack | Severity | Resolution |
|----|--------|----------|------------|
| A1 | Race condition during epoch transition | CRITICAL | Resolved: atomic `campfire:membership-commit` (Section 6.1) eliminates the window. Dual-epoch grace period (Section 3.5) handles reordered delivery. |
| A2 | Backward secrecy claim overstated; rejoin undefined | HIGH | Resolved: claims tightened (Section 2.4). Rejoin semantics defined (Section 3.7). Chain-derived epochs (Section 3.2) clarify what new members can/cannot derive. |
| A3 | Rekey-epoch leaks full membership list | MEDIUM | Acknowledged: documented as explicit trade-off (Section 6.1). Mitigation: per-campfire keypairs. Blind relays excluded from deliveries. |
| A4 | No authenticated encryption flag — downgrade possible | HIGH | Resolved: `encrypted-init` commits the flag under campfire signature (Section 6.2). Members persist locally (Section 2.1). Local flag takes precedence over relay-provided state. |
| A5 | Hosted model makes encryption theatrical | HIGH | Resolved: blind relay role (Section 2.5) provides a protocol-level non-decrypting relay position. Hosted threat model made explicit (Section 7.6). |
| A6 | AAD doesn't commit to algorithm | MEDIUM | Resolved: `algorithm` field added to AAD (Section 4.2). Tainted timestamp acknowledged. |
| A7 | No ordering + ambiguous epoch state | MEDIUM | Resolved: dual-epoch grace period (Section 3.5) with explicit rules for current, prior, and future epochs. |
| A8 | Info string from init message could be attacker-controlled | LOW | Resolved: info string is protocol-fixed, not configurable (Sections 3.1 and 6.2). |
| A9 | Nonce exhaustion in long-lived epochs | LOW | Resolved: scheduled rotation defined (Section 4.4). Trigger: 2^32 messages per epoch. `reason: "scheduled"` in membership-commit. |
| A10 | No leader election for rekey-epoch (threshold=1 race) | HIGH | Resolved: lexicographic tiebreak on message ID for conflicting commits (Section 5.1). Authorization constraint: only the actor who performed the membership change may initiate. |
| A11 | Existing crypto primitives don't support AAD | MEDIUM | Acknowledged: new AAD-aware functions required (Section 4.2 implementation note). Using existing nil-AAD functions is a critical implementation error. |
| A12 | Creator single point of failure for root secret | LOW | Acknowledged: documented in Section 10 (creator key backup). Application-layer concern. |

---

## 12. Prior Art and Credits

- **MLS (RFC 9420):** The hash-chain epoch derivation (Section 3.2) is inspired by the MLS key schedule, which chains forward from each epoch's keys plus new entropy. The multi-purpose key derivation via distinct HKDF info strings follows the MLS pattern. MLS was evaluated for wholesale adoption but rejected for v1 due to implementation complexity at campfire's target scale (2-20 members). See Section 10 (tag encryption, per-sender forward secrecy) for MLS-inspired future work.
- **Signal Sender Keys:** Per-sender forward secrecy via symmetric ratchets per member. Evaluated and deferred to a future version (Section 10).
- **campfire:rekey-epoch → campfire:membership-commit:** The original draft used separate membership change and rekey messages. The atomic commit design (Section 6.1) was proposed during design review to eliminate the race condition identified in A1.

---

## Appendix A: Reserved Tags (additions to protocol-spec.md)

The following tags should be added to the Reserved Tag Namespace table in protocol-spec.md:

| Tag | Signed By | Purpose |
|-----|-----------|---------|
| `campfire:encrypted-init` | Campfire key | Establishes encryption parameters at creation |
| `campfire:membership-commit` | Campfire key | Atomic membership change + epoch rotation |
