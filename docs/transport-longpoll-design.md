# HTTP Long Poll Transport Design

**Status:** Draft
**Date:** 2026-03-15
**Author:** Baron + Claude
**Bead:** workspace-7

## Overview

This document specifies the long-poll extension to the P2P HTTP transport
(`pkg/transport/http`). Long poll enables agents behind NAT to receive messages
via outbound connections without running an inbound HTTP listener. It is not a
new transport — it is an additional endpoint and a small set of in-process
wiring changes to the existing `pkg/transport/http` package.

## Motivation

The P2P HTTP transport assumes all members can receive inbound connections. In
practice, agents running on developer laptops, inside corporate NAT, or in
ephemeral CI environments cannot bind a public address. These agents can send
(outbound POST is unrestricted) but cannot receive pushes via `/deliver`.

The protocol spec notes that NAT'd agents "operate in polling mode" and calls
it "a first-class operating mode, not a fallback." Long poll is the efficient
implementation of that mode: instead of periodic blind polls, the NAT'd agent
makes one GET that blocks until messages arrive. Latency is bounded by message
arrival time, not poll interval.

## Design Principles

- **Same package, new endpoint.** `GET /campfire/{id}/poll` is added to the
  existing route table in `handler.go`. No new transport type, no new binary.
- **Sync-then-block.** The poll handler first flushes any messages newer than
  the provided cursor (same query as `/sync`). If there are none, it blocks
  until a message arrives or the timeout fires. This recovers gaps on reconnect
  automatically.
- **Stateless server.** The server holds no persistent state about connected
  pollers. Each poll connection is self-contained. Reconnection carries the
  cursor; the server replays from there. If the server restarts, pollers
  reconnect with their cursor and catch up via the sync-then-block pattern.
- **Reuse existing auth.** Same `X-Campfire-Sender` + `X-Campfire-Signature`
  headers used by all existing endpoints. The signature covers an empty body
  (same convention as `handleSync`).
- **No fan-out changes for direct peers.** Existing `/deliver` push path is
  unchanged. Long poll is a parallel delivery path for peers that registered
  with an empty endpoint.

## Endpoint Specification

### `GET /campfire/{id}/poll`

**Request headers:**

```
X-Campfire-Sender:    <hex Ed25519 public key of the poller>
X-Campfire-Signature: <base64 Ed25519 signature of empty body>
```

**Query parameters:**

| Parameter | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `since`   | int64  | No       | Nanosecond timestamp (ReceivedAt). Return messages with ReceivedAt > since. Defaults to 0 (full history). |
| `timeout` | int    | No       | Max hold time in seconds. Defaults to 30. Server cap: 120. |

**Success response (messages available, possibly after waiting):**

```
HTTP/1.1 200 OK
Content-Type: application/cbor
X-Campfire-Cursor: <nanosecond ReceivedAt of the last returned message>

<CBOR-encoded []message.Message>
```

The `X-Campfire-Cursor` response header carries the ReceivedAt timestamp of the
newest message in the batch. The poller uses this as `since` on reconnect.

**Timeout response (no messages within the hold window):**

```
HTTP/1.1 204 No Content
X-Campfire-Cursor: <same since value the poller sent>
```

A 204 is not an error. The poller reconnects immediately with the same cursor.

**Error responses:**

| Status | Condition |
|--------|-----------|
| 401    | Missing or invalid signature headers |
| 403    | Sender is not a member of this campfire |
| 400    | Invalid `since` or `timeout` parameter |
| 503    | Too many pollers for this campfire (per-campfire limit exceeded) |

### Membership Check

Every request to `/campfire/{id}/poll` must verify that the sender is a current
member of the campfire. The handler calls `store.GetMembership(campfireID)` and
then checks whether the sender's public key appears in the member set.

This is a new check not present in the existing `/sync` endpoint. The existing
`/sync` gap (no membership verification) is a pre-existing issue and out of
scope for this design. Long poll is specified with the correct security posture
from the start.

## Internal Fan-out Architecture

### The Problem

`handleDeliver` stores messages to SQLite and returns. Poll handlers blocked
waiting for the same campfire need to be woken when a new message arrives. This
requires in-process signaling between `handleDeliver` and active poll goroutines.

### Solution: PollBroker

A new `PollBroker` type is added to `pkg/transport/http`. It is embedded in
`Transport` and initialized in `New()`.

