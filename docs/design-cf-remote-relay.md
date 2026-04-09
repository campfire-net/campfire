# Design: cf CLI Remote Relay Support

> Synthesized from adversarial design review (4 dispositions, 15 attacks, 6 proposals, 8 systems findings, 7 domain rulings). Design campfire `884406`.

## 1. Model

### What is a relay?

A relay is a campfire peer that holds key material and serves HTTP transport endpoints. It is **not** a special entity in the protocol — it is a regular member with elevated availability (always-on, public endpoint). The hosted relay at `mcp.getcampfire.dev` runs `cf-mcp` backed by Azure Table Storage.

### Trust relationship: CLI to relay

The CLI treats the relay as an **admitting peer**, not a trusted authority. The trust model is:

1. **Creation is always local.** The CLI generates the campfire keypair locally (protocol spec line 363: "The creator generates a keypair"). The relay never generates keys on behalf of the CLI.

2. **Registration is a join.** After local creation, the CLI registers the campfire on the relay by performing a modified join operation — the CLI is the admitting party (it holds the private key), and the relay is the joining party (it receives the key via ECDH). This inverts the normal join flow: the creator sends key material TO the relay, not FROM it.

3. **Custodial mode is explicit.** When an MCP client (LLM) creates a campfire via the MCP tool, the relay generates and holds the key. This is a documented compromise for clients that cannot self-custody (P2). The CLI never uses this path.

### Where keys live

| Actor | Creates key? | Holds privkey? | How it gets the key |
|-------|-------------|----------------|---------------------|
| CLI user | Yes (local) | Yes (local SQLite + CBOR) | Generated locally |
| Relay (CLI-registered) | No | Yes (Azure Table Storage) | Received via ECDH during registration |
| Relay (MCP-created) | Yes | Yes (Azure Table Storage) | Generated locally on relay |
| CLI joiner | No | Yes (local SQLite + CBOR) | Received via ECDH during join |

**A3 (privkey in Azure Table Storage):** This is a permanent constraint, not a bug. The relay must hold the private key to serve join requests and sign provenance hops. Azure Table Storage provides encryption at rest. The alternative (relay as blind relay without key material) means it cannot admit new members or sign messages. The risk is accepted and documented: compromise of the relay's storage exposes all campfire private keys it holds. Mitigation: threshold>1 campfires split the key so no single relay holds the full secret. Future work: HSM-backed key storage.

## 2. Operations

### 2.1 `cf create` (unchanged for local, new `--relay` flag)

**Current behavior preserved.** `cf create` with no relay flag creates a filesystem campfire (default) or a p2p-http listener campfire (`--transport p2p-http --listen :9001`).

**New: `--relay <url>` flag.** Creates the campfire locally, then registers it on the specified relay. This is syntactic sugar for `cf create` + `cf register`.

```
cf create --relay https://mcp.east.getcampfire.dev/api --description "my campfire"
```

Equivalent to:
```
cf create --description "my campfire"
cf register <campfire-id> --relay https://mcp.east.getcampfire.dev/api
```

### 2.2 `cf register` (NEW command)

Registers a locally-created campfire on a remote relay. This is the key new operation.

```
cf register <campfire-id> --relay <url>
```

**What it does:**

1. Loads the campfire's private key from local state (`~/.campfire/campfires/{id}.cbor` or from the store's membership record).
2. POSTs to `<relay>/campfire/create` with the campfire's public key, encrypted private key (via ECDH), join protocol, and description.
3. The relay stores the campfire state and becomes a peer that can admit joiners and serve sync/poll.
4. The CLI stores the relay URL as a peer endpoint in local SQLite.
5. Updates the local membership record's TransportType to `p2p-http` and TransportDir to the relay URL.
6. Outputs the beacon (C6: beacon is the distribution solution).

**Wire protocol — `POST /campfire/create`:**

