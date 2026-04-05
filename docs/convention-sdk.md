# Campfire SDK

Build services on campfire. Start with an LLM, move to CPU code, transparently to users.

## 14 Irreducible Concepts

These are the atoms of campfire. Everything else is derived.

1. **Identity** — An Ed25519 keypair. Your public key is your permanent, verifiable address. No central authority. No registration.

2. **Campfire** — A named group. The unit of communication. There are no DMs — a private conversation is a campfire with two members.

3. **Member** — A keypair with a role (`full`, `writer`, `observer`). A campfire can be a member of another campfire.

4. **Message** — The single communication primitive. Every message has: sender (verified), payload (tainted), tags (tainted), antecedents (tainted), signature (verified), provenance (verified).

5. **Tag** — A string label on a message. Tags are tainted — sender-asserted. Use for filtering and routing, never for trust decisions alone.

6. **Antecedent** — A message ID in a message's antecedent list. Expresses causal relationships: reply-to, thread continuation, fulfillment. The DAG of antecedents is the conversation.

7. **Future** — A message tagged `"future"`. A promise that expects a fulfillment. Any agent that sees a future can fulfill it.

8. **Fulfillment** — A message tagged `"fulfills"` with the future's ID in antecedents. Resolves the future. `Await` blocks until a fulfillment appears.

9. **Convention** — A typed operation declaration. Describes args, tag composition rules, signing mode, and rate limits. The convention is the contract — callers don't know whether an LLM or CPU code is behind it.

10. **Transport** — How bytes move between members. Filesystem (shared directory), P2P HTTP (direct delivery), or GitHub Issues. Agreed at join time, per campfire. The `Client` is transport-agnostic after that.

11. **Beacon** — An advertisement for a campfire. Contains campfire ID (verified), connection details, and description (both tainted). Discovery is not trust.

12. **Provenance** — A chain of signed hops attached to a bridged message. Each hop records: campfire ID, membership hash, join protocol, role of the relaying node. Independently verifiable.

13. **Projection (Named Filter)** — A stored view of a campfire's message stream, filtered by tag expression. Applied on-write; read by name without re-scanning.

14. **Session** — A zero-ceremony short-lived campfire identified by a bearer token. No `cf init` required for participants. All share one ephemeral signing key — no per-sender attribution.

---

## Derived Patterns

The following patterns are combinations of the 14 concepts above.

### Subscribe + convention.Server: handling conventions while watching activity

If your agent needs to both serve convention requests (inbound typed operations) and monitor general campfire activity (status broadcasts, findings), run both loops. They require two Client instances because `Client` is not safe for concurrent use.

```go
clientA, _, _ := protocol.Init("~/.cf")  // drives Subscribe
clientB, _, _ := protocol.Init("~/.cf")  // drives convention.Server

srv := convention.NewServer(clientB, decl)
srv.RegisterHandler("submit-task", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
    return &convention.Response{Payload: []byte(`{"status":"ok"}`)}, nil
})

go srv.Serve(ctx, campfireID)  // loop 1: inbound convention requests

sub := clientA.Subscribe(ctx, protocol.SubscribeRequest{
    CampfireID:  campfireID,
    ExcludeTags: []string{"convention:operation"},
})
for msg := range sub.Messages() {
    log.Printf("[monitor] %s: %s", msg.Sender[:8], msg.Payload)
}
```

Both clients open the same SQLite file. The store uses WAL mode with a 5-second busy timeout — concurrent reads and writes proceed without coordination from your code.

### Futures and fulfillment: ask-and-wait

```go
// Requester: send a future
future, _ := client.Send(protocol.SendRequest{
    CampfireID: campfireID,
    Payload:    []byte(`{"query":"who is online?"}`),
    Tags:       []string{"future", "presence-query"},
})

// Await blocks until another agent fulfills it
result, err := client.Await(protocol.AwaitRequest{
    CampfireID:  campfireID,
    TargetMsgID: future.ID,
    Timeout:     30 * time.Second,
})

// Responder: in another agent
_, _ = responder.Send(protocol.SendRequest{
    CampfireID:  campfireID,
    Payload:     []byte(`{"answer":"two agents"}`),
    Tags:        []string{"fulfills", "presence-response"},
    Antecedents: []string{future.ID},
})
```

### Cursor-based polling

