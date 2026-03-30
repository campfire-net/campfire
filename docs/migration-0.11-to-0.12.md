# Migration Guide: SDK 0.11 → 0.12

This guide covers every breaking change in the campfire Go SDK between v0.11 and v0.12. Known consumers: galtrader (`campfire_sdk.go:88`), clankeros (`bridge.go:159`).

---

## 1. `store.MessageRecord` → `protocol.Message`

`Read`, `Await`, and `Subscribe` previously returned `store.MessageRecord` values. In 0.12, they return `protocol.Message` instead. The `store` import is no longer needed in most consumer code.

### Field mapping

| `store.MessageRecord` (0.11) | `protocol.Message` (0.12) | Notes |
|------------------------------|--------------------------|-------|
| `ID` | `ID` | unchanged |
| `CampfireID` | `CampfireID` | unchanged |
| `Sender` | `Sender` | unchanged — hex pubkey string |
| `Payload` | `Payload` | unchanged |
| `Tags` | `Tags` | unchanged |
| `Antecedents` | `Antecedents` | unchanged |
| `Timestamp` | `Timestamp` | unchanged |
| `Signature` | `Signature` | unchanged |
| `Provenance` | `Provenance` | unchanged — `[]message.ProvenanceHop` |
| `Instance` | `Instance` | unchanged |
| `ReceivedAt` | _(removed)_ | store-internal field; not present on `protocol.Message` |

**0.11:**

```go
result, err := client.Read(protocol.ReadRequest{CampfireID: id})
for _, rec := range result.Messages { // []store.MessageRecord
    fmt.Println(rec.ID, string(rec.Payload))
}
```

**0.12:**

```go
result, err := client.Read(protocol.ReadRequest{CampfireID: id})
for _, msg := range result.Messages { // []protocol.Message
    fmt.Println(msg.ID, string(msg.Payload))
}
```

If you need a `store.MessageRecord` from a `protocol.Message` (e.g. for direct store writes), use the inverse helper: `store.MessageRecordFromMessage`. If you previously used `ReceivedAt`, that field is internal to the store and has no equivalent on `protocol.Message`.

---

## 2. `CreateRequest` flat fields → typed `Transport`

`CreateRequest` and `JoinRequest` previously used flat string fields (`TransportDir`, `TransportType`) plus loose P2P-HTTP fields (`HTTPTransport`, `MyHTTPEndpoint`). In 0.12, all transport configuration is expressed through a single `Transport` field holding one of three typed structs.

**0.11:**

```go
// Filesystem transport
result, err := client.Create(protocol.CreateRequest{
    TransportType: "filesystem",
    TransportDir:  "/var/campfires",
})

// P2P HTTP transport
result, err := client.Create(protocol.CreateRequest{
    TransportType:  "p2p-http",
    TransportDir:   "/tmp/p2p-state",
    HTTPTransport:  httpTransport,
    MyHTTPEndpoint: "http://10.0.0.1:9000",
})

// GitHub transport
result, err := client.Create(protocol.CreateRequest{
    TransportType: "github",
    TransportDir:  `github:{"repo":"owner/repo","issue_number":42}`,
})
```

**0.12:**

```go
// Filesystem transport
result, err := client.Create(protocol.CreateRequest{
    Transport: protocol.FilesystemTransport{Dir: "/var/campfires"},
})

// P2P HTTP transport
result, err := client.Create(protocol.CreateRequest{
    Transport: protocol.P2PHTTPTransport{
        Transport:  httpTransport,
        MyEndpoint: "http://10.0.0.1:9000",
        Dir:        "/tmp/p2p-state", // optional; temp dir if empty
    },
})

// GitHub transport
result, err := client.Create(protocol.CreateRequest{
    Transport: protocol.GitHubTransport{
        Owner:  "owner",
        Repo:   "repo",
        Branch: "main",
        Dir:    "campfires/",
        Token:  os.Getenv("GITHUB_TOKEN"),
    },
})
```

The same change applies to `JoinRequest`. Replace `TransportDir`/`TransportType`/`HTTPTransport`/`MyHTTPEndpoint`/`PeerHTTPEndpoint` with the corresponding typed struct.

---

## 3. `Store()` / `Identity()` → `PublicKeyHex()`

