# Protocol Extension: Message Confidentiality

**Status:** Draft
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
  encrypted: bool          # CBOR field N — confidentiality mode
  key_epoch: uint64        # CBOR field N+1 — current symmetric key generation
}
```

When `encrypted: true`, all message payloads are ciphertext. Plaintext payloads in an encrypted campfire are rejected by conforming implementations.

### 2.2 Metadata Is Not Encrypted

Encryption covers `payload` only. These fields remain plaintext:
- `id`, `sender`, `timestamp`, `signature` — required for verification and routing
- `tags` — required for filter evaluation (campfires filter before delivery)
- `antecedents` — required for threading and DAG construction
- `provenance` — required for relay verification
- `instance` — required for routing

**Trade-off acknowledged:** Tags leak semantic information ("blocker", "gate-human", "security"). An agent that wants tag confidentiality should use opaque tags and encode meaning in the encrypted payload. The protocol does not encrypt tags because filters depend on them — an encrypted tag cannot be filtered, making campfire-level reception requirements unenforceable.

### 2.3 Group Key, Not Per-Recipient

Messages are encrypted under a shared symmetric key that all current members hold. This is not per-recipient encryption (which would require N ciphertexts per message in an N-member campfire). The group key model means:

- One ciphertext per message regardless of group size
- All current members can decrypt
- The campfire relay does not need to re-encrypt per recipient
- Key rotation on membership changes controls access

### 2.4 Epoch-Based Key Rotation

The symmetric key rotates on membership changes. Each rotation creates a new **key epoch**. Members who leave (or are evicted) do not receive the new epoch key and cannot decrypt future messages. Members who join receive only the current epoch key and cannot decrypt messages from prior epochs.

This provides:
- **Forward secrecy on eviction:** Evicted members lose access to future messages
- **Backward secrecy on join:** New members cannot read history before their join

**Not provided:** Protection against a member who saved ciphertext and key material before leaving. Once a member held the key and the ciphertext, they can always decrypt those messages. This is inherent to group encryption — the same limitation exists in Signal groups, MLS, and every group E2E system.

---

## 3. Key Management

### 3.1 Campfire Encryption Key (CEK)

The CEK is a 256-bit symmetric key used for AES-256-GCM encryption of message payloads. It is derived deterministically from a root secret and the current epoch:

```
CEK = HKDF-SHA256(
  ikm:  campfire_root_secret,
  salt: epoch_number (8 bytes, big-endian),
  info: "campfire-message-key-v1"
)
```

The `campfire_root_secret` is a 256-bit random value generated at campfire creation time. It is part of the campfire's key material, delivered to members alongside the campfire's Ed25519 private key (for threshold=1) or FROST key shares (for threshold>1).

### 3.2 Key Delivery

Key delivery uses the existing hybrid encryption mechanism (protocol-spec.md Section 6.2):

1. On **campfire creation** with `encrypted: true`: The creator generates a `campfire_root_secret` and stores it locally. The campfire state includes `encrypted: true` and `key_epoch: 0`.

2. On **member join**: The admitting member encrypts the `campfire_root_secret` and current `key_epoch` to the joiner's Ed25519 public key using the existing `EncryptToEd25519Key()` hybrid encryption (Ed25519→X25519 + ephemeral ECDH + HKDF + AES-256-GCM). The joiner derives the current CEK from the root secret and epoch.

3. On **member eviction or leave**: The remaining members increment `key_epoch` and publish a `campfire:rekey-epoch` system message. Each remaining member already holds the root secret and can derive the new CEK locally. The departed member holds the old root secret but the new epoch number changes the HKDF output, producing a different CEK.

**Wait — this doesn't work.** If the departed member has the root secret, they can derive any future CEK by just incrementing the epoch. The root secret must change on eviction.

### 3.3 Root Secret Rotation on Eviction

When a member is evicted (or leaves), the campfire must rotate the root secret, not just the epoch. The new root secret is generated by a remaining member and distributed to all other remaining members via the existing key delivery mechanism.

```
campfire:rekey-epoch {
  new_epoch: uint64
  key_deliveries: {
    <member_pubkey>: <encrypted_new_root_secret>,
    ...
  }
}
```

Each `encrypted_new_root_secret` is hybrid-encrypted to the recipient member's Ed25519 public key. The evicted member's key is not in the delivery list.

**Cost:** O(N) encryptions per eviction, where N is the remaining member count. At campfire sizes typical for agent coordination (2-20 members), this is negligible.

On **voluntary leave**, the same rotation happens. The leaving member is trusted (they chose to leave) but the protocol does not distinguish trust levels — a member who leaves could have been compromised. Rotate anyway.

On **member join**, the root secret is NOT rotated. The new member receives the current root secret and epoch. They can derive the current CEK but not past CEKs (they don't have the previous root secrets). If the campfire wants to prevent new members from reading future messages sent before their join was processed, the join can optionally trigger an epoch bump (configurable).

### 3.4 Key Epoch Lifecycle

```
Epoch 0: campfire created, root_secret_0 generated
  → CEK_0 = HKDF(root_secret_0, epoch=0)

Member A evicted:
Epoch 1: root_secret_1 generated, delivered to remaining members
  → CEK_1 = HKDF(root_secret_1, epoch=1)

Member B joins:
  → B receives root_secret_1 and epoch=1
  → B derives CEK_1

