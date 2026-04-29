# Bridge — Transport Relay Model

`protocol.Bridge` pumps messages between two `*Client` instances for a shared campfire ID. The bridge is a layer-1 primitive: it is transport-agnostic (works over filesystem or HTTP) and is the correct tool whenever a campfire's message stream needs to cross a transport boundary.

See `pkg/protocol/bridge.go` (137 LOC) and [`protocol-spec.md §4.2`](../docs/protocol-spec.md) for the wire-level specification.

---

## Mode — Re-Publish (0.30.0)

`BridgeOptions` currently supports one mode: **re-publish**.

**Re-publish mode (the only mode in 0.30.0).** The bridge calls `dest.Send()` with the source message's payload and tags. The destination receives a fresh message — new `ID`, new `Sender` (the bridge agent), new `Signature`, new hop. Original-author provenance is **not preserved**; the bridging campfire is the apparent author downstream. Use when the bridge is the trust anchor, for example when importing messages from a realm that has no Ed25519 representation (Slack→cf, Teams→cf, email→cf).

`BridgeOptions` fields in 0.30.0:
- `Bidirectional bool` — enable two-way pumping (source→dest AND dest→source)
- `TagFilter []string` — restrict bridging to messages with at least one matching tag
- `OnMessage func(msg *Message, direction string)` — optional per-message callback

---

## When to Use Bridge

| Scenario | Supported in 0.30.0 |
|---|---|
| Teams or Slack import into campfire | Yes — re-publish mode |
| Email gateway into campfire | Yes — re-publish mode |
| Cross-transport relay (fs ↔ HTTP) | Yes — re-publish mode |
| Multi-region message mirroring | Yes — re-publish mode (original-author attribution not preserved) |
| Hosted reader with original-author attribution | Planned — see §Planned for 0.30.x |

For bidirectional relays, set `Bidirectional: true` in `BridgeOptions`. The bridge runs two goroutines, each direction independent, with a shared dedup map keyed on `msg.ID`.

---

## The Blind-Relay Role

Re-publish mode produces a provenance hop carrying `role: blind-relay`. The role is hop-scoped, not message-scoped.

`IsBridged()` (`pkg/protocol/message.go:51`) returns `true` — the receiver always knows a bridge was involved. The encryption-key filter (`pkg/campfire/encryption.go:104-127`) excludes blind-relay members from key-chain delivery; the bridge sees plaintext only if it is an admitted member, not by virtue of being a relay.

Under re-publish, the bridge hop is the only hop. The original author is opaque to the receiver.

---

## What Bridge Does NOT Do

- **Not a federation primitive.** Bridge operates on a single shared campfire ID. It does not link separate campfires or resolve cross-campfire routing. Federation is a higher-layer concern.
- **Does not decrypt or re-sign payloads.** The bridge forwards payload bytes it receives from the transport. It never holds the private keys of the original author and cannot forge or modify signed content.
- **Does not cache.** The bridge is a live pump — it forwards messages as they arrive on the source subscription. It does not replay history or buffer messages for late joiners. The store on the destination side provides durability; the bridge is the delivery mechanism.
- **Does not enforce policy or gates.** The bridge forwards everything it observes (subject to `TagFilter` if set). Convention gates run at the receiving campfire's dispatcher, not inside the bridge.
- **Does not preserve original-author provenance in 0.30.0.** Re-publish replaces every message's apparent author with the bridge agent. Callers who need original-author attribution end-to-end should wait for pass-through mode (see §Planned for 0.30.x).

---

## Planned for 0.30.x — Pass-Through Mode

Pass-through mode is not implemented in 0.30.0. It is tracked in `campfireagent-4a0`.

When implemented, pass-through will write the original message envelope to the destination transport unchanged and append exactly one provenance hop signed by the bridging campfire with `Role = campfire.RoleBlindRelay`. `ID`, `Sender`, `Signature`, `Payload`, `Tags`, `Antecedents`, and all existing `Provenance` hops will be preserved. Original-author cryptographic attribution will survive end-to-end.

**Primary use case**: Tier 3.5 hosted-reader deployments (`reader.getcampfire.dev`). Re-publish at Tier 3.5 would replace every message's apparent author with the hosted-reader campfire, violating the trust-is-chosen property. Do not deploy the hosted reader to production until pass-through ships.

### Threat model (for future implementers)

Three attack classes the pass-through design must address:

**Forgery via verbatim re-injection.** Without a bridge hop, a hostile relay could replay any message it has observed into any destination campfire with no on-chain evidence of transit. Pass-through must append a blind-relay hop signed by the bridging campfire. The original `Sender`/`Signature` is unchanged; the bridge cannot forge messages it never saw.

**Replay across long latencies.** A message originally published at T=0 could be forwarded at T+30d. The appended hop's `Timestamp` must record when the bridge delivered the message. Consumers can compute `hop.Timestamp − msg.Timestamp` and reject stale pass-throughs at policy layer.

**Denial of service / selective forwarding (residual).** A compromised bridge may drop messages or forward selectively. This is a courier risk, not a forgery risk — the bridge cannot produce messages the original author never signed. Detection: subscribers can join the source campfire directly to spot-check delivery.

---

## Hosted-Reader Case Study (Planned — Tier 3.5)

This describes the intended deployment at `reader.getcampfire.dev` once pass-through mode ships (`campfireagent-4a0`).

1. The reader operator creates a long-lived identity, creates reader campfire `R`, and joins target campfire `T` with the observer/blind-relay role.
2. A user who wants to read `T` without joining `T` directly joins `R` instead (a separate trust decision).
3. The reader process calls `protocol.Bridge(ctx, clientFor(T), clientFor(R), T.ID, BridgeOptions{Bidirectional: false})` — with pass-through semantics once implemented.
4. When author `A` publishes message `m` to `T`, the bridge appends a blind-relay hop signed by `R` and writes `m` to `R`'s transport.
5. The user, subscribed to `R`, receives `m` with `Sender = A` (verified Ed25519) and `Provenance = [T_hop, R_hop_blind_relay]`. The UI can display "from A, via reader R."
6. If `R` is compromised, `A`'s messages cannot be forged — `Signature` covers `Payload+Tags+Antecedents` and `R` does not hold `A`'s key. `R` can drop or delay messages; both are detectable by joining `T` directly to spot-check.

In 0.30.0, bridging to a hosted reader produces re-publish mode output (bridge agent is apparent sender). The Tier 3.5 trust model requires pass-through; do not deploy to production until `campfireagent-4a0` ships.