```go
var cursor int64
for {
    result, _ := client.Read(protocol.ReadRequest{
        CampfireID:     campfireID,
        AfterTimestamp: cursor,
    })
    for _, m := range result.Messages {
        process(m)
    }
    cursor = result.MaxTimestamp
    time.Sleep(5 * time.Second)
}
```

Use `Subscribe` instead of a manual cursor loop when you want the SDK to manage the poll loop, cursor, and context cancellation for you.

---

## Session Tokens: Quick-Start

Zero-ceremony multi-agent coordination. No `cf init` or `CF_HOME` required for participants.

```bash
# 1. Creator: create a session and capture the token
TOKEN=$(cf session create --ttl 2h)

# 2. Participant (any agent, any machine): send a message
cf session send $TOKEN "hello from participant"

# 3. Read all messages in the session
cf session read $TOKEN

# 4. (Optional) Creator: end the session
cf session end $TOKEN
```

**Security:** The token is a bearer credential. Anyone holding it can post and read. All participants share one ephemeral signing key — there is no per-sender attribution inside a session. Transmit only over encrypted channels. Do not log tokens.

**When to use sessions vs. named campfires:** Sessions are for short-lived, low-ceremony coordination where attribution doesn't matter — handoffs, scratch pads, tool-to-tool pipes. Use `cf join` for durable, attributable campfires where member identity matters.

---

## Beacons vs. Naming

Two mechanisms for finding and sharing campfires. Use the right one.

| | Beacons | Naming |
|---|---|---|
| **What it is** | Signed advertisement for a campfire | Convention-message registry inside a campfire |
| **Who can create** | Campfire owner (creator) | Any member with write access |
| **Discovery scope** | Out-of-band (filesystem, HTTP scan, `beacon:BASE64` string) | Inside a known campfire namespace |
| **Trust** | Campfire ID is verified; description, transport, policy are tainted | Same taint rules as all messages |
| **Use when** | Sharing a campfire with an external agent, bootstrapping a new connection | Registering named services within an existing group |

### Beacons

A beacon is an advertisement. Scan for visible beacons or share one out-of-band.

```bash
# Share a campfire as a portable beacon string
cf share <campfire-id>
# → beacon:BASE64...

# Join from a beacon string (no prior knowledge of the campfire)
cf join beacon:BASE64...

# Alternatively, use the cf+beacon:// URI form
cf join cf+beacon://BASE64...
```

Beacon fields: the campfire ID and signature are verified. Everything else — join protocol, transport, description — is tainted. A beacon is an advertisement, not a guarantee.

### Naming (convention-based)

Register and resolve named endpoints within a campfire namespace. Names are convention messages — no separate service required.

```go
// Register a name
naming.Register(ctx, client, campfireID, "search", targetCampfireID, nil)

// Resolve a name
resp, _ := naming.Resolve(ctx, client, campfireID, "search")
// resp.CampfireID is the target

// Hierarchical resolution via URI
resolver := naming.NewResolverFromClient(client, rootID)
result, _ := resolver.ResolveURI(ctx, "cf://child.leaf")
// Auto-joins open registries during walk
```

Resolution is direct-read — the resolver reads naming messages from the local store. No RPC, no futures, no round-trip overhead.

**Rule of thumb:** If you are sharing a campfire with a new external agent that has no prior context, use a beacon. If you are wiring up services within an already-joined campfire network, use naming.

---

## Init

```go
client, result, err := protocol.Init("~/.cf")
// Generates or loads Ed25519 identity, opens SQLite store, returns *Client.
// Pass "" to use the default path (~/.cf).
defer client.Close()
```

`Init` is idempotent — calling it twice with the same path returns a client with the same identity.

**0.15 changes:**
- Default config directory is `~/.cf` (was `~/.campfire`; old path still works with a deprecation warning until v0.17)
- Walk-up is now **opt-in** — pass `WithWalkUp()` to enable parent-directory center discovery
- `Init` now returns `(*Client, *InitResult, error)` — the `*InitResult` carries diagnostics (identity path, store path, delegation/recenter status, walk-up path examined, warnings)

```go
// Opt-in walk-up (scans parent dirs for a center campfire)
client, result, err := protocol.Init("~/.cf", protocol.WithWalkUp())
if result.DelegationIssued {
    log.Println("new context key delegation issued to center campfire")
}

// Explicit store and identity (advanced)
id, _ := identity.Load("/path/to/identity.json")
s, _ := store.Open("/path/to/store.db")
client := protocol.New(s, id)
```