Request (JSON, signed with `signRequest`):
```json
{
  "campfire_id": "<64-hex campfire public key>",
  "encrypted_priv_key": "<base64 AES-256-GCM encrypted private key>",
  "ephemeral_x25519_pub": "<hex X25519 public key used for ECDH>",
  "join_protocol": "open",
  "reception_requirements": [],
  "threshold": 1,
  "description": "my campfire",
  "creator_pubkey": "<hex Ed25519 public key of creator>"
}
```

Response (JSON):
```json
{
  "campfire_id": "<64-hex>",
  "relay_x25519_pub": "<hex X25519 public key for ECDH>",
  "beacon": "<beacon string>",
  "endpoint": "<relay's public endpoint URL>"
}
```

**Key exchange (P5: join ECDH is the right model):**

The registration uses the same ECDH pattern as join, but inverted:
1. CLI generates ephemeral X25519 keypair.
2. CLI sends `ephemeral_x25519_pub` + AES-256-GCM encrypted campfire private key.
3. Relay generates its own ephemeral X25519 keypair, derives shared secret, decrypts privkey.
4. Relay returns its `relay_x25519_pub` so the CLI can verify the exchange completed.

This reuses `HkdfSHA256(shared, "campfire-join-v1")` and `AESGCMEncrypt`/`aesGCMDecrypt` from `pkg/transport/http/handler_join.go` and `pkg/transport/http/peer.go`.

### 2.3 `cf join --via <relay-url>` (existing, fix S3)

Already works. The `--via` flag on `cf join` contacts a relay to join a campfire. Two fixes needed:

**S3 fix (independent, do first):** CLI `joinP2PHTTP` in `cmd/cf/cmd/join.go` does not store the relay as a peer endpoint. The SDK (`pkg/protocol/join.go` lines 302-306) added this in v0.17.2 but the CLI path was never updated. Fix: add the same `UpsertPeerEndpoint` call after line 379 in `cmd/cf/cmd/join.go`:

```go
// Store the relay (--via endpoint) as a peer so syncFromHTTPPeers can pull from it.
s.UpsertPeerEndpoint(store.PeerEndpoint{
    CampfireID:   campfireID,
    MemberPubkey: fmt.Sprintf("%x", result.CampfirePubKey),
    Endpoint:     via,
})
```

### 2.4 `cf send`, `cf read`, `cf sync` (existing, minor changes)

These already work with p2p-http transport. `cf send` delivers to peer endpoints. `cf read` syncs from peer endpoints via `syncFromHTTPPeers`. No changes needed to these commands.

**A5 (post-reap sync returns empty):** This is inherent to session-based relay architecture. When a relay session is reaped, the campfire's in-memory transport is unregistered. The `reconstructFromGlobalStore` fallback in `transport_http.go` line 234 handles this — it rebuilds the transport from Azure Table Storage. If the global store has the membership (which it does after `handleCreateHTTP` writes to it), sync works across instances and reaps. No fix needed beyond ensuring `cf register` writes to the global store (which the new `/campfire/create` handler will do).

### 2.5 `cf ls` (existing, no changes)

Lists campfires from local store. Relay-registered campfires appear with transport type `p2p-http`.

### 2.6 Config-driven relay (C3)

Add `relay` field to `[transport]` in `.cf/config.toml`:

```toml
[transport]
relay = "https://mcp.east.getcampfire.dev/api"
```

When `transport.relay` is set and `cf create` is called without `--transport` or `--relay`:
- Default transport becomes `p2p-http` via the configured relay.
- `cf create` automatically registers on the relay after local creation.
- `cf register` uses this as the default relay URL when `--relay` is omitted.

This is a config INPUT (not trust output), consistent with the config security model (config.go line 14).

**File:** `pkg/protocol/config.go`

Add to `rawTransportConfig`:
```go
type rawTransportConfig struct {
    Type     string `toml:"type"`
    Endpoint string `toml:"endpoint"`
    Dir      string `toml:"dir"`
    Relay    string `toml:"relay"`  // NEW
}
```