Member C evicted:
Epoch 2: root_secret_2 generated, delivered to remaining members (including B)
  → CEK_2 = HKDF(root_secret_2, epoch=2)
```

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

The AES-GCM AAD binds the ciphertext to the message context, preventing ciphertext transplant attacks (moving an encrypted payload from one message to another):

```
AAD = CBOR({
  message_id: message.id,
  sender:     message.sender,
  campfire:   campfire.public_key,
  epoch:      encrypted_payload.epoch,
  timestamp:  message.timestamp
})
```

### 4.3 Signing Encrypted Messages

The message signature covers the `payload` field, which now contains the CBOR-encoded `EncryptedPayload`. The signing process is unchanged — `MessageSignInput` includes the payload bytes as-is. This means:

- The signature proves the sender produced this specific ciphertext
- Verification does not require decryption
- Relays and filters can verify authenticity without reading content

### 4.4 Nonce Generation

Nonces are 12 bytes, generated as:

```
nonce = random(12)
```

With AES-256-GCM, the probability of nonce collision becomes non-negligible at ~2^48 messages under the same key. With epoch rotation on every membership change, a single CEK is unlikely to encrypt more than a few thousand messages. Random nonces are safe.

---

## 5. Interaction With Protocol Features

### 5.1 Filters

Filters operate on plaintext fields: `tags`, `sender`, `timestamp`, `antecedents`. Since `payload` is encrypted, payload-based filtering (e.g., `payload-size` predicate) operates on the ciphertext size, not the plaintext size. This is documented but acceptable — payload-size filtering on encrypted campfires is approximate.

### 5.2 Provenance

Provenance hops are computed over the entire message including the encrypted payload. No change required — hops sign message bytes, and the payload bytes happen to be ciphertext.

### 5.3 Compaction

Compaction events (`campfire:compact`) reference message IDs, not payloads. No change required.

### 5.4 Threshold Signatures

Threshold > 1 campfires work identically. The campfire's signing key (FROST-distributed) signs provenance hops. The CEK is a separate symmetric key, independent of the signing key. Both are delivered to members on join.

### 5.5 Transport Bridging

`cf bridge` relays encrypted payloads as opaque bytes. The bridge does not decrypt — it does not hold the CEK (unless the bridge operator is a member). This is correct: the bridge is transport infrastructure, not a campfire participant. If the bridge is a member (per the bridge identity design in the hosted spec), it does hold the CEK and could decrypt. This is the expected trade-off for bridge-as-member.

### 5.6 Hosted cf-mcp

The hosted service stores encrypted payloads. If the hosted agent is a member (it holds the CEK), it can decrypt. This is inherent to the hosted model — the server holds the agent's private key and thus can derive the CEK.

For campfires where the hosted agent is NOT a member (it only relays), payloads are opaque. The hosted service is a blind relay for encrypted campfires it doesn't belong to.

**The graduation argument is now concrete:** "Self-host to ensure the hosted operator cannot read your campfire messages." For encrypted campfires with at least one self-hosted member, the hosted operator only sees ciphertext.

---

## 6. System Messages

### 6.1 campfire:rekey-epoch

Published when the root secret rotates (eviction or leave).

```
campfire:rekey-epoch {
  tags: ["campfire:rekey-epoch"]
  payload: {
    epoch: uint64,
    reason: "eviction" | "leave" | "scheduled",
    deliveries: {
      <member_pubkey_hex>: <hybrid_encrypted_root_secret>,
      ...
    }
  }
}
```

This message is signed by the campfire key (system message). Its payload is NOT encrypted under the CEK (the old CEK is being retired; the new one hasn't been established yet). The `deliveries` field contains per-member hybrid-encrypted root secrets — only the intended recipient can decrypt their delivery.

### 6.2 campfire:encrypted-init

Published at campfire creation when `encrypted: true`. Contains the initial key delivery to the creator (for consistency — the creator already has the key, but the system message establishes the epoch in the message history).

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

---

## 7. Backward Compatibility

### 7.1 Unencrypted Campfires

Campfires with `encrypted: false` (or the field absent) behave exactly as today. No payload wrapping, no CEK, no epoch tracking. The encryption machinery is entirely dormant.

### 7.2 Mixed Clients

A client that does not support encryption MUST reject membership in an encrypted campfire. The `encrypted: true` flag in campfire state signals this requirement. A non-encryption-aware client that joins an encrypted campfire will see opaque CBOR bytes in the payload field and cannot participate meaningfully.

### 7.3 Upgrade Path

An existing unencrypted campfire cannot be upgraded to encrypted in-place (this would break message history continuity). To encrypt, create a new campfire with `encrypted: true` and migrate members. The old campfire's history remains readable.

---

## 8. What This Does Not Cover

- **Metadata privacy:** Sender, recipient membership, tags, timestamps, and message graph structure are visible. Metadata-private protocols (like Sealed Sender in Signal) require a fundamentally different architecture.
- **Deniability:** Messages are non-repudiable (Ed25519 signatures). Deniable authentication would require replacing Ed25519 with a ring signature or similar scheme.
- **Post-quantum:** AES-256 is believed quantum-resistant. Ed25519 and X25519 are not. Post-quantum key exchange (e.g., ML-KEM) is future work.
- **Large payload encryption:** The entire payload is encrypted as one AES-GCM block. For payloads >64KB, chunked encryption with STREAM construction may be warranted. Deferred — campfire messages are coordination signals, not file transfers.