```
PollBroker
  mu       sync.Mutex
  subs     map[campfireID][]chan struct{}   // notification channels, one per waiter
  limits   map[campfireID]int              // active poller count per campfire
```

`handleDeliver` calls `broker.Notify(campfireID)` after a successful store
write. `handlePoll` registers a channel, blocks on it (with timeout), and
deregisters on return. The channel carries only a signal — no data. The poller
queries the store after wakeup to retrieve the actual messages.

**Why channels, not sync.Cond?**
`select` with a timeout channel is simpler and avoids the `Cond.Wait` /
`Cond.Broadcast` race window. Each poller owns its channel; `Notify` closes or
sends to each registered channel under the broker's lock. Closed channels
cannot be reused — a new channel is allocated per poll request.

**Notify semantics:**
`Notify` sends a non-blocking signal to every registered channel for the
campfire. Pollers that are not yet blocked (still in the sync-then-block
handoff) will miss the notification but will pick up the message in the initial
sync query on their next reconnect.

### PollBroker API (internal)

```go
// Subscribe registers a notification channel for campfireID.
// Returns the channel and a deregister function. Returns error if limit exceeded.
func (b *PollBroker) Subscribe(campfireID string) (<-chan struct{}, func(), error)

// Notify wakes all subscribers for campfireID. Called by handleDeliver.
func (b *PollBroker) Notify(campfireID string)
```

### Per-Campfire Poller Limit

Default limit: 64 pollers per campfire per server instance. Configurable via
`Transport` options. Requests that exceed the limit return HTTP 503.

Rationale: each poller holds a goroutine and a response writer. 64 per campfire
is generous for the expected use case (small agent clusters) while preventing
trivial resource exhaustion.

### Overall Connection Limits

The `http.Server` already imposes connection limits via `MaxHeaderBytes` and the
OS TCP backlog. Long poll connections are counted by the OS toward the process's
open file descriptor limit. No additional server-level limit is added in this
design — the per-campfire limit and the OS fd limit are the effective bounds.

## Peer Registration: Poll Mode vs. Direct Mode

When a NAT'd agent joins, it sends `JoinerEndpoint: ""` in the `JoinRequest`.
The admitting member skips the endpoint storage step (`req.JoinerEndpoint == ""`
branch in `handleJoin`). The NAT'd agent is a member but has no push address.

After joining, the NAT'd agent selects a reachable peer from the `Peers` list in
the `JoinResponse` and establishes a poll connection. The peer does not need to
know the agent is polling — it sees a normal authenticated GET and holds the
connection.

No new registration step is required. The join flow already handles the empty
endpoint case. The agent's decision to poll vs. receive pushes is local.

**Peer list representation for pollers:**
When the transport builds the peer list for fan-out (determining where to
`Deliver`), peers with empty endpoints are skipped — they receive via poll.
This is the existing behavior in `DeliverToAll` when a peer has no endpoint.
No change required.

## Client API

A new function in `peer.go`:

```go
// Poll makes a long-poll request to endpoint and returns when messages are
// available or the server-side timeout fires. Returns (nil, nil) on timeout
// (204 response). cursor is the ReceivedAt nanosecond timestamp to resume from;
// pass 0 for full history.
//
// On success, the returned cursor is the ReceivedAt of the newest returned
// message. Pass it as cursor on the next call.
func Poll(endpoint, campfireID string, cursor int64, timeoutSecs int, id *identity.Identity) ([]message.Message, int64, error)
```

The caller is responsible for the reconnect loop. A typical polling agent:

```
cursor := lastKnownCursor  // persisted across restarts
for {
    msgs, newCursor, err := Poll(endpoint, campfireID, cursor, 30, id)
    if err != nil { backoff(); continue }
    if len(msgs) > 0 {
        processMessages(msgs)
        cursor = newCursor
        persistCursor(cursor)
    }
    // 204: reconnect immediately with same cursor
}
```

## Reconnection and Gap Recovery

The sync-then-block pattern handles gap recovery without any special logic:

1. Poller sends `since=<last cursor>`.
2. Server queries `store.ListMessages(campfireID, since)` immediately.
3. If messages exist, they are returned immediately (no blocking). The poller
   processes them, updates its cursor, and reconnects.
4. If no messages exist, the server blocks. When a new message arrives, it
   returns immediately.

This means a poller that was disconnected for an hour reconnects and drains the
backlog in rapid-fire 200 responses before settling into blocking mode. The
transition from catch-up to live is seamless and automatic.