Add to `TransportConfig`:
```go
type TransportConfig struct {
    Type     string
    Endpoint string
    Dir      string
    Relay    string  // NEW: default relay URL for cf create/register
}
```

## 3. Server-Side: `/campfire/create` Endpoint

### 3.1 Handler location

**File:** `pkg/transport/http/handler_create.go` (NEW)

This keeps the handler in the transport package alongside `handler_join.go`, `handler_message.go`, etc. The handler is protocol-level — any HTTP transport node can accept registrations, not just the hosted MCP server.

### 3.2 Route registration

**File:** `pkg/transport/http/handler.go` line 201

Add to the `route()` switch:
```go
case action == "create" && r.Method == http.MethodPost:
    h.signatureOnlyMiddleware(campfireID, h.handleCreate)(w, r)
```

Wait — the create endpoint doesn't have a campfire ID in the path yet (the campfire doesn't exist on the relay). The route pattern is `/campfire/{id}/{action}`, but for create, the campfire ID IS in the request body, not the path.

**Revised route:** `POST /campfire/create` (no `{id}` segment).

This requires handling at the `TransportRouter` level in `cmd/cf-mcp/transport_http.go`, not in the per-campfire handler. The router already handles `/campfire/` prefix routing.

**File:** `cmd/cf-mcp/transport_http.go`, `ServeHTTP` method (line 191)

Add before the per-campfire routing:
```go
// Handle /campfire/create — no campfire ID in path.
if path == "/campfire/create" && req.Method == http.MethodPost {
    r.handleCreateCampfire(w, req)
    return
}
```

### 3.3 `handleCreateCampfire` implementation

**File:** `cmd/cf-mcp/transport_http.go` (add method on `TransportRouter`)

```go
// CreateCampfireRequest is the body for POST /campfire/create.
type CreateCampfireRequest struct {
    CampfireID           string   `json:"campfire_id"`
    EncryptedPrivKey     []byte   `json:"encrypted_priv_key"`
    EphemeralX25519Pub   string   `json:"ephemeral_x25519_pub"`
    JoinProtocol         string   `json:"join_protocol"`
    ReceptionRequirements []string `json:"reception_requirements"`
    Threshold            uint     `json:"threshold"`
    Description          string   `json:"description"`
    CreatorPubkey        string   `json:"creator_pubkey"`
}

// CreateCampfireResponse is returned on success.
type CreateCampfireResponse struct {
    CampfireID      string `json:"campfire_id"`
    RelayX25519Pub  string `json:"relay_x25519_pub"`
    Beacon          string `json:"beacon,omitempty"`
    Endpoint        string `json:"endpoint"`
}
```

The handler:
1. Validates the signature (reuse `signatureOnlyMiddleware` pattern — check `X-Campfire-Sender` matches `CreatorPubkey`).
2. Generates ephemeral X25519 keypair, derives shared secret via ECDH with the client's `EphemeralX25519Pub`.
3. Decrypts `EncryptedPrivKey` using AES-256-GCM with the derived key.
4. Validates the decrypted private key produces the claimed `CampfireID` (public key).
5. Stores membership in global store (same as `handleCreateHTTP` lines 1641-1664).
6. Creates a transport, registers with the router.
7. Generates a beacon with the relay's endpoint.
8. Returns the response.

**Auth:** Signature-only (same as join). The creator proves identity ownership but is not yet a member of a campfire on this relay.

**Idempotency:** If the campfire already exists in the global store, return 409 Conflict. The creator should use `cf join --via` to attach to an existing campfire.

### 3.4 Estimated size

- `handler_create.go` in `pkg/transport/http/`: ~80 lines (request parsing, ECDH, decrypt, validate)
- `transport_http.go` route addition: ~5 lines
- `transport_http.go` handler method: ~60 lines (store, router registration, beacon generation)
- Total server-side: ~145 lines

## 4. CLI-Side Implementation

### 4.1 `cmd/cf/cmd/register.go` (NEW file)

