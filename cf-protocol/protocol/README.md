# cf-protocol/protocol

Package `protocol` provides the unified client API for campfire operations.

## Overview

`protocol.Client` is the primary API surface for all campfire operations. Init,
Create, Join, Send, Read, Subscribe, Await, Admit, Evict, Leave, Disband, and
Members are all methods on `*Client`.

## Quickstart

```go
// Initialize a client (creates identity + store under configDir).
client, result, err := protocol.InitWithConfig(protocol.WithConfigDir("/path/to/.cf"))
if err != nil {
    log.Fatal(err)
}
defer client.Close()

// Create a campfire.
created, err := client.Create(protocol.CreateRequest{
    Transport: protocol.FilesystemTransport{Dir: "/path/to/transport"},
})

// Send a message.
_, err = client.Send(protocol.SendRequest{
    CampfireID: created.CampfireID,
    Payload:    []byte(`{"msg":"hello"}`),
    Tags:       []string{"status"},
})

// Read messages.
result, err := client.Read(protocol.ReadRequest{
    CampfireID: created.CampfireID,
})
for _, msg := range result.Messages {
    fmt.Println(string(msg.Payload))
}

// Subscribe (streaming).
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
sub := client.Subscribe(ctx, protocol.SubscribeRequest{CampfireID: created.CampfireID})
for msg := range sub.Messages() {
    fmt.Println(string(msg.Payload))
}
```

## Godoc

Full API reference: https://pkg.go.dev/github.com/campfire-net/campfire/cf-protocol/protocol

## Demo scripts

- `cf-conventions/demos/sdk-doc-coverage.sh` — verify 100% doc + Example_ coverage
- `cf-conventions/demos/conventions-idioms-walkthrough.sh` — end-to-end convention usage

## Key types

| Type | Description |
|------|-------------|
| `Client` | The primary API client |
| `Message` | SDK-facing campfire message |
| `SendRequest` | Parameters for `Client.Send` |
| `ReadRequest` | Parameters for `Client.Read` |
| `SubscribeRequest` | Parameters for `Client.Subscribe` |
| `CreateRequest` / `CreateResult` | Campfire creation |
| `JoinRequest` / `JoinResult` | Joining a campfire |
| `AdmitRequest` | Pre-admitting a member |
| `EvictRequest` / `EvictResult` | Evicting a member |
| `FilesystemTransport` | Local filesystem transport |
| `P2PHTTPTransport` | P2P HTTP transport |
| `InitResult` | Initialization metadata |
| `ScopeConfig` | Operation/campfire access control |

## Related packages

- `cf-conventions/cf-convention` — convention server SDK (typed operations)
- `cf-protocol/internal/store` — SQLite store (re-exported via `protocol.Store`)
- `cf-protocol/internal/campfire` — campfire primitives (re-exported via `protocol.Campfire`)