**Cursor precision and deduplication:**
The cursor is `ReceivedAt` in nanoseconds. Two messages stored within the same
nanosecond would both be returned on reconnect after the first one was processed.
Pollers must deduplicate by message ID. The store's `AddMessage` is already
idempotent by message ID, so a duplicate deliver is a no-op on the receiving
end.

## Interaction with Existing Endpoints

### `/sync`

Sync and poll serve identical data (same `store.ListMessages` query). They are
complementary:
- **`/sync`**: one-shot, for catching up or for agents that prefer polling on
  their own schedule.
- **`/poll`**: blocking, for agents that want minimal latency without a timer.

An agent may use both: poll for live delivery, sync as a fallback if the poll
connection drops and the agent suspects it missed messages.

### `/deliver`

`handleDeliver` gains one new call after the store write:
```go
h.transport.pollBroker.Notify(campfireID)
```

No other changes to the deliver path.

### `cf join`

No changes. NAT'd agents already send `JoinerEndpoint: ""`. The join response
already includes a peer list. The agent chooses a poll target from that list.

### `cf send`

No changes. Sending is always an outbound POST to `/deliver` on reachable peers.
NAT does not affect sending.

### `cf read`

`cf read` currently reads from the local store. For NAT'd agents (no inbound
listener), messages are not pushed via `/deliver` — they arrive only via poll.
The implementation of `cf read` needs a polling mode:

- If the agent has an active listener (`selfEndpoint != ""`): messages arrive
  via `/deliver`, read from store as today.
- If no listener (`selfEndpoint == ""`): spawn a poll loop against a known peer
  before reading from store, or block on poll and stream results to stdout.

This is a `cf read` implementation concern, not a transport protocol change.
The transport provides `Poll()`. The CLI layer decides when to call it.

### `cf serve`

NAT'd agents do not run `cf serve` for the campfire's push path. They have no
bindable address to advertise. The `cf serve` command remains relevant for
direct peers that serve as poll targets.

## Security Analysis

### Authentication

Every poll request presents `X-Campfire-Sender` and `X-Campfire-Signature`
headers. `verifyRequestSignature` is called with an empty body (same convention
as `handleSync`). This proves the requester holds the Ed25519 private key
corresponding to the claimed public key.

Membership is then verified: the sender's public key must appear in the campfire's
member list. This prevents any authenticated agent (valid keypair, not a member)
from receiving campfire messages.

### DoS: Connection Exhaustion

Holding connections open is the inherent cost of long poll. Mitigations:
- Per-campfire limit (64 pollers by default, 503 on breach).
- Server-side timeout cap (120 seconds). Pollers must reconnect periodically.
- Signature verification on every request prevents unauthenticated connection
  holding.
- The OS fd limit provides a hard ceiling across all campfires on the server.

A member can register at most one effective poll connection per campfire (they
have one identity). An attacker with N forged identities could open N connections,
but each forged identity would fail membership verification — so the DoS
amplifier is bounded by actual campfire membership size.

### Message Confidentiality

The poll stream carries plaintext CBOR messages over HTTP. Confidentiality
depends on transport-layer encryption (TLS). Campfire's HTTP transport is
specified to use HTTPS in production deployments. This design does not change
that requirement.

For end-to-end confidentiality within a campfire, message payload encryption
(encrypt payload to recipient public key) is an application concern noted in
the protocol spec. The transport carries whatever bytes the application provides.

### Cursor Manipulation

A poller could send `since=0` to request the full message history. This is the
same behavior available via `/sync?since=0`. No new attack surface. The server
returns whatever the store has for the campfire, bounded by its retention policy.

If full history access is a concern, it should be addressed at the store/retention
layer (message TTL), not at the poll layer.

### Membership Spoofing

The poll handler verifies membership on every request. A non-member with a valid
Ed25519 keypair will receive 403. A member that has been evicted will also receive
403 (membership check is against current state). Eviction takes effect on the
next poll request — held connections are not forcibly terminated on eviction, but
will not be renewed.

**Recommendation:** When `handleMembership` processes an eviction event, call
`broker.NotifyEviction(campfireID, memberPubKey)`. The broker closes any channels
held by that member's poll connections, causing their goroutines to return without
sending messages. This is a nice-to-have; correctness holds without it because the
poller reconnects and receives 403.

## Failure Modes