`protocol.Client` previously exported `Store()` and `Identity()` accessors. Both are removed in 0.12. The only public accessor for the identity is `PublicKeyHex()`, which returns the hex-encoded Ed25519 public key (or empty string for read-only clients).

**0.11:**

```go
pubkey := client.Identity().PublicKeyHex()
```

**0.12:**

```go
pubkey := client.PublicKeyHex()
```

If you previously used `client.Store()` to access the underlying store for operations not covered by the `Client` API, those operations now need to go through the `Client` methods. File a request against the campfire repo if a needed operation is missing from the `Client` surface.

---

## 4. `ReadResult.Messages` type change

`ReadResult.Messages` changed from `[]store.MessageRecord` to `[]protocol.Message`. This is the same type change described in §1 — if you iterate `result.Messages`, update the element type in your range variable (or use `_` and access fields directly).

**0.11:**

```go
var result *protocol.ReadResult // Messages []store.MessageRecord
for _, rec := range result.Messages {
    process(rec.Tags, rec.Payload)
}
```

**0.12:**

```go
var result *protocol.ReadResult // Messages []protocol.Message
for _, msg := range result.Messages {
    process(msg.Tags, msg.Payload)
}
```

---

## 5. `Await` return type change

`Await` previously returned `*store.MessageRecord`. It now returns `*protocol.Message`.

**0.11:**

```go
rec, err := client.Await(protocol.AwaitRequest{
    CampfireID:  campfireID,
    TargetMsgID: sentMsgID,
    Timeout:     30 * time.Second,
})
if err != nil { ... }
fmt.Println(rec.ID, string(rec.Payload))
```

**0.12:**

```go
msg, err := client.Await(protocol.AwaitRequest{
    CampfireID:  campfireID,
    TargetMsgID: sentMsgID,
    Timeout:     30 * time.Second,
})
if err != nil { ... }
fmt.Println(msg.ID, string(msg.Payload))
```

The field names are identical (see §1 field mapping). Only the package and the absence of `ReceivedAt` differs.

---

## 6. `Subscription` channel type change

`Subscription.Messages()` previously returned `<-chan store.MessageRecord`. It now returns `<-chan protocol.Message`.

**0.11:**

```go
sub := client.Subscribe(ctx, protocol.SubscribeRequest{CampfireID: id})
for rec := range sub.Messages() { // store.MessageRecord
    handle(rec)
}
```

**0.12:**

```go
sub := client.Subscribe(ctx, protocol.SubscribeRequest{CampfireID: id})
for msg := range sub.Messages() { // protocol.Message
    handle(msg)
}
```

The `Subscription.Err()` method and the context-cancellation semantics are unchanged.

---

## 7. `IsBridged()` helper (new — replaces manual provenance checking)

In 0.11, detecting bridged messages (e.g. messages relayed from Teams or Slack via a blind-relay hop) required manually inspecting `Provenance` hops:

**0.11:**

```go
func isBridged(rec store.MessageRecord) bool {
    for _, hop := range rec.Provenance {
        if hop.Role == campfire.RoleBlindRelay {
            return true
        }
    }
    return false
}
```

**0.12:**

```go
if msg.IsBridged() {
    // message came through a blind-relay bridge
}
```

`IsBridged()` is a method on `protocol.Message`. It returns `true` if any provenance hop carries the `campfire.RoleBlindRelay` role. Remove the manual loop and the direct `campfire` import where the only use was this check.

---

## Summary of changes

| Area | 0.11 | 0.12 |
|------|------|------|
| Message type in Read/Await/Subscribe | `store.MessageRecord` | `protocol.Message` |
| `ReadResult.Messages` element type | `store.MessageRecord` | `protocol.Message` |
| `Await` return type | `*store.MessageRecord` | `*protocol.Message` |
| `Subscription.Messages()` element type | `store.MessageRecord` | `protocol.Message` |
| Transport config in Create/Join | flat `TransportDir`/`TransportType` + loose fields | typed `Transport` interface (`FilesystemTransport`, `P2PHTTPTransport`, `GitHubTransport`) |
| Identity/store accessors | `client.Store()`, `client.Identity()` | removed; use `client.PublicKeyHex()` |
| Bridge detection | manual `Provenance` loop | `msg.IsBridged()` |