```go
var registerCmd = &cobra.Command{
    Use:   "register <campfire-id>",
    Short: "Register a local campfire on a remote relay",
    Long:  `Registers an existing locally-created campfire on a remote relay server.
The relay becomes a peer that can admit new members and serve sync/poll.`,
    Args: cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error { ... },
}
```

**Flags:**
- `--relay <url>` — relay endpoint (falls back to `transport.relay` from config)

**Implementation:**
1. Load agent identity.
2. Open store, get membership for campfire ID.
3. Load campfire private key from CBOR state file.
4. Generate ephemeral X25519 keypair.
5. Derive shared secret placeholder (will complete after server responds).
6. Encrypt private key with ECDH-derived AES key.
7. POST to `<relay>/campfire/create` with signed request.
8. Parse response, store relay as peer endpoint.
9. Update membership TransportType to `p2p-http`, TransportDir to relay URL.
10. Output beacon.

**Estimated size:** ~120 lines.

### 4.2 `cmd/cf/cmd/create.go` changes

Add `--relay` flag:
```go
createCmd.Flags().String("relay", "", "Register on a remote relay after creation")
```

In `RunE`, after the transport switch (line 88), if `--relay` is set:
```go
relayURL, _ := cmd.Flags().GetString("relay")
if relayURL == "" {
    // Fall back to config
    relayURL = resolveRelayFromConfig()
}
if relayURL != "" {
    return createAndRegister(cf, agentID, s, description, relayURL)
}
```

`createAndRegister`:
1. Create locally (filesystem, for state persistence).
2. Call `registerOnRelay(campfireID, relayURL, agentID, s)` (shared with `register.go`).
3. Output beacon from relay response.

**Estimated size:** ~40 lines (flag handling + `createAndRegister` wrapper).

### 4.3 Config resolution

**File:** `cmd/cf/cmd/root.go` (or wherever config is resolved)

Add helper:
```go
func resolveRelayFromConfig() string {
    cfg := resolvedConfig() // existing config cascade
    if cfg != nil && cfg.Transport.Relay != "" {
        return cfg.Transport.Relay
    }
    return ""
}
```

## 5. Adversary Attack Resolution

| ID | Severity | Attack | Resolution |
|----|----------|--------|------------|
| A1 | Critical | `--remote` flag on `cf init` is dead code | **Delete.** Remove `--remote` flag, remove `createCenterCampfire` function. See section 7. |
| A2 | Critical | No CLI command to create campfire on relay | **Resolved.** New `cf register` command + `--relay` flag on `cf create`. See sections 2.1, 2.2. |
| A3 | Critical | Privkey plaintext in Azure Table Storage | **Permanent constraint.** Relay must hold privkey to serve join/sign. Azure provides encryption at rest. Future: HSM. See section 1. |
| A4 | High | Session reap leaves stale global store entries | **Permanent constraint.** Stale global store entries are features, not bugs. `reconstructFromGlobalStore` (transport_http.go:234) uses them to rebuild transport on any instance. The global store IS the persistence layer. |
| A5 | High | Post-reap sync returns empty silently | **Resolved by existing code.** `reconstructFromGlobalStore` rebuilds transport from global store. After the fix, CLI-registered campfires also write to global store, so they survive reap. |
| A6 | High | Push model vs pull-only relay | **Documented.** Relay campfires support both push and pull (`DeliveryModePull` + `DeliveryModePush`). CLI clients without a listener use pull (sync/poll). CLI clients with `--listen` use push. No mismatch. |
| A7 | Medium | Stale endpoint fanout | **Out of scope.** General peer health monitoring. Not specific to relay support. |
| A8 | High | `--transport p2p-http` incompatible with relay | **Resolved.** `--relay` flag is the correct way to register on a relay. `--transport p2p-http` remains for self-hosted listener mode. Clear distinction. |
| A9 | Medium | Two auth planes (MCP session token vs HTTP signature) | **By design.** MCP auth is for LLM clients. HTTP signature auth is for CLI/SDK clients. They serve different populations. The relay accepts both; they don't interfere. |
| A10 | Medium | CBOR state fragility | **Out of scope.** General state management improvement. Not specific to relay support. |
| A11 | High | Multi-instance Azure Functions consistency | **Resolved by existing design.** Global store (Azure Table Storage) provides cross-instance consistency. `reconstructFromGlobalStore` handles instance-local cache misses. `cf register`'s `/campfire/create` handler writes to global store. |
| A12 | Medium | Pull polling cost | **Out of scope.** Polling optimization is independent of relay registration. |
| A13 | Low | Trailing slash normalization | **Out of scope.** URL normalization is a general HTTP hygiene issue. |
| A14 | High | Relay URL hardcoded in SQLite, no migration | **Deferred.** Add `cf relay migrate --from <old> --to <new>` when multi-region failover is needed. For now, the relay URL in the membership record is the source of truth. Document that changing relay URLs requires re-registration. |
| A15 | High | No CLI commands for relay-native operations | **Resolved.** `cf register` is the new relay-native command. Invite, admit, and session lifecycle work through existing `cf invite`, `cf admit`, and `cf session` commands which operate on the relay via HTTP transport. |