### Write Failure During Stream

If the write to `http.ResponseWriter` fails mid-stream (client dropped), the
handler goroutine receives a write error and returns. The deregister function
removes the subscription from the broker. The client reconnects with its last
good cursor.

Because all messages in a batch are written together (one CBOR marshal of the
full slice), partial delivery is not possible within a batch. Either the full
batch is written or none of it is. The cursor is in the response header, which
is written before the body — a header-only partial write would cause the client
to reconnect with the same cursor and receive the same messages again. Deduplication
by message ID handles this.

### Multiple Pollers for the Same Campfire on the Same Server

Each is an independent goroutine. `broker.Notify` wakes all of them. Each reads
from the store independently. There is a minor TOCTOU window: between Notify and
the store read, another message could arrive. The next poll will catch it.

No ordering guarantee is added beyond what the store provides: ReceivedAt order,
which is wall-clock order at the receiving peer.

### Server Restart

All broker state is in-memory. On restart, all poll connections are dropped. Pollers
reconnect and resume from their cursors. No server-side recovery needed.

### Network Partition and Recovery

From the poller's perspective: the TCP connection is dropped. The reconnect loop
retries with the last cursor. Once connectivity resumes, the sync-then-block
pattern catches up the gap automatically.

From the serving peer's perspective: it sees a dropped connection and cleans up
the goroutine. No state to recover.

### Peer Unavailability

A NAT'd agent polls a specific peer. If that peer goes down, the poller must
select an alternative peer from its known peer list. The `cf read` polling loop
should implement peer selection with fallback: try the preferred peer, on failure
rotate to the next peer in the list.

This is a client-side concern. The transport's `Poll()` function is single-peer.
The caller iterates the peer list on error.

## Ordering Guarantees

Messages are returned in `ReceivedAt` order (ascending). This is wall-clock time
at the receiving peer, not message send time (the message's `timestamp` field is
sender wall clock, not authoritative per the spec). No causal ordering is
guaranteed across different senders.

This matches the behavior of `/sync` and the existing protocol. The open question
about message ordering in the spec remains open; long poll does not resolve or
worsen it.

## Summary of Changes

### New: `pkg/transport/http/poll_broker.go`

- `PollBroker` struct with `Subscribe`, `Notify`, `NotifyEviction` methods.
- Per-campfire channel registry, per-campfire limits.

### Modified: `pkg/transport/http/transport.go`

- Add `pollBroker *PollBroker` field to `Transport`.
- Initialize in `New()`.

### Modified: `pkg/transport/http/handler.go`

- Add `case action == "poll" && r.Method == http.MethodGet:` to `route()`.
- Add `handlePoll(w, r, campfireID)` method.
- In `handleDeliver`: call `h.transport.pollBroker.Notify(campfireID)` after
  successful store write.

### Modified: `pkg/transport/http/peer.go`

- Add `Poll()` function.

### No changes to:

- Protocol spec (`docs/protocol-spec.md`). Long poll is an implementation detail
  of the p2p-http transport, not a protocol change.
- Store (`pkg/store`). No schema changes required; `/poll` uses existing
  `ListMessages` query.
- Other transport packages (`pkg/transport/fs`, `pkg/transport/unix`).
- CLI commands beyond `cf read` behavior (polling mode is an implementation
  detail of what `cf read` calls).

## Open Questions

1. **Should the cursor use `ReceivedAt` or message `ID` (UUID)?** UUID ordering
   is not temporal. `ReceivedAt` (nanosecond int64) is consistent with `/sync`
   and requires no schema change. Recommendation: keep `ReceivedAt`.

2. **Should poll connections be terminated on eviction?** Current design: no
   (403 on reconnect is sufficient). The `NotifyEviction` path is a
   nice-to-have. Defer to implementation.

3. **Should membership verification be backported to `/sync`?** It should.
   That is a separate fix, out of scope for this bead.

4. **What is the right per-campfire poller limit?** 64 is a guess. The right
   number depends on expected campfire sizes. For the dogfood use case (small
   agent clusters, < 10 members), even 10 would be sufficient. 64 provides
   headroom. Make it configurable.

5. **Should `cf read` in poll mode stream messages to stdout as they arrive
   (long-lived process) or batch and exit?** Depends on the use case. Agent
   processes will want a long-lived loop. Human users may prefer batch. A
   `--follow` flag (like `tail -f`) is the standard convention.
