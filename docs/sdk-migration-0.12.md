# SDK 0.12 Migration Guide

Upgrading from SDK 0.11 to 0.12 requires updating code that calls `Create`, `Join`,
`Admit`, `Evict`, `Read`, `Await`, or `Subscribe`, and any code that accesses
`Store` or `Identity` directly on `*Client`.

## Breaking changes

### 1. Typed transport configs replace string-keyed maps

**0.11** — transport was passed as a `map[string]any` or via legacy string flags.

**0.12** — pass a typed config struct. The `Transport` interface is sealed:
only `FilesystemTransport`, `P2PHTTPTransport`, and `GitHubTransport` are accepted.

```go
// Before (0.11)
client.Create(protocol.CreateRequest{
    TransportType: "filesystem",
    TransportDir:  "/path/to/campfires",
})

// After (0.12)
client.Create(protocol.CreateRequest{
    Transport: protocol.FilesystemTransport{Dir: "/path/to/campfires"},
})
```

The same applies to `JoinRequest`, `AdmitRequest`, and `EvictRequest`.

```go
// P2P HTTP
protocol.P2PHTTPTransport{
    Transport:    httpTransport,   // *cfhttp.Transport, already started
    MyEndpoint:   "http://host:9001",
    PeerEndpoint: "http://peer:9001", // required for Join only
}

// GitHub
protocol.GitHubTransport{
    Owner: "org", Repo: "repo", Branch: "main",
    Dir: "campfires/", Token: os.Getenv("GITHUB_TOKEN"),
}
```

### 2. `Read`, `Await`, and `Subscribe` return `protocol.Message`

**0.11** — these methods returned `store.MessageRecord`.

**0.12** — they return `protocol.Message` (or `[]protocol.Message`), which adds
the `IsBridged()` helper and a stable public API surface.

```go
// Before (0.11) — result.Messages was []store.MessageRecord
for _, r := range result.Messages {
    fmt.Println(r.Sender, string(r.Payload))
}

// After (0.12) — result.Messages is []protocol.Message
for _, m := range result.Messages {
    fmt.Println(m.Sender, string(m.Payload))
    if m.IsBridged() {
        // message came through a blind-relay (Teams, Slack, etc.)
    }
}
```

`Await` similarly returns `*protocol.Message` instead of `*store.MessageRecord`.

`Subscribe` channels now emit `protocol.Message` values.

### 3. `Store` and `Identity` fields removed from `*Client`

**0.11** — `client.Store` and `client.Identity` were exported fields.

**0.12** — these are unexported. Access the public key via `PublicKeyHex()`:

```go
// Before (0.11)
pubKey := client.Identity.PublicKeyHex()

// After (0.12)
pubKey := client.PublicKeyHex() // returns "" for read-only clients
```

If you need the store for low-level access, use `client.ClientStore()` (returns
`store.Store`) — but prefer the `Client` methods for all standard operations.

## New features in 0.12

### `protocol.Message` type

SDK-facing message with `IsBridged()` helper:

```go
type Message struct {
    ID          string
    CampfireID  string
    Sender      string   // hex-encoded Ed25519 public key
    Payload     []byte
    Tags        []string
    Antecedents []string
    Timestamp   int64
    Instance    string   // tainted (sender-asserted) role label
    Signature   []byte
    Provenance  []message.ProvenanceHop
}

func (m *Message) IsBridged() bool
```

`IsBridged()` returns true if any provenance hop carries the `blind-relay` role —
meaning the message was bridged from an external system (Teams, Slack, etc.).

### `client.Get` and `client.GetByPrefix`

Targeted single-message lookups from the local store:

```go
msg, err := client.Get(messageID)         // exact ID — nil, nil if not found
msg, err := client.GetByPrefix("a1b2c3") // prefix — error if ambiguous
```

### `client.PublicKeyHex()`

Returns the hex-encoded public key of the client's identity:

```go
pubKey := client.PublicKeyHex() // pass to convention.NewExecutor, etc.
```

### `CreateRequest.Description` and `CreateResult.BeaconID`

Optional human-readable description stored in the membership record:

```go
result, err := client.Create(protocol.CreateRequest{
    Transport:   protocol.FilesystemTransport{Dir: dir},
    Description: "my coordination campfire",
})
fmt.Println(result.BeaconID) // hex beacon ID — equals CampfireID
```

### Transport interface

```go
type Transport interface {
    TransportType() string
}
```

`FilesystemTransport`, `P2PHTTPTransport`, and `GitHubTransport` all satisfy this
interface. Pass any of them to `CreateRequest.Transport` or `JoinRequest.Transport`.

## Dual-loop pattern

SDK 0.12 documents the dual-loop pattern for running `convention.Server` and
`Subscribe` concurrently in the same process. Because `Client` is not safe for
concurrent use, two separate `Client` instances are required — both backed by the
same `configDir`.

See [convention-sdk.md — Dual-loop pattern](convention-sdk.md#dual-loop-pattern-subscribe--conventionserver-concurrently) for the full example.

## Quick checklist

- [ ] Replace `TransportType`/`TransportDir` map fields with typed transport structs
- [ ] Update `Read`/`Await`/`Subscribe` callers: `store.MessageRecord` → `protocol.Message`
- [ ] Replace `client.Identity.PublicKeyHex()` with `client.PublicKeyHex()`
- [ ] Remove direct `client.Store` / `client.Identity` field access
- [ ] Pass `client.PublicKeyHex()` to `convention.NewExecutor`