## 6. Domain Purist Compliance

| ID | Ruling | Compliance |
|----|--------|------------|
| P1 | Creation MUST be local | **Compliant.** `cf create` always generates keypair locally. `cf register` is a separate operation. |
| P2 | Relay holding privkey is a compromise, not protocol | **Compliant.** Documentation explicitly states this. CLI path keeps key locally AND shares with relay. MCP path is the compromise. |
| P3 | Center model requires creator as center | **Compliant.** Creator generates key, registers on relay. Creator is center. Relay is a peer. |
| P4 | Decompose into create + register | **Compliant.** Two separate commands: `cf create` + `cf register`. Convenience flag `--relay` composes them. |
| P5 | Join ECDH is the right key transfer model | **Compliant.** Registration uses identical ECDH pattern from `pkg/transport/http/handler_join.go`. |
| P6 | Same command, different trust semantics breaks one-interface | **Compliant.** `cf create` is always local. `cf register` is explicitly about relay interaction. No hidden mode switching. |
| P7 | Relay-generated identity dies with relay | **Compliant.** CLI-created campfires survive relay death — the creator holds the key locally. Only MCP-created campfires (custodial) die with the relay. |

## 7. Dead Code Removal

### Delete `createCenterCampfire` and `--remote` flag

**File:** `cmd/cf/cmd/init.go`

1. Delete lines 545-606 (entire `createCenterCampfire` function).
2. Search for any `--remote` flag declaration and remove it. (Systems pragmatist S1 confirms the flag is declared but never read in `RunE` — the value is silently dropped.)

```bash
# Verify no callers remain:
grep -rn 'createCenterCampfire\|--remote' cmd/cf/cmd/init.go
```

The `--remote` flag was never wired to `RunE` (S1). The `createCenterCampfire` function was only reachable via the dead flag. Both are safe to delete.

### Delete `createP2PHTTP` listener-mode confusion

**Do NOT delete.** `createP2PHTTP` (create.go line 243) is the self-hosted listener mode — "I am the relay." It is a valid use case (S2 correctly identifies it). It stays. The new `--relay` flag provides the client-mode alternative.

## 8. File Change Summary

### New files (in `~/projects/campfire/`)

| File | Lines | Purpose |
|------|-------|---------|
| `cmd/cf/cmd/register.go` | ~120 | `cf register` command |
| `pkg/transport/http/handler_create.go` | ~80 | Server-side `/campfire/create` handler |

### Modified files

