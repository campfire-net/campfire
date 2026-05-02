# cf-session Lifecycle — cf 0.30

`cf-session` is the L3 ephemeral-identity convention for parallel agent coordination. Each participant gets its own Ed25519 keypair with a scoped grant from the session creator — per-sender attribution is preserved throughout the session.

The 0.19 shared-key session token model (`cfs1_<base64>`) is **removed in 0.30**. Do not use it.

## When to use cf-session

Use cf-session when you need:
- Short-lived, attributed coordination (swarm dispatches, parallel tool-to-tool pipes)
- Per-worker audit trails without pre-provisioning worker identities
- Revocation granularity: compromise of one worker compromises one grant, not the session

Use `cf join` for durable campfires with persistent membership.

## Orchestrator flow

```bash
# 1. Create a session campfire (TTL required; max 24h)
SESSION_ID=$(cf session create --ttl 2h)

# 2. Share the session ID with workers out-of-band
#    Workers join; the orchestrator's handler issues grants lazily

# 3. Read the session to monitor worker activity
cf session read "$SESSION_ID"

# 4. End when done (session messages become eligible for compaction)
cf session end "$SESSION_ID"
```

## Worker flow

```bash
# Worker joins using the session ID (received out-of-band)
cf session join "$SESSION_ID"

# Worker can now call convention operations scoped by the session grant
cf "$SESSION_ID" <operation> --<arg> <value>
```

## Lazy-mint issuance

No grants are pre-minted at session open. When a worker presents itself (`identity:introduce` into the session campfire), the orchestrator's session handler responds with a `delegation:grant` scoped as:

```
parent_grant ⊓ capability_template
```

Workers can never exceed the template's scope. The capability template is embedded in the `session:open` event.

## Key handling backends

Two backends, selected in the session declaration's `key_handling` field:

**`jail` (default)** — orchestrator generates the worker's Ed25519 keypair and writes it to a 0700 jail directory. The worker process reads the key from the file. The key never crosses a network boundary. Trust boundary: the parent process (equivalent to `sudo`).

**`signing_proxy` (opt-in)** — orchestrator retains the worker key and exposes a Unix socket. The worker calls the socket for every signature. The worker process never holds the private key. The proxy enforces the grant's `where` and `op_glob` constraints on each signing request. Requires a resident daemon; fails on stateless deployments.

Choose `signing_proxy` when the worker process is untrusted code and you need the key protected at the process boundary. Use `jail` for trusted worker agents (the common case).

## Canonical grant log

Coordination messages in the session campfire are ephemeral and eligible for compaction after session close (default 24h retention). However, **issued grants live in the owner's identity campfire**, not in the session campfire. Compacting a closed session does not break the authorization re-walk from worker → orchestrator → human root.

## Go SDK (0.30)

```go
// Orchestrator opens a session
exec := convention.NewExecutor(client)
result, err := exec.Execute(ctx, cfsession.OpenDeclaration(), identityCampfireID, map[string]any{
    "session_id":               sessionID,
    "parent_grant_chain_root":  myPubKeyHex,
    "capability_template":      capTemplate,  // CapabilityTemplate struct
    "until":                    expiry.UnixNano(),
    "key_handling":             "jail",
})

// Worker joins
workerClient, _, _ := protocol.InitWithConfig()
exec2 := convention.NewExecutor(workerClient)
exec2.Execute(ctx, cfsession.IntroduceWorkerDeclaration(), sessionID, map[string]any{
    "worker_pubkey_hex": workerClient.PublicKeyHex(),
})
// The orchestrator's handler responds with a delegation:grant automatically.
```

## Capacity template fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `convention` | string | yes | Convention name the session grants access to |
| `op_glob` | string | yes | Op pattern: `"*"` or `"claim-item\|complete"` |
| `until` | time.Time | yes | Absolute expiry; worker grants inherit this as their max TTL |
| `where` | string | no | Optional campfire ID constraint; empty = no restriction |

## See also

- `docs/agent/gate-predicates.md` — scoped grant predicates
- `docs/agent/failure-modes.md` — session expiry and revocation
- `cf-conventions/cf-session/session.go` — Go implementation
- `docs/convention-sdk.md` §cf-session — full SDK reference
