# Bridge — Transport Relay Model

`protocol.Bridge` pumps messages between two `*Client` instances for a shared campfire ID. The bridge is a layer-1 primitive: it is transport-agnostic (works over filesystem or HTTP) and is the correct tool whenever a campfire's message stream needs to cross a transport boundary.

See `pkg/protocol/bridge.go` (137 LOC) and [`protocol-spec.md §4.2`](../docs/protocol-spec.md) for the wire-level specification.

---

## Modes

`BridgeOptions` carries a `Forward bool` flag that selects between two modes.

**Re-publish mode (`Forward: false`, default).** The bridge calls `dest.Send()` with the source message's payload and tags. The destination receives a fresh message — new `ID`, new `Sender` (the bridge agent), new `Signature`, new hop. Original-author provenance is **not preserved**; the bridging campfire is the apparent author downstream. Use when the bridge is the trust anchor, for example when importing messages from a realm that has no Ed25519 representation (Slack→cf, Teams→cf, email→cf).

**Pass-through mode (`Forward: true`).** The bridge writes the original message envelope to the destination transport unchanged and appends exactly one provenance hop signed by the bridging campfire with `Role = campfire.RoleBlindRelay`. `ID`, `Sender`, `Signature`, `Payload`, `Tags`, `Antecedents`, and all existing `Provenance` hops are preserved. Original-author cryptographic attribution survives end-to-end. The appended hop's `Timestamp` is the time-of-bridge marker. Use whenever upstream and downstream share a signature scheme — for cf→cf relays, hosted-reader deployments (Tier 3.5), and any cross-transport relay where original-author trust must reach the receiver.

**Tier 3.5 hosted-reader deployments MUST use `Forward: true`.** Re-publish at Tier 3.5 would replace every message's apparent author with the hosted-reader campfire, violating the trust-is-chosen property.

---

## When to Use Which

| Scenario | Mode |
|---|---|
| Teams or Slack import into campfire | Re-publish (`Forward: false`) |
| Email gateway into campfire | Re-publish (`Forward: false`) |
| Hosted reader (`reader.getcampfire.dev`) observing a target campfire | Pass-through (`Forward: true`) |
| Cross-transport relay (fs ↔ HTTP) where original-author trust must survive | Pass-through (`Forward: true`) |
| Multi-region message mirroring | Pass-through (`Forward: true`) |

For bidirectional relays, set `Bidirectional: true` in `BridgeOptions`. The bridge runs two goroutines, each direction independent, with a shared dedup map keyed on `msg.ID`.

---

## The Blind-Relay Role

Both modes produce a provenance hop carrying `role: blind-relay`. The role is hop-scoped, not message-scoped.

`IsBridged()` (`pkg/protocol/message.go:51`) returns `true` in both modes — the receiver always knows a bridge was involved. The encryption-key filter (`pkg/campfire/encryption.go:104-127`) excludes blind-relay members from key-chain delivery in both modes; the bridge sees plaintext only if it is an admitted member, not by virtue of being a relay.

Under pass-through, the hop chain is: `[original-campfire-hop, ..., bridging-campfire-blind-relay-hop]`. Three provenance layers survive: author (`Sender`/`Signature`), publisher (first hop), and courier (bridge hop).

Under re-publish, the bridge hop is the only hop. The original author is opaque to the receiver.

---

## What Bridge Does NOT Do

- **Not a federation primitive.** Bridge operates on a single shared campfire ID. It does not link separate campfires or resolve cross-campfire routing. Federation is a higher-layer concern.
- **Does not decrypt or re-sign payloads.** The bridge forwards payload bytes it receives from the transport. It never holds the private keys of the original author and cannot forge or modify signed content.
- **Does not cache.** The bridge is a live pump — it forwards messages as they arrive on the source subscription. It does not replay history or buffer messages for late joiners. The store on the destination side provides durability; the bridge is the delivery mechanism.
- **Does not enforce policy or gates.** The bridge forwards everything it observes (subject to `TagFilter` if set). Convention gates run at the receiving campfire's dispatcher, not inside the bridge.
- **Does not provide provenance loss protection in re-publish mode.** Callers who need original-author attribution must use pass-through mode. Re-publish mode intentionally destroys that attribution — use it only when that is acceptable.

---

## Pass-Through Threat Model

Three attack classes addressed by the pass-through design:

**Forgery via verbatim re-injection (closed).** Without a bridge hop, a hostile relay could replay any message it has observed into any destination campfire with no on-chain evidence of transit. Pass-through still appends a blind-relay hop signed by the bridging campfire. The receiver's `IsBridged()` check fires, and the hop signature names the courier. The original `Sender`/`Signature` is unchanged; the bridge cannot forge messages it never saw.

**Replay across long latencies (closed).** A message originally published at T=0 could be forwarded at T+30d. The appended hop's `Timestamp` (`pkg/message/message.go:169`) records when the bridge delivered the message. Consumers can compute `hop.Timestamp − msg.Timestamp` and reject stale pass-throughs at policy layer. Original message timestamps are not modified.

**Denial of service / selective forwarding (residual).** A compromised bridge may drop messages or forward selectively. This is a courier risk, not a forgery risk — the bridge cannot produce messages the original author never signed. Detection: subscribers can join the source campfire directly to spot-check delivery. The bridge is a courier, not a trust anchor.

---

## Hosted-Reader Case Study

This is the Tier 3.5 deployment at `reader.getcampfire.dev`.

1. The reader operator creates a long-lived identity, creates reader campfire `R`, and joins target campfire `T` with the observer/blind-relay role.
2. A user who wants to read `T` without joining `T` directly joins `R` instead (a separate trust decision).
3. The reader process calls `protocol.Bridge(ctx, clientFor(T), clientFor(R), T.ID, BridgeOptions{Forward: true, Bidirectional: false})`.
4. When author `A` publishes message `m` to `T`, the bridge appends a blind-relay hop signed by `R` and writes `m` to `R`'s transport.
5. The user, subscribed to `R`, receives `m` with `Sender = A` (verified Ed25519) and `Provenance = [T_hop, R_hop_blind_relay]`. The UI can display "from A, via reader R."
6. If `R` is compromised, `A`'s messages cannot be forged — `Signature` covers `Payload+Tags+Antecedents` and `R` does not hold `A`'s key. `R` can drop or delay messages; both are detectable by joining `T` directly to spot-check.
