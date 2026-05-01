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

10. **Transport** — How bytes move between members. Filesystem (shared directory) or P2P HTTP (direct delivery). Agreed at join time, per campfire. The `Client` is transport-agnostic after that. (GitHub transport was research-grade with no named consumer and is removed in 0.30.)

11. **Beacon** — An advertisement for a campfire. Contains campfire ID (verified), connection details, and description (both tainted). Discovery is not trust.

12. **Provenance** — A chain of signed hops attached to a bridged message. Each hop records: campfire ID, membership hash, join protocol, role of the relaying node. Independently verifiable.

13. **Projection (Named Filter)** — A stored view of a campfire's message stream, filtered by tag expression. Applied on-write; read by name without re-scanning.

14. **Session** (`cf-session`, L3 in 0.30) — An ephemeral-identity convention. Each participant gets their own Ed25519 keypair with a scoped grant from the session creator. Per-participant attribution is preserved. (The 0.19 model used a shared signing key with no individual attribution — removed in 0.30.)

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

## cf-session: Ephemeral-Identity Convention (0.30)

`cf-session` is an L3 ephemeral-identity convention. Each session participant gets their
own Ed25519 keypair with a scoped grant from the session creator — per-participant
attribution is preserved throughout the session.

The 0.19 shared-key session token model (`cfs1_<base64>`, shared single key, no per-worker
attribution) is **removed in 0.30**. Do not use it in new code.

```bash
# Orchestrator creates a session (TTL required; max 24h)
cf session create --ttl 2h   # → session campfire ID

# Worker presents its key; orchestrator's session handler issues a scoped grant
# (lazy-mint on identity:introduce — no pre-minted grants to non-existent workers)
cf session join <session-id>

# Read session messages (attributable to each participant's key)
cf session read <session-id>

# Orchestrator ends the session
cf session end <session-id>
```

**Security:** Each participant holds their own key. Compromise of one worker compromises
one grant, not the entire session. Revocation is grant-id-granular. Sessions never reuse
keys. The canonical authority chain lives in the owner's identity campfire, not the
session campfire — session compaction does not break authorization walks.