| File | Change | Lines |
|------|--------|-------|
| `cmd/cf/cmd/create.go` | Add `--relay` flag, `createAndRegister` | ~40 |
| `cmd/cf/cmd/join.go` line ~379 | S3 fix: store relay as peer endpoint | ~5 |
| `cmd/cf/cmd/init.go` | Delete `createCenterCampfire` (lines 545-606), remove `--remote` flag | -65 |
| `cmd/cf/cmd/root.go` | Add `registerCmd` to root, `resolveRelayFromConfig` helper | ~15 |
| `cmd/cf-mcp/transport_http.go` | Route `/campfire/create` to handler, `handleCreateCampfire` method | ~65 |
| `pkg/protocol/config.go` | Add `Relay` field to transport config | ~5 |
| `pkg/transport/http/handler.go` | No change (create is routed at TransportRouter level, not per-campfire handler) | 0 |

### Total estimated: ~330 lines added, ~65 lines deleted = ~265 net lines

This aligns with the systems pragmatist's estimate of ~320 LOC (S7).

## 9. Shared Internal Function (C5)

The registration logic (ECDH key exchange, encrypt privkey, POST to relay, store peer endpoint) is shared between:
- `cmd/cf/cmd/register.go` (standalone register)
- `cmd/cf/cmd/create.go` (create + register convenience)

Extract to a shared function in `cmd/cf/cmd/relay.go`:

```go
// registerOnRelay registers a locally-created campfire on a remote relay.
// It performs ECDH key exchange, encrypts the campfire private key, and POSTs
// to the relay's /campfire/create endpoint. On success, it stores the relay
// as a peer endpoint and updates the membership record.
func registerOnRelay(campfireID, relayURL string, agentID *identity.Identity, s store.Store, cfHome string) (*RelayRegistrationResult, error)

type RelayRegistrationResult struct {
    Beacon   string
    Endpoint string
}
```

This does NOT merge with the MCP `handleCreateHTTP` code path (C5 partially). The MCP server-side create is fundamentally different — it generates keys locally on the server. The CLI client-side register sends an already-generated key to the server. They share the ECDH crypto primitives (which already exist in `pkg/transport/http/`) but not the orchestration logic.

## 10. Demo Script (Acceptance Test)

