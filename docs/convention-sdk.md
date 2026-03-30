# Campfire SDK

Build services on campfire. Start with an LLM, move to CPU code, transparently to users.

`pkg/protocol` provides a `Client` for the full campfire lifecycle: create and join campfires, send and read messages, subscribe to live streams, manage members. `pkg/convention` layers typed operation dispatch on top. Both packages work across all transports — filesystem, GitHub Issues, P2P HTTP — without transport-specific code in your service.

## Init — one call to get started

```go
client, err := protocol.Init("~/.campfire")
// Generates or loads Ed25519 identity, opens SQLite store, returns *Client.
// Pass "" for default path.
defer client.Close()
```

`Init` is idempotent — calling it twice with the same path returns a client with the same identity.

For explicit control (e.g., custom store backend):

```go
id, _ := identity.Load("/path/to/identity.json")
s, _ := store.Open("/path/to/store.db")
client := protocol.New(s, id)

    // 3. Create a campfire (or join an existing one)
    result, err := client.Create(protocol.CreateRequest{})
    if err != nil {
        log.Fatal(err)
    }
    campfireID := result.CampfireID
    fmt.Println("created campfire:", campfireID)

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
    exec := convention.NewExecutor(client, client.PublicKeyHex())

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

To get the hex-encoded public key of the client's identity (e.g., to pass to `convention.NewExecutor`):

```go
pubKeyHex := client.PublicKeyHex() // returns "" for read-only clients
```

## Message type

`protocol.Message` is the SDK-facing message type returned by `Read`, `Get`, `GetByPrefix`, `Await`, and `Subscribe`:

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
```

`Sender`, `Tags`, `Antecedents`, `Instance`, and `Payload` are tainted — sender-asserted, not cryptographically verified beyond authorship. Never make access-control decisions based solely on these fields. `Signature` and `Provenance` are verified.

```go
// Test whether the message was bridged from an external system (Teams, Slack, etc.)
if msg.IsBridged() {
    // at least one provenance hop is a blind-relay
}
```

## Get and GetByPrefix

Retrieve a single message by its ID (or an unambiguous prefix) from the local store:

```go
// Exact ID lookup — returns nil, nil if not found
msg, err := client.Get(messageID)

// Prefix lookup — returns error if the prefix is ambiguous
msg, err := client.GetByPrefix("a1b2c3d4")
```

Both return `*protocol.Message` or `nil` if not found. Use these for targeted lookups after you already know the message ID (e.g., fetching a specific future by ID after receiving its ID from a peer).

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
exec := convention.NewExecutor(client, client.PublicKeyHex())
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

The same `Client` and `Executor` code runs on all transports. At create/join time, pass a typed transport config. After that, transport selection is automatic — the campfire's membership record determines the routing.

Three typed transport configs are available in `pkg/protocol`:

```go
// Local filesystem — members share a directory
protocol.FilesystemTransport{Dir: "/path/to/campfires"}

// P2P HTTP — direct peer-to-peer HTTP delivery
protocol.P2PHTTPTransport{
    Transport:    httpTransport,   // *cfhttp.Transport, already started
    MyEndpoint:   "http://host:9001",
    PeerEndpoint: "http://peer:9001", // required for Join
    Dir:          "/optional/state/dir",
}

// GitHub Issues — messages stored as issue comments
protocol.GitHubTransport{
    Owner:  "org",
    Repo:   "repo",
    Branch: "main",
    Dir:    "campfires/",
    Token:  os.Getenv("GITHUB_TOKEN"), // or use SendRequest.GitHubToken
}
```

The `Transport` interface is sealed — only these three types are accepted by `CreateRequest` and `JoinRequest`.

For GitHub transport, pass the token at send time:

```go
msg, err := client.Send(protocol.SendRequest{
    CampfireID:  campfireID,
    Payload:     []byte("hello"),
    Tags:        []string{"status"},
    GitHubToken: os.Getenv("GITHUB_TOKEN"),
})
```

For P2P HTTP with threshold > 1, use `SigningModeThreshold`. The client runs FROST signing rounds with co-signers automatically.

## Subscribe

`Subscribe` returns a live stream of messages. It manages the poll loop, cursor, sync, and context cancellation internally.

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

sub := client.Subscribe(ctx, protocol.SubscribeRequest{
    CampfireID:     campfireID,
    Tags:           []string{"status"},       // OR filter — any matching tag
    TagPrefixes:    []string{"galtrader:"},    // OR with Tags — prefix match
    ExcludeTags:    []string{"convention:operation"},
    PollInterval:   500 * time.Millisecond,   // default 500ms
})