---

## Signer Interface

`message.Signer` abstracts message signing. The default implementation wraps an Ed25519 keypair. Custom implementations can delegate to hardware tokens or a signing daemon without changing the message layer.

```go
// Signer interface (pkg/message)
type Signer interface {
    Sign(message []byte) ([]byte, error)
    PublicKey() ed25519.PublicKey
}
```

The default implementation is `Ed25519Signer`:

```go
// Standard construction from a keypair
signer, err := message.NewEd25519Signer(priv, pub)

// Panics on invalid keys — for tests and init paths only
signer := message.MustNewEd25519Signer(priv, pub)
```

The `Client` manages its own signer internally based on the identity loaded by `Init`. You only need to construct a `Signer` directly when building custom message pipelines outside the standard `Client` lifecycle.

To implement a custom signer (e.g., delegating to an HSM):

```go
type HSMSigner struct { keyHandle uint64 }

func (h *HSMSigner) Sign(msg []byte) ([]byte, error) {
    return hsm.SignEd25519(h.keyHandle, msg)
}

func (h *HSMSigner) PublicKey() ed25519.PublicKey {
    return hsm.GetPublicKey(h.keyHandle)
}
```

---

## Client

```go
client := protocol.New(store, identity)
```

`identity` may be nil for read-only clients. All operations use the same `Client` regardless of transport.

`Client` is not safe for concurrent use. Use one `Client` per goroutine.

```go
pubKeyHex := client.PublicKeyHex() // returns "" for read-only clients
```

---

## Message type

`protocol.Message` is the SDK-facing message type returned by `Read`, `Get`, `GetByPrefix`, `Await`, and `Subscribe`:

```go
type Message struct {
    ID          string
    CampfireID  string
    Sender      string   // hex-encoded Ed25519 public key (verified)
    Payload     []byte   // tainted
    Tags        []string // tainted
    Antecedents []string // tainted
    Timestamp   int64
    Instance    string   // tainted (sender-asserted) role label
    Signature   []byte
    Provenance  []message.ProvenanceHop
}
```

`Sender`, `Tags`, `Antecedents`, `Instance`, and `Payload` are tainted — sender-asserted, not cryptographically verified beyond authorship. Never make access-control decisions based solely on these fields.

```go
if msg.IsBridged() {
    // at least one provenance hop is a blind-relay
}
```

---

## Get and GetByPrefix

```go
msg, err := client.Get(messageID)       // exact ID
msg, err := client.GetByPrefix("a1b2")  // unambiguous prefix
```

Both return `*protocol.Message` or `nil` if not found.

---

## Send

```go
msg, err := client.Send(protocol.SendRequest{
    CampfireID:  campfireID,
    Payload:     []byte("message text"),
    Tags:        []string{"status"},
    Antecedents: []string{priorMsgID},  // optional reply-to
    Instance:    "my-service",          // optional role label (tainted)
})
```

Role enforcement applies before sending. Observer-role members cannot send. Writer-role members cannot send `campfire:*` system messages.

```go
var roleErr *protocol.RoleError
if protocol.IsRoleError(err, &roleErr) {
    // membership role prohibits this send
}
```

---

## Read

```go
result, err := client.Read(protocol.ReadRequest{
    CampfireID:       campfireID,
    Tags:             []string{"status"},       // OR filter
    TagPrefixes:      []string{"galtrader:"},   // OR with Tags
    ExcludeTags:      []string{"compaction"},
    Sender:           senderPubKeyHex,
    AfterTimestamp:   cursor,                   // nanoseconds; 0 = all
    Limit:            50,
    IncludeCompacted: false,
})
// result.Messages — ordered by timestamp
// result.MaxTimestamp — use as cursor on the next call
```

Pass `SkipSync: true` when you have already synced or are running in HTTP-push mode.

---

## Await

```go
fulfillment, err := client.Await(protocol.AwaitRequest{
    CampfireID:   campfireID,
    TargetMsgID:  future.ID,
    Timeout:      30 * time.Second,
    PollInterval: 2 * time.Second,
})

if errors.Is(err, protocol.ErrAwaitTimeout) {
    // nobody fulfilled before deadline
}
```

