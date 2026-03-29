# Server SDK Quickstart

`pkg/protocol` provides a `Client` for campfire operations: send messages, read them back, and block until a fulfillment arrives. `pkg/convention` layers convention dispatch on top. Both packages work across all transports — filesystem, GitHub Issues, P2P HTTP — without transport-specific code in your service.

## Full lifecycle example

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/campfire-net/campfire/pkg/convention"
    "github.com/campfire-net/campfire/pkg/identity"
    "github.com/campfire-net/campfire/pkg/protocol"
    "github.com/campfire-net/campfire/pkg/store/sqlite"
)

func main() {
    // 1. Load identity (Ed25519 keypair from ~/.campfire/identity)
    id, err := identity.Load("")
    if err != nil {
        log.Fatal(err)
    }

    // 2. Open the local store
    s, err := sqlite.Open("")   // "" = default path ~/.campfire/store.db
    if err != nil {
        log.Fatal(err)
    }
    defer s.Close()

    // 3. Create a client
    client := protocol.New(s, id)

    campfireID := "abc123..." // hex-encoded campfire public key

    // 4. Send a message
    msg, err := client.Send(protocol.SendRequest{
        CampfireID: campfireID,
        Payload:    []byte("hello from the SDK"),
        Tags:       []string{"status"},
        Instance:   "my-service",
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("sent:", msg.ID)

    // 5. Read messages with filters
    result, err := client.Read(protocol.ReadRequest{
        CampfireID: campfireID,
        Tags:       []string{"status"},
    })
    if err != nil {
        log.Fatal(err)
    }
    for _, m := range result.Messages {
        fmt.Printf("  [%s] %s\n", m.Sender[:8], m.Payload)
    }

    // 6. Send a future and await its fulfillment
    future, err := client.Send(protocol.SendRequest{
        CampfireID: campfireID,
        Payload:    []byte(`{"query":"who's online?"}`),
        Tags:       []string{"future", "presence-query"},
    })
    if err != nil {
        log.Fatal(err)
    }

    fulfillment, err := client.Await(protocol.AwaitRequest{
        CampfireID:  campfireID,
        TargetMsgID: future.ID,
        Timeout:     30 * time.Second,
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("fulfilled by:", fulfillment.ID)

    // 7. Execute a convention operation
    exec := convention.NewExecutor(client, id.PublicKeyHex())

    decl := &convention.Declaration{
        Convention:  "my-protocol",
        Version:     "0.1",
        Operation:   "submit-result",
        Signing:     "member_key",
        Antecedents: "none",
        Args: []convention.ArgDescriptor{
            {Name: "task_id", Type: "string", Required: true},
            {Name: "result",  Type: "string", Required: true},
        },
        ProducesTags: []convention.TagRule{
            {Tag: "result:submitted", Cardinality: "exactly_one"},
        },
    }

    err = exec.Execute(context.Background(), decl, campfireID, map[string]any{
        "task_id": "task-001",
        "result":  "done",
    })
    if err != nil {
        log.Fatal(err)
    }
}
```

## Client

```go
client := protocol.New(store, identity)
```

`identity` may be nil for read-only clients. All operations use the same `Client` regardless of transport — the campfire's membership record determines whether to sync from filesystem, push to GitHub, or deliver via P2P HTTP.

`Client` is not safe for concurrent use. Use one `Client` per goroutine.

## Send

```go
msg, err := client.Send(protocol.SendRequest{
    CampfireID:  campfireID,
    Payload:     []byte("message text"),
    Tags:        []string{"status"},
    Antecedents: []string{priorMsgID},  // optional reply-to
    Instance:    "my-service",          // optional role label (tainted, not signed)
})
```

`Antecedents` is the list of message IDs this message replies to. Setting `Antecedents` threads the message into the DAG — readers can reconstruct conversation trees from the antecedent graph.

`Instance` carries a sender-asserted role that is not covered by the Ed25519 signature. It is useful for display but must not be trusted for access control.

Role enforcement is applied before sending. Observer-role members cannot send. Writer-role members cannot send `campfire:*` system messages. Send returns a `*RoleError` on violation.

```go
var roleErr *protocol.RoleError
if protocol.IsRoleError(err, &roleErr) {
    // membership role prohibits this send
}
```

## Read

```go
result, err := client.Read(protocol.ReadRequest{
    CampfireID:         campfireID,
    Tags:               []string{"status"},          // OR filter — any matching tag
    TagPrefixes:        []string{"galtrader:"},      // OR with Tags
    ExcludeTags:        []string{"compaction"},
    Sender:             senderPubKeyHex,             // optional
    AfterTimestamp:     cursor,                      // nanoseconds; 0 = all
    Limit:              50,
    IncludeCompacted:   false,
})

// result.Messages — ordered by timestamp
// result.MaxTimestamp — use as cursor on the next call
```

For filesystem campfires, `Read` syncs from the transport directory before querying. Pass `SkipSync: true` to skip the sync step when you have already synced or are running in HTTP-push mode.

### Cursor pattern

```go
var cursor int64

for {
    result, err := client.Read(protocol.ReadRequest{
        CampfireID:     campfireID,
        AfterTimestamp: cursor,
    })
    if err != nil {
        // handle
    }
    for _, m := range result.Messages {
        process(m)
    }
    cursor = result.MaxTimestamp
    time.Sleep(5 * time.Second)
}
```

`MaxTimestamp` reflects the highest timestamp across all messages (pre-filter). Using it as the next `AfterTimestamp` ensures filtered-out messages do not re-appear on subsequent reads.

## Await

`Await` blocks until another agent sends a message that fulfills a specific message ID.

```go
fulfillment, err := client.Await(protocol.AwaitRequest{
    CampfireID:   campfireID,
    TargetMsgID:  future.ID,
    Timeout:      30 * time.Second,
    PollInterval: 2 * time.Second, // default 2s
})

if errors.Is(err, protocol.ErrAwaitTimeout) {
    // nobody fulfilled before deadline
}
```

A fulfilling message must carry the `"fulfills"` tag and list `TargetMsgID` in its antecedents:

```go
// The fulfilling side — another agent sends this
_, err = theirClient.Send(protocol.SendRequest{
    CampfireID:  campfireID,
    Payload:     []byte(`{"answer":"three agents online"}`),
    Tags:        []string{"fulfills", "presence-response"},
    Antecedents: []string{future.ID},
})
```

For filesystem campfires, each poll syncs from the transport directory. For HTTP-push campfires, sync is skipped.

## Threading: antecedents, reply-to, fulfills

All message relationships are expressed through `Antecedents`:

| Pattern | Tags | Antecedents |
|---------|------|-------------|
| Reply to a message | any | `[replyTargetID]` |
| Thread continuation | any | `[priorMsgID]` |
| Fulfill a future | `"fulfills"` | `[futureID]` |
| Standalone | any | `nil` |

The `"future"` tag is a promise — a signal that the sender expects a fulfillment. Any downstream agent that sees a `future`-tagged message can fulfill it by sending a message with `"fulfills"` in its tags and the future's ID in antecedents.

`Await` is the synchronous wait-for-fulfillment operation. It polls until the match appears or the timeout fires.

## Convention execution

`convention.Executor` wraps a `Client` with convention dispatch: it validates args, composes the correct tag set, enforces rate limits, and calls `Send`.

```go
exec := convention.NewExecutor(client, id.PublicKeyHex())
```

A `Declaration` describes one operation: its convention name, version, argument schema, tag composition rules, and signing mode.

```go
decl := &convention.Declaration{
    Convention:  "task-runner",
    Version:     "0.1",
    Operation:   "submit-result",
    Signing:     "member_key",
    Antecedents: "exactly_one(target)", // thread to the task message
    Args: []convention.ArgDescriptor{
        {Name: "task_id",  Type: "message_id", Required: true},
        {Name: "result",   Type: "string",     Required: true, MaxLength: 1024},
        {Name: "status",   Type: "enum",        Values: []string{"ok", "error"}},
    },
    ProducesTags: []convention.TagRule{
        {Tag: "result:submitted",    Cardinality: "exactly_one"},
        {Tag: "result:status:*",     Cardinality: "at_most_one"},
    },
}

err = exec.Execute(ctx, decl, campfireID, map[string]any{
    "task_id": taskMsgID,
    "result":  "output text",
    "status":  "result:status:ok",
})
```

`Execute` validates the args map against `decl.Args`, composes `Tags` from `ProducesTags` and the arg values, resolves `Antecedents`, and calls `Send`.

### Antecedent rules

| Rule | Behaviour |
|------|-----------|
| `"none"` or `""` | No antecedents |
| `"exactly_one(target)"` | Takes the `message_id`-typed arg as the single antecedent |
| `"exactly_one(self_prior)"` | Finds caller's most recent message with the same operation tag; requires it |
| `"zero_or_one(self_prior)"` | Like above but allows genesis (first message has no antecedent) |

### Operator provenance gating

```go
exec = exec.WithProvenance(myProvenanceChecker)

// Declaration gating: operation requires level 2
decl.MinOperatorLevel = 2

// Execute returns error if caller's level < 2
```

Implement `convention.ProvenanceChecker` to map public keys to integer trust levels (0–3). The executor enforces `MinOperatorLevel` before sending.

## Transport abstraction

The same `Client` and `Executor` code runs on all transports. Transport selection is determined by the campfire's membership record in the store, not by client configuration:

| Transport dir prefix | Transport |
|----------------------|-----------|
| (filesystem path) | Local filesystem — sync-before-query |
| `github:{...}` | GitHub Issues — needs `GITHUB_TOKEN` or `SendRequest.GitHubToken` |
| (peer HTTP endpoint) | P2P HTTP — messages delivered via push |

For GitHub transport, pass the token:

```go
msg, err := client.Send(protocol.SendRequest{
    CampfireID:  campfireID,
    Payload:     []byte("hello"),
    Tags:        []string{"status"},
    GitHubToken: os.Getenv("GITHUB_TOKEN"),
})
```

For P2P HTTP with threshold > 1, use `SigningModeThreshold`. The client runs FROST signing rounds with co-signers automatically.

## See also

- [`pkg/protocol/`](../pkg/protocol/) — `Client`, `SendRequest`, `ReadRequest`, `AwaitRequest`
- [`pkg/convention/`](../pkg/convention/) — `Executor`, `Declaration`, `ArgDescriptor`
- [Protocol spec](protocol-spec.md) — message envelope, provenance hops, identity
- [CLI reference](cli-conventions.md) — the same operations, from the command line
- [MCP server reference](mcp-conventions.md) — conventions as auto-generated MCP tools