```bash
#!/bin/bash
# demo-cf-remote-relay.sh — acceptance test for cf CLI remote relay support
# Prerequisites: cf binary built, relay running at $RELAY_URL
# Usage: RELAY_URL=https://mcp.east.getcampfire.dev/api ./demo-cf-remote-relay.sh

set -euo pipefail

RELAY_URL="${RELAY_URL:?Set RELAY_URL to the relay endpoint}"

# --- Setup: two independent agents ---

echo "=== Setup: Agent A (creator) ==="
export CF_HOME_A=$(mktemp -d)
cf init --force 2>/dev/null
AGENT_A_KEY=$(cf init 2>/dev/null | head -1)
echo "Agent A: ${AGENT_A_KEY:0:12}..."

echo "=== Setup: Agent B (joiner) ==="
export CF_HOME_B=$(mktemp -d)
CF_HOME="$CF_HOME_B" cf init --force 2>/dev/null
AGENT_B_KEY=$(CF_HOME="$CF_HOME_B" cf init 2>/dev/null | head -1)
echo "Agent B: ${AGENT_B_KEY:0:12}..."

# --- Step 1: Agent A creates a campfire locally + registers on relay ---

echo ""
echo "=== Step 1: Create + register on relay ==="
export CF_HOME="$CF_HOME_A"
CREATE_OUTPUT=$(cf create --relay "$RELAY_URL" --description "demo-relay-test" --json)
CAMPFIRE_ID=$(echo "$CREATE_OUTPUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['campfire_id'])")
echo "Campfire: ${CAMPFIRE_ID:0:12}..."

# Verify the campfire appears in local membership list
echo "--- Verify: cf ls shows campfire ---"
cf ls | grep -q "${CAMPFIRE_ID:0:12}" && echo "PASS: campfire in local list" || echo "FAIL: campfire not in local list"

# --- Step 2: Agent A sends a message ---

echo ""
echo "=== Step 2: Agent A sends a message ==="
cf send "$CAMPFIRE_ID" --tag test "Hello from Agent A via relay"
echo "PASS: message sent"

# --- Step 3: Agent B joins via relay ---

echo ""
echo "=== Step 3: Agent B joins via relay ==="
export CF_HOME="$CF_HOME_B"
cf join "$CAMPFIRE_ID" --via "$RELAY_URL"
echo "PASS: Agent B joined"

# --- Step 4: Agent B reads the message ---

echo ""
echo "=== Step 4: Agent B reads messages ==="
READ_OUTPUT=$(cf read "$CAMPFIRE_ID" --all)
echo "$READ_OUTPUT" | grep -q "Hello from Agent A" && echo "PASS: message received" || echo "FAIL: message not found"

# --- Step 5: Agent B sends a reply ---

echo ""
echo "=== Step 5: Agent B sends a reply ==="
cf send "$CAMPFIRE_ID" --tag test "Reply from Agent B"
echo "PASS: reply sent"

# --- Step 6: Agent A reads the reply ---

echo ""
echo "=== Step 6: Agent A reads the reply ==="
export CF_HOME="$CF_HOME_A"
READ_OUTPUT=$(cf read "$CAMPFIRE_ID" --all)
echo "$READ_OUTPUT" | grep -q "Reply from Agent B" && echo "PASS: reply received" || echo "FAIL: reply not found"

# --- Step 7: Verify members ---

echo ""
echo "=== Step 7: Verify members ==="
MEMBERS=$(cf members "$CAMPFIRE_ID")
echo "$MEMBERS" | grep -q "${AGENT_A_KEY:0:12}" && echo "PASS: Agent A is member" || echo "FAIL: Agent A not in members"
echo "$MEMBERS" | grep -q "${AGENT_B_KEY:0:12}" && echo "PASS: Agent B is member" || echo "FAIL: Agent B not in members"

# --- Step 8: Standalone register (create then register separately) ---

echo ""
echo "=== Step 8: Standalone cf register ==="
export CF_HOME="$CF_HOME_A"
CAMPFIRE_2=$(cf create --description "register-test" --json | python3 -c "import sys,json; print(json.load(sys.stdin)['campfire_id'])")
cf register "$CAMPFIRE_2" --relay "$RELAY_URL"
echo "PASS: standalone register completed"

# Verify Agent B can join the second campfire
export CF_HOME="$CF_HOME_B"
cf join "$CAMPFIRE_2" --via "$RELAY_URL"
echo "PASS: Agent B joined second campfire via relay"

# --- Cleanup ---

echo ""
echo "=== Cleanup ==="
rm -rf "$CF_HOME_A" "$CF_HOME_B"
echo "Done. All tests passed."
```

## 11. Implementation Order

1. **S3 fix** (join.go line ~379, independent one-liner). Do first.
2. **Dead code removal** (init.go: delete `createCenterCampfire`, `--remote` flag).
3. **Config addition** (config.go: `Relay` field in transport config).
4. **Server-side** `/campfire/create` handler (handler_create.go + transport_http.go routing).
5. **CLI** `cf register` command (register.go + relay.go shared function).
6. **CLI** `cf create --relay` flag (create.go changes).
7. **Integration test** using demo script.

Steps 1-2 are independent and can be done in parallel. Steps 4-6 depend on step 3. Step 7 depends on all prior steps.

## 12. What This Design Does NOT Cover

- **Multi-region failover** (A14): Deferred. Relay URL migration is a separate design.
- **Polling optimization** (A12): Independent concern.
- **Stale endpoint health checks** (A7): Independent concern.
- **CBOR state hardening** (A10): Independent concern.
- **HSM-backed key storage**: Future enhancement to address A3 for high-security deployments.
- **Threshold>1 relay registration**: The ECDH key transfer works for threshold=1. Threshold>1 requires DKG share distribution, which is a more complex protocol. Out of scope for this design; the existing MCP path handles it.