for msg := range sub.Messages() {
    fmt.Printf("[%s] %s\n", msg.Sender[:8], msg.Payload)
}

// Channel closes when context is cancelled or a transport error occurs.
if err := sub.Err(); err != nil {
    log.Printf("subscription error: %v", err)
}
```

`Subscribe` replaces the manual cursor loop shown in the Read section above. Use `Read` for one-shot queries; use `Subscribe` for continuous watching.

### Start from a point in time

```go
sub := client.Subscribe(ctx, protocol.SubscribeRequest{
    CampfireID:     campfireID,
    AfterTimestamp: lastProcessedTimestamp, // skip already-seen messages
})
```

When `AfterTimestamp` is 0 (default), all existing messages are delivered first, then new ones stream in.

## Campfire lifecycle

### Create

```go
result, err := client.Create(protocol.CreateRequest{
    JoinProtocol: "open",  // or "invite-only"
    Transport:    protocol.FilesystemTransport{Dir: "/path/to/campfires"},
    Threshold:    1,       // >1 triggers FROST DKG (P2P HTTP only)
})
// result.CampfireID — hex-encoded campfire public key
// result.Beacon — published beacon for discovery
```

For P2P HTTP:

```go
result, err := client.Create(protocol.CreateRequest{
    JoinProtocol: "open",
    Transport: protocol.P2PHTTPTransport{
        Transport:  httpTransport,
        MyEndpoint: "http://localhost:9001",
    },
})
```

### Join

```go
// Filesystem
result, err := client.Join(protocol.JoinRequest{
    CampfireID: campfireID,
    Transport:  protocol.FilesystemTransport{Dir: "/path/to/campfires"},
})

// P2P HTTP — PeerEndpoint is required
result, err := client.Join(protocol.JoinRequest{
    CampfireID: campfireID,
    Transport: protocol.P2PHTTPTransport{
        Transport:    httpTransport,
        MyEndpoint:   "http://localhost:9002",
        PeerEndpoint: "http://peer:9001",
    },
})
// result.CampfireID — joined campfire ID
// result.JoinProtocol — "open" or "invite-only"
```

After joining, Send/Read/Subscribe work immediately.

### Leave, Disband

```go
client.Leave(campfireID)    // remove self, send campfire:member-left
client.Disband(campfireID)  // creator-only: tear down campfire entirely
```

### Admit, Evict

```go
// Pre-admit a member (they can then Join without invite-only rejection)
client.Admit(protocol.AdmitRequest{
    CampfireID:      campfireID,
    MemberPubKeyHex: memberPubKeyHex,
    Role:            "writer",  // "full", "writer", "observer"
    Transport:       protocol.FilesystemTransport{Dir: "/path/to/campfires"},
})

// Remove a member (rekeys the campfire for P2P HTTP with threshold>1)
result, err := client.Evict(protocol.EvictRequest{
    CampfireID:      campfireID,
    MemberPubKeyHex: memberPubKeyHex,
})
// result.Rekeyed — true if campfire was rekeyed
// result.NewCampfireID — new campfire ID after rekey (threshold>1 only)
```

### Members

```go
members, err := client.Members(campfireID)
for _, m := range members {
    fmt.Printf("%s role=%s\n", m.PubKeyHex[:8], m.Role)
}
```

## Convention Server SDK

Build a service that handles convention operations. The Server polls for incoming requests via `Subscribe`, parses and validates args per the declaration, dispatches to your handler, and sends auto-threaded responses.

```go
decl := &convention.Declaration{
    Convention: "task-runner",
    Version:    "0.1",
    Operation:  "submit-result",
    Signing:    "member_key",
    Args: []convention.ArgDescriptor{
        {Name: "task_id", Type: "string", Required: true},
        {Name: "result",  Type: "string", Required: true},
    },
    ProducesTags: []convention.TagRule{
        {Tag: "result:submitted", Cardinality: "exactly_one"},
    },
}

srv := convention.NewServer(client, decl)
srv.WithPollInterval(2 * time.Second)
srv.WithErrorHandler(func(err error) { log.Printf("handler error: %v", err) })

srv.RegisterHandler("submit-result", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
    taskID := req.Args["task_id"].(string)
    result := req.Args["result"].(string)

    // Your business logic here — LLM call, database write, API call, anything.
    // This handler can be powered by an LLM today and moved to CPU code tomorrow.
    // Callers see the same convention interface either way.

    return &convention.Response{
        Payload: []byte(fmt.Sprintf(`{"status":"ok","task_id":"%s"}`, taskID)),
    }, nil
})