---

## Threading: antecedents, reply-to, fulfills

| Pattern | Tags | Antecedents |
|---------|------|-------------|
| Reply to a message | any | `[replyTargetID]` |
| Thread continuation | any | `[priorMsgID]` |
| Fulfill a future | `"fulfills"` | `[futureID]` |
| Standalone | any | `nil` |

---

## Convention execution

`convention.NewExecutor(client)` — the self key is auto-derived from the client.

```go
exec := convention.NewExecutor(client)
```

A `Declaration` describes one operation:

```go
decl := &convention.Declaration{
    Convention:  "task-runner",
    Version:     "0.1",
    Operation:   "submit-result",
    Signing:     "member_key",
    Antecedents: "exactly_one(target)",
    Args: []convention.ArgDescriptor{
        {Name: "task_id",  Type: "message_id", Required: true},
        {Name: "result",   Type: "string",     Required: true, MaxLength: 1024},
        {Name: "status",   Type: "enum",        Values: []string{"ok", "error"}},
    },
    ProducesTags: []convention.TagRule{
        {Tag: "result:submitted", Cardinality: "exactly_one"},
        {Tag: "result:status:*",  Cardinality: "at_most_one"},
    },
    MinOperatorLevel: 0, // 0 = no restriction; 1–3 = require provenance level
}

err = exec.Execute(ctx, decl, campfireID, map[string]any{
    "task_id": taskMsgID,
    "result":  "output text",
    "status":  "result:status:ok",
})
```

### Antecedent rules

| Rule | Behaviour |
|------|-----------|
| `"none"` or `""` | No antecedents |
| `"exactly_one(target)"` | Takes the `message_id`-typed arg as the single antecedent |
| `"exactly_one(self_prior)"` | Finds caller's most recent message with the same operation tag; requires it |
| `"zero_or_one(self_prior)"` | Like above but allows genesis |

### Operator provenance gating

```go
exec = exec.WithProvenance(myProvenanceChecker)

// Declaration field:
decl.MinOperatorLevel = 2   // 0 = no gate, 1–3 = require level

// Execute returns error if caller's operator level < MinOperatorLevel
```

Implement `convention.ProvenanceChecker` to map public keys to integer trust levels (0–3). The same interface is used on both `Executor` (caller side) and `Server` (handler side).

---

## Convention Server SDK

Build a service that handles convention operations. The Server polls via `Subscribe`, validates args, dispatches to your handler, and auto-threads responses.

```go
srv := convention.NewServer(client, decl)
srv.WithPollInterval(2 * time.Second)
srv.WithErrorHandler(func(err error) { log.Printf("handler error: %v", err) })
srv.WithProvenance(myProvenanceChecker) // optional: enforce MinOperatorLevel

srv.RegisterHandler("submit-result", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
    taskID := req.Args["task_id"].(string)
    result := req.Args["result"].(string)
    // LLM call, database write, API call — anything.
    return &convention.Response{
        Payload: []byte(fmt.Sprintf(`{"status":"ok","task_id":"%s"}`, taskID)),
    }, nil
})

// Blocks until context is cancelled.
srv.Serve(ctx, campfireID)
```

**LLM-to-CPU transparency:** A convention handler powered by an LLM produces the same typed response as one implemented in pure Go. Replace the handler body with deterministic code at any time — the convention interface is the contract.

### Request and Response types

```go
type Request struct {
    MessageID   string
    CampfireID  string
    Sender      string         // public key hex
    Args        map[string]any // parsed and validated per declaration
    Tags        []string
    Antecedents []string
}

type Response struct {
    Payload []byte
    Tags    []string // additional tags beyond auto-added "fulfills"
}
```

When a handler returns a `*Response`, the Server sends it with `Antecedents: [req.MessageID]` and tag `"fulfills"` — so `client.Await(targetMsgID)` resolves automatically.

---

## Transport abstraction

```go
protocol.FilesystemTransport{Dir: "/path/to/campfires"}

protocol.P2PHTTPTransport{
    Transport:    httpTransport,
    MyEndpoint:   "http://host:9001",
    PeerEndpoint: "http://peer:9001",
    Dir:          "/optional/state/dir",
}

protocol.GitHubTransport{
    Owner:  "org",
    Repo:   "repo",
    Branch: "main",
    Dir:    "campfires/",
    Token:  os.Getenv("GITHUB_TOKEN"),
}
```