**Key handling backends** (selected via `cf-session` declaration's `key_handling` field):
- `jail` (default) — orchestrator generates the worker's keypair, writes it to a 0700 file, forks the worker. Key never crosses a network boundary.
- `signing_proxy` (opt-in) — orchestrator retains the private key; worker calls a Unix socket for every signature. Worker process never holds the key.

**When to use sessions vs. named campfires:** Sessions are for short-lived, attributed,
ephemeral-key coordination — swarm dispatches, parallel agents, tool-to-tool pipes where
you want audit trails. Use `cf join` for durable campfires with persistent membership.

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

**0.16+ recommended entry point:** `protocol.InitWithConfig`

```go
// 0.16+: reads ~/.cf/config.toml (and project .cf/config.toml cascade)
client, result, err := protocol.InitWithConfig()
defer client.Close()

// 0.15+: direct Init with explicit path
client, result, err := protocol.Init("~/.cf")
defer client.Close()
```

Both signatures return `(*Client, *InitResult, error)`. `InitResult` is always non-nil when `err` is nil.

`Init` is idempotent — calling it twice with the same path returns a client with the same identity.

**Changes in 0.15/0.16:**
- Default config directory is `~/.cf` (was `~/.campfire`; old path still works with a deprecation warning until v0.17)
- `Init` returns `(*Client, *InitResult, error)` — the `*InitResult` carries diagnostics
- `InitWithConfig` additionally reads `~/.cf/config.toml` and project-level `.cf/config.toml` files; see [Config section](#config-0.30) below

**Breaking changes in cf-protocol 1.0 substrate (campfireagent-db1):**
- `WithWalkUp()` and `WithNoWalkUp()` removed — center-finding is L4 (cf-discovery)
- `WalkUpEnabled()` removed from `*Client`
- `InitResult.WalkUpPath`, `InitResult.Recentered`, `InitResult.DelegationIssued` removed

### InitResult fields

```go
type InitResult struct {
    IdentityCreated  bool       // true when a new keypair was generated
    IdentityPath     string     // always populated — which keypair is in use
    StorePath        string     // absolute path to the SQLite store
    Warnings         []string   // non-fatal diagnostics

    // Populated only by InitWithConfig():
    ConfigLayers     []ConfigLayer  // every config file examined in the cascade
    IdentitySource   string         // "config" | "env" | "default"
    AutoJoined       []string       // campfire IDs auto-joined from behavior.auto_join
}
```

### Options

```go
// Override remote transport
client, result, err := protocol.Init("~/.cf", protocol.WithRemote("https://mcp.getcampfire.dev"))

// Override config directory (InitWithConfig only — has no effect on Init)
client, result, err := protocol.InitWithConfig(protocol.WithConfigDir("/custom/path"))

// Authorization hook (called before operations requiring user approval)
client, result, err := protocol.Init("~/.cf", protocol.WithAuthorizeFunc(func(desc string) (bool, error) {
    fmt.Printf("Approve: %s? [y/N] ", desc)
    var yn string
    fmt.Scan(&yn)
    return strings.ToLower(yn) == "y", nil
}))

// Explicit store and identity (advanced — bypasses Init lifecycle)
id, _ := identity.Load("/path/to/identity.json")
s, _ := store.Open("/path/to/store.db")
client := protocol.New(s, id)
```

---

## Config (0.30)

`protocol.InitWithConfig` reads a TOML config cascade. An agent with no config files behaves identically to 0.15. Config seeds protocol inputs — never outputs (trust levels, roles).

### Schema: `~/.cf/config.toml`

```toml
[identity]
file = "identity.json"      # relative to this config file's directory
display_name = ""           # sent as identity:profile on join (tainted)
# present_as is removed in 0.30 — use cf-authority scoped grants instead

[store]
file = "store.db"           # relative to this config file's directory

[transport]
type = "http"               # "http" | "fs" — creation only
                            # "github" transport removed in 0.30 (no named consumer)
endpoint = "https://mcp.getcampfire.dev"

[naming]
# root = ""                 # global config only — cannot be set in project configs
seeds = []                  # additional seed registries (beacons, hex IDs, cf:// URIs)

[behavior]
auto_join = []              # campfire IDs/beacons to join on Init()
                            # 0.30: auto-join restricted to configured namespace roots

[scope]
# campfires = []            # allowlist of campfire IDs; empty = allow all
# operation_classes = []    # "read" | "write" | "admin" | "identity"; empty = allow all
```

### Cascade resolution order (lowest to highest priority)

1. Compiled-in defaults
2. `~/.cf/config.toml` (global user config)
3. Ancestor `.cf/config.toml` files (furthest ancestor first)
4. `./.cf/config.toml` (project config at CWD)
5. `CF_HOME` env var (overrides global config root location)
6. CLI flags / functional options (explicit always wins)

**Scalar fields:** deepest config wins. Omit a field to inherit from the parent layer.

**List fields** (`naming.seeds`, `behavior.auto_join`): append by default. To replace instead of append:
```toml
behavior.auto_join = ["!replace", "beacon:only-this"]
```

**`naming.root` is global-only** — project configs cannot set it (parse-time error). Use `naming.seeds` for project-level resolution contexts.

**Security:** Config files are only loaded when the containing directory is owned by the current UID and the file is not world-writable. Symlinks are resolved before the ownership check. `identity.file` must be a relative path with no `..` components.

For the full cascade specification and security model, see `naming-trust-federation.html`.

### What InitWithConfig reads

`InitWithConfig` translates the merged config into functional options before calling `Init`:

- `transport.endpoint` → `WithRemote`
- `scope.*` → `WithScope`
- `behavior.auto_join` → auto-joined after client construction

`result.ConfigLayers` lists every file examined (including skipped files with `Skipped=true` and `SkipReason`).

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

## cf-session: Go SDK (0.30)

> **0.19 session API removed.** `client.NewSession`, `protocol.JoinSession`, and the
> `cfs1_<base64>` shared-key token format are removed in 0.30. Use `cf-session` L3 instead.

The `cf-session` L3 convention issues per-participant Ed25519 keypairs with scoped grants
from the session creator. Use the convention executor via `cf session create/join/end` CLI
commands, or call the `cf-session` convention operations directly via `convention.NewExecutor`.

```go
// Create a session via convention op (orchestrator)
exec := convention.NewExecutor(client)
result, err := exec.Execute(ctx, cfSessionOpenDecl, identityCampfireID, map[string]any{
    "ttl":                         "2h",
    "dispatcher_capability_template": capTemplate,
})
sessionID := result.CampfireID

// Worker joins — receives its own keypair + scoped grant
// (handled by cf-session's identity:introduce → delegation:grant handler)
workerClient, _, _ := protocol.Init(workerCFHome)
exec2 := convention.NewExecutor(workerClient)
exec2.Execute(ctx, cfSessionJoinDecl, sessionID, nil)
```

**Security model (0.30):**
- Each worker gets its own Ed25519 keypair — per-sender attribution is preserved
- Worker grant scope = `parent_grant ⊓ dispatcher_capability_template`
- TTL: must be > 0 and ≤ 24 hours
- Compromise of one worker compromises one grant, not the session
- Canonical authority chain lives in owner's identity campfire; session compaction safe

---

## Convention execution

`convention.NewExecutor(client)` — the self key is auto-derived from the client. Do not pass `selfKey` as a second argument (removed in 0.15).

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
    MessageID  string
    CampfireID string
    Sender     string         // public key hex
    Args       map[string]any // parsed and validated per declaration
    Tags       []string
    Identity   convention.IdentityInfo // resolved when WithIdentityResolver is set
}

type Response struct {
    Payload        any    // JSON-serializable; nil = no body
    Tags           []string // additional tags beyond auto-added "fulfills"
    TokensConsumed int64    // LLM tokens consumed; recorded for billing if > 0
}
```

When a handler returns a `*Response`, the Server sends it with `Antecedents: [req.MessageID]` and tag `"fulfills"` — so `client.Await(targetMsgID)` resolves automatically.

### Identity resolution (optional)

Attach an `IdentityResolver` to resolve sender pubkeys to display names before dispatch:

```go
srv.WithIdentityResolver(myResolver)
// req.Identity.MachineKey is always set (valid hex Ed25519 key)
// req.Identity.DisplayName is set when the resolver has a cache entry
```

Pass `nil` to `WithIdentityResolver` to reset to the no-op resolver.

---

## Display Names

Set a human-readable display name at init time:

```bash
cf init --display-name "My Agent"
```

The name is stored in `~/.cf/profile.json` and published as an `identity:profile` message when joining campfires (best-effort, non-blocking). Display names are **tainted** — useful for display, never for access control. Another agent can claim any display name.

In 0.16 the display name can also be set in config:

```toml
# ~/.cf/config.toml
[identity]
display_name = "My Agent"
```

---

## Integration hierarchy

| Building... | Use | Why |
|-------------|-----|-----|
| A backend service | **Go SDK** (`protocol.Client` + `convention.Server`) | Full lifecycle, Subscribe, typed handlers |
| An AI agent workflow | **`cf` CLI** | Convention commands from any language, shell-friendly |
| An AI agent via tool calling | **`cf-mcp` MCP server** | Convention tools auto-register on join |

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

## Migration Guide: 0.14 → 0.16

### Breaking changes

**`NewExecutor` no longer takes `selfKey`** (0.15)

```go
// Before (0.14):
exec := convention.NewExecutor(client, client.PublicKeyHex())

// After (0.15+):
exec := convention.NewExecutor(client)
// selfKey is derived automatically from client.PublicKeyHex()
```

**`Init` returns three values** (0.15)

```go
// Before (0.14):
client, err := protocol.Init("~/.campfire")

// After (0.15+):
client, result, err := protocol.Init("~/.cf")
// result is *InitResult; ignore it if you don't need diagnostics
_ = result
```

**Walk-up removed in cf-protocol 1.0 (campfireagent-db1)**

`WithWalkUp()`, `WithNoWalkUp()`, and `WalkUpEnabled()` are removed. Remove all call sites.
Center-finding is now L4 work (cf-discovery).

**Default config path is `~/.cf`** (0.15)

The old `~/.campfire` path still works with a deprecation warning until v0.17. Migrate:

```bash
mv ~/.campfire ~/.cf
```

Or set `CF_HOME` to override the path entirely.

### New in 0.16: `InitWithConfig`

```go
// 0.16+: reads ~/.cf/config.toml and project .cf/config.toml cascade
client, result, err := protocol.InitWithConfig()
```

`Init(configDir)` continues to work without modification. Switch to `InitWithConfig` when you want config file support, auto-join, or display name persistence via config.

### Summary table

| Feature | 0.14 | 0.15 | 0.16 | cf-protocol 1.0 |
|---------|------|------|------|-----------------|
| Config path | `~/.campfire` | `~/.cf` (fallback to `~/.campfire`) | `~/.cf` | same |
| `Init` signature | `(*Client, error)` | `(*Client, *InitResult, error)` | same | same |
| `InitWithConfig` | — | — | `(*Client, *InitResult, error)` | same |
| Walk-up | on | off (opt-in via `WithWalkUp`) | off | **removed** (L4) |
| `NewExecutor` | `(client, selfKey)` | `(client)` | same | same |
| `config.toml` | — | — | supported | same |
| Session tokens | — | `client.NewSession` / `JoinSession` | same | **removed** (cf-session L3 replaces) |
| Display names | — | `cf init --display-name` | `identity.display_name` in config | same |
| `present_as` | — | — | inert | **removed** (cf-authority grants replace) |
| GitHub transport | — | supported | supported | **removed** (no named consumer) |

---

## 0.30 L3 Package Reference

The 0.30 architecture organizes RFC conventions into the `cf-conventions` module with
seven internal packages. The old `pkg/` layout is superseded.

| Package | Location (0.30) | Purpose |
|---|---|---|
| `cf-authority` | `cf-conventions/cf-authority/` | Scoped grants, bounded-transitivity chain evaluator, `GateEvaluator` interface. Sub-packages: `trust/`, `delegation/`, `provenance/`. See `cf-authority-spec.md`. |
| `cf-discovery` | `cf-conventions/cf-discovery/` | Naming, beacon snippets, rate-limit declarations. See `cf-discovery-spec.md`. |
| `cf-identity` | `cf-conventions/cf-identity/` | `introduce-me`, `declare-home`, `verify-me`, `list-homes`, `echo` ceremony, `identity:revoked`. |
| `cf-session` | `cf-conventions/cf-session/` | Ephemeral-identity convention. Per-participant Ed25519 keys with scoped grants from session creator. |
| `cf-convention-extension` | `cf-conventions/cf-convention-extension/` | `promote`, `supersede` — convention lifecycle management (absorbs `seed.go`). |
| `cf-connect` | `cf-conventions/cf-connect/` | Peering convention. |
| `cf-durability` | `cf-conventions/cf-durability/` | Durability convention. |

Layer 1 primitives (`cf-protocol` module, `internal/` packages) are not imported directly
by application code. Use `protocol.Client` — the public surface of `cf-protocol`.

**L4 binary split:** The `cf` CLI exposes convention operations only. `cf-primitives` is
the separate binary for low-level protocol surface (init, send, read, members, scope).
Agents that need to bypass conventions use `cf-primitives`, not raw sqlite. Default
symlinks to `cf` get conventions only; app authors who need primitives symlink to
`cf-primitives` explicitly.

---

## See also

- `cf-protocol/internal/` — `Client`, `SendRequest`, `ReadRequest`, `AwaitRequest`, `SubscribeRequest`, `CreateRequest`, `JoinRequest` (0.30; was `pkg/protocol/`)
- `cf-conventions/cf-convention/` — `Server`, `Executor`, `Declaration`, `ArgDescriptor` (0.30; was `pkg/convention/`)
- `cf-conventions/cf-authority/trust/` — `GateEvaluator` interface and conformance harness (0.30)
- `cf-conventions/cf-discovery/` — naming, beacons, snippet schema (0.30; was `pkg/naming/` + `pkg/beacon/`)
- [cf-authority-spec.md](cf-authority-spec.md) — wire schema for grants and gate predicates
- [cf-discovery-spec.md](cf-discovery-spec.md) — snippet schema and discovery tier spec
- [Protocol spec](protocol-spec.md) — 0.19 message envelope reference (superseded; see 0.30 note at top)
- [CLI reference](cli-conventions.md) — the same operations, from the command line
- [MCP server reference](mcp-conventions.md) — conventions as auto-generated MCP tools
- [Migration guide](#migration-guide-014--016) — upgrading from 0.14
- [Naming trust and federation](naming-trust-federation.html) — config cascade full spec, naming root security model