// Blocks until context is cancelled. Handles all matching messages.
srv.Serve(ctx, campfireID)
```

**Key property: LLM-to-CPU transparency.** A convention handler powered by an LLM produces the same typed response as one implemented in pure Go. Callers don't know or care which is behind the convention. You can start with an LLM doing the work, validate the behavior, then replace the handler body with deterministic code — the convention interface is the contract.

### Request and Response types

```go
type Request struct {
    MessageID   string         // ID of the incoming message
    CampfireID  string         // which campfire
    Sender      string         // sender's public key hex
    Args        map[string]any // parsed and validated per declaration
    Tags        []string       // message tags
    Antecedents []string       // message threading
}

type Response struct {
    Payload []byte   // response payload (auto-threaded as antecedent of request)
    Tags    []string // additional tags beyond the auto-added "fulfills"
}
```

When a handler returns a `*Response`, the Server sends it as a message with `Antecedents: [req.MessageID]` and tag `"fulfills"` — so the caller's `client.Await(targetMsgID)` resolves automatically.

## Dual-loop pattern: Subscribe + convention.Server concurrently

Some agents need to both **serve convention requests** (handle inbound operations from other agents) and **monitor campfire activity** (react to arbitrary messages — status updates, findings, peer beads). The dual-loop pattern runs both loops simultaneously in the same process.

**When to use this pattern**: Your agent serves at least one convention operation AND needs to watch the campfire for messages that arrive outside of a request/response convention — for example, a coordinator that handles task submissions and also watches for status broadcasts from other agents, or an orchestrator that dispatches work via a convention and monitors completion signals in parallel.

If your agent only serves conventions, use `convention.Server` alone. If it only watches, use `Subscribe` alone. Use the dual-loop when you need both.

### Setup: two Client instances from the same configDir

`Client` is not safe for concurrent use from multiple goroutines. The dual-loop pattern requires two separate Client instances — one for the Subscribe loop, one for the convention.Server. Both are created from the same `configDir`, which means they share the same identity and the same SQLite store file. The store handles concurrent access internally (WAL journal mode, 5-second busy timeout).

```go
// Client A: drives the Subscribe loop.
clientA, err := protocol.Init("~/.campfire")
if err != nil {
    log.Fatal(err)
}
defer clientA.Close()

// Client B: drives convention.Server.Serve().
// Same configDir → same identity, same store file, separate handle.
clientB, err := protocol.Init("~/.campfire")
if err != nil {
    log.Fatal(err)
}
defer clientB.Close()
```

### Code example

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/campfire-net/campfire/pkg/convention"
    "github.com/campfire-net/campfire/pkg/protocol"
)

func main() {
    campfireID := os.Args[1]

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Shut down cleanly on SIGINT / SIGTERM.
    sigs := make(chan os.Signal, 1)
    signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigs
        cancel()
    }()

    // Client A: Subscribe loop — monitors all campfire activity.
    clientA, err := protocol.Init("~/.campfire")
    if err != nil {
        log.Fatalf("init clientA: %v", err)
    }
    defer clientA.Close()

    // Client B: convention.Server — handles inbound operation requests.
    clientB, err := protocol.Init("~/.campfire")
    if err != nil {
        log.Fatalf("init clientB: %v", err)
    }
    defer clientB.Close()

    // Build the convention server on clientB.
    decl := &convention.Declaration{
        Convention: "task-runner",
        Version:    "0.1",
        Operation:  "submit-task",
        Signing:    "member_key",
        Args: []convention.ArgDescriptor{
            {Name: "task_id", Type: "string", Required: true},
            {Name: "payload", Type: "string", Required: true},
        },
        ProducesTags: []convention.TagRule{
            {Tag: "task:accepted", Cardinality: "exactly_one"},
        },
    }

    srv := convention.NewServer(clientB, decl)
    srv.WithErrorHandler(func(err error) {
        log.Printf("convention server error: %v", err)
    })

    srv.RegisterHandler("submit-task", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
        taskID := req.Args["task_id"].(string)
        log.Printf("handling task %s from %s", taskID, req.Sender[:8])
        return &convention.Response{
            Payload: []byte(fmt.Sprintf(`{"status":"accepted","task_id":"%s"}`, taskID)),
        }, nil
    })

    // Loop 1 (goroutine): convention.Server polls for convention requests.
    srvErr := make(chan error, 1)
    go func() {
        srvErr <- srv.Serve(ctx, campfireID)
    }()

    // Loop 2 (goroutine): Subscribe watches all non-convention messages.
    sub := clientA.Subscribe(ctx, protocol.SubscribeRequest{
        CampfireID:  campfireID,
        ExcludeTags: []string{"convention:operation"}, // skip what the server handles
    })
    subDone := make(chan struct{})
    go func() {
        defer close(subDone)
        for msg := range sub.Messages() {
            log.Printf("[monitor] %s: %s", msg.Sender[:8], msg.Payload)
        }
        if err := sub.Err(); err != nil {
            log.Printf("subscribe error: %v", err)
        }
    }()

    // Wait for either loop to finish (context cancel or error).
    select {
    case err := <-srvErr:
        if err != nil {
            log.Printf("convention server stopped: %v", err)
        }
    case <-subDone:
    }
}
```