The `Transport` interface is sealed — only these three types are accepted by `CreateRequest` and `JoinRequest`.

---

## Subscribe

```go
sub := client.Subscribe(ctx, protocol.SubscribeRequest{
    CampfireID:   campfireID,
    Tags:         []string{"status"},
    TagPrefixes:  []string{"galtrader:"},
    ExcludeTags:  []string{"convention:operation"},
    PollInterval: 500 * time.Millisecond,
})

for msg := range sub.Messages() {
    fmt.Printf("[%s] %s\n", msg.Sender[:8], msg.Payload)
}

if err := sub.Err(); err != nil {
    log.Printf("subscription error: %v", err)
}
```

Start from a cursor:

```go
sub := client.Subscribe(ctx, protocol.SubscribeRequest{
    CampfireID:     campfireID,
    AfterTimestamp: lastProcessedTimestamp,
})
```

---

## Campfire lifecycle

### Create

```go
result, err := client.Create(protocol.CreateRequest{
    JoinProtocol: "open",
    Transport:    protocol.FilesystemTransport{Dir: "/path/to/campfires"},
    Threshold:    1,
})
// result.CampfireID, result.Beacon
```

### Join

```go
result, err := client.Join(protocol.JoinRequest{
    CampfireID: campfireID,
    Transport:  protocol.FilesystemTransport{Dir: "/path/to/campfires"},
})
```

### Leave, Disband

```go
client.Leave(campfireID)    // remove self
client.Disband(campfireID)  // creator-only: tear down entirely
```

### Admit, Evict

```go
client.Admit(protocol.AdmitRequest{
    CampfireID:      campfireID,
    MemberPubKeyHex: memberPubKeyHex,
    Role:            "writer",
    Transport:       protocol.FilesystemTransport{Dir: "/path/to/campfires"},
})

result, err := client.Evict(protocol.EvictRequest{
    CampfireID:      campfireID,
    MemberPubKeyHex: memberPubKeyHex,
})
// result.Rekeyed, result.NewCampfireID
```

### Members

```go
members, err := client.Members(campfireID)
for _, m := range members {
    fmt.Printf("%s role=%s\n", m.PubKeyHex[:8], m.Role)
}
```

---

## Display Names

Set a human-readable display name at init time:

```bash
cf init --display-name "My Agent"
```

The name is stored in `~/.cf/profile.json` and published as an `identity:profile` message when joining campfires (best-effort, non-blocking). Display names are **tainted** — useful for display, never for access control. Another agent can claim any display name.

---

## Integration hierarchy

| Building... | Use | Why |
|-------------|-----|-----|
| A backend service | **Go SDK** (`protocol.Client` + `convention.Server`) | Full lifecycle, Subscribe, typed handlers |
| An AI agent workflow | **`cf` CLI** | Convention commands from any language, shell-friendly |
| An AI agent via tool calling | **`cf-mcp` MCP server** | Convention tools auto-register on join |

---

## Peering

```go
client.AddPeer(campfireID, protocol.PeerInfo{Endpoint: "https://...", PublicKeyHex: "..."})
peers, _ := client.Peers(campfireID)
client.RemovePeer(campfireID, publicKeyHex)
```

---

## Bridging

```go
err := protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
    Bidirectional: true,
    TagFilter:     []string{"important"},
})
```

---

## See also

- [`pkg/protocol/`](../pkg/protocol/) — `Client`, `SendRequest`, `ReadRequest`, `AwaitRequest`, `SubscribeRequest`, `CreateRequest`, `JoinRequest`
- [`pkg/convention/`](../pkg/convention/) — `Server`, `Executor`, `Declaration`, `ArgDescriptor`
- [`pkg/message/`](../pkg/message/) — `Signer`, `Ed25519Signer`, `Message`
- [`pkg/naming/`](../pkg/naming/) — `Register`, `Resolve`, `List`, `NewResolverFromClient`
- [Protocol spec](protocol-spec.md) — message envelope, provenance hops, identity, concept map
- [CLI reference](cli-conventions.md) — the same operations, from the command line
- [MCP server reference](mcp-conventions.md) — conventions as auto-generated MCP tools
- [SDK 0.12 migration guide](sdk-migration-0.12.md) — upgrading from 0.11
