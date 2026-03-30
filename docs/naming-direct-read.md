# Naming: Direct-Read Resolution

## Design

Name resolution in campfire uses **direct-read** semantics. When you call `naming.Resolve()`, the resolver reads naming convention messages directly from the campfire's message store. There is no RPC call, no futures/await cycle, and no dedicated naming service process.

This means:

- **Resolution is a read, not a request.** The resolver queries the local store (synced from the transport) for `naming:register` messages matching the requested name. No outbound message is sent.
- **No timeout/retry failure modes.** Because resolution doesn't depend on another agent being online to fulfill a request, the only failure mode is "name not found" — not "naming service unreachable" or "request timed out."
- **Consistency is eventual.** A name registered on one node is visible to others after the next transport sync. For filesystem campfires, this is immediate (shared directory). For HTTP campfires, it depends on the poll interval.

## Why not RPC-based resolution?

An earlier design considered a directory service that would handle `naming:resolve` requests via the futures/await pattern. This was deferred because:

1. **Unnecessary complexity.** Naming data is already in the campfire message stream. Reading it directly is simpler than routing through a service.
2. **Availability dependency.** An RPC-based resolver requires the directory service to be running. Direct-read works as long as the campfire store is synced.
3. **Cost.** Every RPC resolution would produce two messages (request + fulfillment). Direct-read produces zero messages for reads — only writes (registrations) generate messages.

The `docs/directory-service.md` design is deferred. If cross-namespace discovery or access-controlled resolution becomes necessary, it can be layered on top of the current direct-read foundation without changing the `naming.Resolve()` API.

## API

```go
// Register a name (writes a naming:register message)
naming.Register(ctx, client, campfireID, "search", targetID, nil)

// Resolve a name (reads from local store — no messages sent)
resp, _ := naming.Resolve(ctx, client, campfireID, "search")

// List all registrations (reads from local store)
registrations, _ := naming.List(ctx, client, campfireID)

// Hierarchical resolution across namespaces
resolver := naming.NewResolverFromClient(client, rootID)
result, _ := resolver.ResolveURI(ctx, "cf://child.leaf")
```

## See also

- [`pkg/naming/`](../pkg/naming/) — implementation
- [SDK reference](convention-sdk.md#naming) — naming section in the SDK guide