### Concurrency notes

Both clients open the same SQLite file at `configDir/store.db`. The store is opened with WAL journal mode and a 5-second busy timeout, so concurrent reads and writes from separate client handles proceed without coordination from your code. Writes (Send, message delivery) briefly hold a write lock; reads (Subscribe polls, convention.Server polls) run concurrently.

You do not need a mutex around the two clients. Each client is used exclusively by its own goroutine — clientA by the Subscribe goroutine, clientB by the `srv.Serve` goroutine. This is what "one `Client` per goroutine" means in practice.

### Teardown

Both loops respect context cancellation. Calling `cancel()` stops both:

- `Subscribe` closes its `Messages()` channel and its goroutine exits.
- `convention.Server.Serve` returns once the context is cancelled.

The `defer clientA.Close()` and `defer clientB.Close()` calls release the SQLite handles after both loops have stopped. Closing in the wrong order (closing a client while its goroutine is still running) is safe — the goroutine will see a context cancellation or a store error and exit cleanly before the next poll.

## Integration hierarchy

| Building... | Use | Why |
|-------------|-----|-----|
| A backend service | **Go SDK** (`protocol.Client` + `convention.Server`) | Full lifecycle, Subscribe, typed handlers, LLM-to-CPU migration |
| An AI agent workflow | **`cf` CLI** | Convention commands from any language, shell-friendly |
| An AI agent via tool calling | **`cf-mcp` MCP server** | Convention tools auto-register on join, no code needed |

The SDK, CLI, and MCP server all speak the same protocol. A convention handler written against the SDK is callable by a CLI user and an MCP agent — they're different interfaces to the same campfire.

## Naming

Register and resolve named endpoints within a campfire namespace. Names are stored as convention messages — no separate service required.

### Register a name

```go
naming.Register(ctx, client, campfireID, "search", targetID, nil)
```

### Resolve a name

```go
resp, _ := naming.Resolve(ctx, client, campfireID, "search")
// resp.CampfireID is the target
```

### List all names

```go
registrations, _ := naming.List(ctx, client, campfireID)
```

### Hierarchical resolution

```go
resolver := naming.NewResolverFromClient(client, rootID)
result, _ := resolver.ResolveURI(ctx, "cf://child.leaf")
// Auto-joins open registries during walk
```

Resolution is direct-read — the resolver reads naming messages from the campfire store, no RPC or futures involved. This keeps resolution fast and eliminates a class of timeout/retry failure modes.

See [`pkg/naming/`](../pkg/naming/) for the full API.

## Peering

Manage peer endpoints for P2P HTTP campfires. Peers are the delivery targets for outbound messages.

```go
client.AddPeer(campfireID, protocol.PeerInfo{Endpoint: "https://...", PublicKeyHex: "..."})
peers, _ := client.Peers(campfireID)
client.RemovePeer(campfireID, publicKeyHex)
// Returns ErrTransportNotSupported on non-HTTP transports
```

## Bridging

Bridge messages between two campfires. Both campfires can use different transports.

```go
err := protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
    Bidirectional: true,
    TagFilter: []string{"important"},
})
```

`Bridge` runs a forwarding loop: messages matching `TagFilter` on the source are re-sent to the destination (with provenance hops). When `Bidirectional` is true, the reverse direction is also forwarded. The loop runs until the context is cancelled.

## See also

- [`pkg/protocol/`](../pkg/protocol/) — `Client`, `SendRequest`, `ReadRequest`, `AwaitRequest`, `SubscribeRequest`, `CreateRequest`, `JoinRequest`
- [`pkg/convention/`](../pkg/convention/) — `Server`, `Executor`, `Declaration`, `ArgDescriptor`
- [Protocol spec](protocol-spec.md) — message envelope, provenance hops, identity
- [CLI reference](cli-conventions.md) — the same operations, from the command line
- [MCP server reference](mcp-conventions.md) — conventions as auto-generated MCP tools
- [SDK 0.12 migration guide](sdk-migration-0.12.md) — upgrading from 0.11: typed transports, `protocol.Message`, removed exported fields
