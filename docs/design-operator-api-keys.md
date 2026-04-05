# Design: Operator API Keys for mcp.getcampfire.dev

**Status:** Draft
**Date:** 2026-04-03
**Authors:** Architect (adversarial design synthesis)
**Reviewers:** Adversary, Creative, Systems Pragmatist, Domain Purist

## 1. Problem Statement

Mallcop-pro (and future operator-tier consumers) needs programmatic access to mcp.getcampfire.dev to:

1. Create campfires per customer during registration
2. Declare conventions in each campfire
3. Generate join credentials for daemon containers
4. Execute convention operations from webhook handlers

Today, every `campfire_init` call creates a new ephemeral Ed25519 identity and a disposable session. There is no way to associate multiple sessions with a single billing entity, no way to pre-authenticate, and no way to revoke all sessions for a compromised credential.

## 2. Key Design Decision: Forge Keys as API Keys

**Decision: Use Forge API keys directly. Do not build a `cfapikeys` table.**

Rationale:

- `forge.Client.ResolveKey()` already exists (`pkg/forge/keys.go:82`) and returns a `KeyRecord` with `AccountID`, `Role`, `RPMLimit`, `MonthlyBudget`, `Revoked` fields (`pkg/forge/types.go:34-45`).
- `hosting.SignupService.CreateOperator()` already chains `CreateAccount` + `CreateKey` (`pkg/hosting/signup.go:36-55`).
- `hosting.ForgeIdentityResolver` already resolves and caches Forge keys with a 5-minute TTL (`pkg/hosting/identity.go:42-108`).
- Building a `cfapikeys` table creates a second source of truth that will drift from Forge's revocation and rate-limit state.

The Forge key prefix is `forge-tk-*`. Bearer tokens presented to cf-mcp that match this prefix are dispatched to the Forge validation path. Existing session tokens (32-byte hex, 64 characters) are dispatched to the session token validation path.

## 3. Architectural Invariants (Domain Purist Conditions)

These are non-negotiable. Violation of any invariant is a blocking defect.

**INV-1: API key never enters the protocol layer.** The Forge API key MUST NOT appear in any message, beacon, provenance record, membership record, or any structure defined in `pkg/protocol/`. The API key is a hosting-layer credential that resolves to an `OperatorIdentity`; from that point forward, only the `AccountID` flows through the system.

**INV-2: API key path is documented as hosting convenience.** The Ed25519 Signed auth scheme (already stubbed at `cmd/cf-mcp/main.go:4048-4053`) is the protocol-native authentication path. The API key path is a convenience for operators who cannot manage Ed25519 key material. Both paths converge at `OperatorIdentity` resolution.

**INV-3: Ephemeral identity implication is documented.** Sessions created via API key own campfires through ephemeral Ed25519 keys generated per-session, not through a persistent operator identity bound to the API key. Campfire ownership is tied to the session's ephemeral key. This means: if an API-key session is destroyed, the campfires it created are owned by an unreachable key. Operators MUST use `campfire_id` parameter (auto-provision path) or persistent agent names to maintain continuity across sessions.

## 4. Auth Dispatch: Insertion Point and Flow

### 4.1 Token Classification

The bearer token format determines the auth path:

| Prefix | Path | Validation |
|--------|------|------------|
| `forge-tk-` | Forge API key | `ForgeIdentityResolver.ResolveKey()` then issue session token |
| 64-char hex | Session token | `TokenRegistry.lookup()` (existing path) |
| `Signed ` | Ed25519 signed auth | Reserved; returns 401 today (`main.go:4048-4053`) |

### 4.2 Insertion Point (Adversary Attack #2 -- CRITICAL)

The Forge key detection MUST be inserted **before** `validateToken` at `main.go:4094`. The current code at lines 4044-4054 parses the `Authorization` header and extracts the bearer token. The new logic inserts between token extraction (line 4047) and the existing `isInit` check (line 4068).

**Concrete insertion: after line 4054, before line 4056.**

```go
// --- BEGIN: Forge API key detection ---
// If bearer token has forge-tk- prefix, resolve via Forge and issue a
// session token transparently. This runs BEFORE validateToken so that
// forge-tk-* tokens never reach the session registry (which would
// reject them as unknown).
if token != "" && strings.HasPrefix(token, "forge-tk-") {
    resolvedToken, resolveErr := s.resolveForgeKeyToSession(r.Context(), token)
    if resolveErr != nil {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusUnauthorized)
        json.NewEncoder(w).Encode(errResponse(req.ID, -32000,
            fmt.Sprintf("invalid API key: %v", resolveErr)))
        return
    }
    token = resolvedToken
    // Fall through to normal session validation with the issued token.
}
// --- END: Forge API key detection ---
```

### 4.3 Forge Key Resolution Flow

`resolveForgeKeyToSession` is a new method on `*server`:

```go
func (s *server) resolveForgeKeyToSession(ctx context.Context, forgeKey string) (string, error)
```

Steps:
1. Call `s.identityResolver.ResolveKey(ctx, forgeKey)` -- returns `OperatorIdentity` or error.
2. Check `KeyRecord.Revoked` -- if true, return error.
3. Look up existing session for this `AccountID` in the operator-session index (section 5.3).
4. If session exists and is valid, return its token.
5. If no session, issue a new token via `s.sessManager.issueOperatorToken()`, store the operator-session mapping, and return the token.

The `ForgeIdentityResolver` cache (5-minute TTL, `pkg/hosting/identity.go:65-70`) amortizes Forge round-trips. No additional cache layer is needed.

### 4.4 Fail-Closed Guarantee (Adversary Attack #5 -- High)

If `ForgeIdentityResolver.ResolveKey()` returns an error (Forge unreachable, transient Azure error, etc.), the request MUST fail with HTTP 401. The error path in `resolveForgeKeyToSession` returns an error, which the caller (section 4.2) converts to 401. There is no fallback to anonymous mode. The `token` variable is never set on the error path.

## 5. Data Structures and Interfaces

### 5.1 OperatorIdentity (existing, no changes)

```go
// pkg/hosting/identity.go:16-20
type OperatorIdentity struct {
    AccountID string
    Name      string
    Role      string
}
```

### 5.2 ForgeIdentityResolver (existing, no changes needed)

Already implemented at `pkg/hosting/identity.go:42-108`. The `ResolveKey` method resolves a Forge API key to an `OperatorIdentity` with a 5-minute in-memory cache.

### 5.3 Operator-Session Index (NEW)

**Purpose:** Map `AccountID` to active session token(s), enabling: (a) session reuse across requests with the same API key, and (b) bulk revocation when a key is compromised.

This addresses **Adversary Attack #8 (CRITICAL)**: without this index, there is no way to enumerate sessions by operator for revocation.

```go
// cmd/cf-mcp/session.go -- new type

// operatorSessionIndex maps Forge AccountIDs to their active session tokens.
// Thread-safe. Used to reuse sessions across API-key requests and to
// enumerate sessions for bulk revocation.
type operatorSessionIndex struct {
    mu       sync.RWMutex
    byAcct   map[string][]string // accountID -> []token
    byToken  map[string]string   // token -> accountID (reverse index)
}

func newOperatorSessionIndex() *operatorSessionIndex

// Associate records that token belongs to accountID.
func (idx *operatorSessionIndex) Associate(accountID, token string)

// TokensForAccount returns all active tokens for an account.
func (idx *operatorSessionIndex) TokensForAccount(accountID string) []string

// Remove deletes a token from the index (called on session close/revoke).
func (idx *operatorSessionIndex) Remove(token string)

// RevokeAccount revokes all tokens for an account and returns the count.
// Calls revokeFunc for each token.
func (idx *operatorSessionIndex) RevokeAccount(accountID string, revokeFunc func(token string)) int
```

**Storage:** In-memory only for v1. On process restart, API-key clients re-authenticate on next request (Forge key is re-resolved, new session issued).

### 5.4 SessionManager Extensions

Add to `SessionManager` struct (`cmd/cf-mcp/session.go:471`):

```go
type SessionManager struct {
    // ... existing fields ...

    // operatorIndex maps Forge account IDs to session tokens for API-key auth.
    operatorIndex *operatorSessionIndex

    // identityResolver resolves Forge API keys to OperatorIdentity.
    // Non-nil only in hosted HTTP mode with FORGE_SERVICE_KEY set.
    identityResolver hosting.IdentityResolver
}
```

New methods:

```go
// issueOperatorToken issues a session token bound to an operator account.
// If a valid session already exists for this account, returns the existing token.
// Concurrency-safe: uses singleflight keyed by accountID.
func (m *SessionManager) issueOperatorToken(ctx context.Context, accountID string) (string, error)

// revokeOperatorSessions revokes all sessions for an operator account.
// Returns the number of sessions revoked.
func (m *SessionManager) revokeOperatorSessions(accountID string) int
```

### 5.5 Token TTL for API-Key Sessions

API-key sessions use TTL=0 (no expiry) in the TokenRegistry. They are terminated only by:
- Explicit revocation (`campfire_revoke_session`)
- Operator account revocation (`revokeOperatorSessions`)
- Process restart (in-memory registry loss; client re-authenticates)
- Forge key revocation (detected on next `ResolveKey` call after cache TTL expires)

## 6. Operator Account Resolution (Adversary Attack #12 -- High)

**Problem:** `EnsureOperatorAccount` at `cmd/cf-mcp/operator_accounts.go:92` is keyed by session Ed25519 public key, not by Forge account ID. Two sessions from the same API key generate different ephemeral keys, creating separate `OperatorAccount` rows and duplicate signup credits.

**Fix:** When a session is created via API key, skip `EnsureOperatorAccount` entirely. The Forge account already exists (the API key resolved to it). Instead:

1. `resolveForgeKeyToSession` returns both the session token and the `OperatorIdentity.AccountID`.
2. The session object stores the `AccountID` directly (no DB lookup needed).
3. The metering hook uses this stored `AccountID` for usage events.
4. `EnsureOperatorAccount` remains unchanged for anonymous/ephemeral sessions.

## 7. Concurrency: Quota and Session Creation Races

### 7.1 Concurrent campfire_init Quota Bypass (Adversary Attack #3 -- High)

**Problem:** N concurrent API-key requests could all observe no existing session and all issue new tokens.

**Fix:** `issueOperatorToken` uses a `singleflight.Group` keyed by `accountID`:

```go
func (m *SessionManager) issueOperatorToken(ctx context.Context, accountID string) (string, error) {
    v, err, _ := m.operatorSF.Do(accountID, func() (interface{}, error) {
        // Check operatorIndex first
        if tokens := m.operatorIndex.TokensForAccount(accountID); len(tokens) > 0 {
            // Validate the first token is still live
            if _, err := m.validateToken(tokens[0]); err == nil {
                return tokens[0], nil
            }
            // Token expired/revoked -- clean up and fall through
            m.operatorIndex.Remove(tokens[0])
        }
        // Issue new token
        token, err := m.issueToken()
        if err != nil {
            return nil, err
        }
        m.operatorIndex.Associate(accountID, token)
        return token, nil
    })
    if err != nil {
        return "", err
    }
    return v.(string), nil
}
```

### 7.2 Per-Operator Session Limit

Each operator account is limited to **5 concurrent sessions** (configurable). The `operatorSessionIndex.Associate` method enforces this limit atomically under its write lock.

### 7.3 last_used Write Contention (Adversary Attack #6 -- Low)

Accepted. Last-writer-wins for `last_used` is correct behavior for a monotonically increasing timestamp.

## 8. CLI: Key Provisioning

New subcommand: `cf admin create-operator`

```
cf admin create-operator --name "Mallcop Pro" [--forge-url URL] [--service-key KEY]
```

Implementation (~80 LOC in `cmd/cf/cmd/admin_create_operator.go`):

1. Instantiate `hosting.SignupService` with a `forge.Client`.
2. Call `CreateOperator(ctx, name)` -- returns `OperatorIdentity` + plaintext key.
3. Print the key exactly once to stdout. Print usage instructions to stderr.

**Key delivery (Adversary Attack #11 -- High):**

```
WARNING: This key is shown once and cannot be retrieved again.
Store it in a secrets manager (e.g., Azure Key Vault, AWS Secrets Manager).
Do not pass it as a command-line argument in production -- use environment variables.
```

## 9. Metering

API-key sessions meter to their Forge account ID directly. Store `accountID` on the `Session` struct, wire it into the existing `rateLimiter.SetForgeAccount()` call synchronously during `resolveForgeKeyToSession` instead of the async goroutine at `main.go:1087-1100`.

## 10. Structured campfire_init Response

When a session is created via API key, the `campfire_init` response includes:

```json
{
  "session_token": "abcdef0123...",
  "operator_account_id": "forge-acct-xyz",
  "auth_method": "forge-api-key",
  "session_ttl": "infinite"
}
```

## 11. Security Disposition Matrix

| # | Attack | Severity | Disposition | Resolution |
|---|--------|----------|-------------|------------|
| 1 | Timing oracle on hash lookup | Medium | **Resolved** | Forge keys are opaque to cf-mcp; timing is Forge's concern. |
| 2 | Auth dispatch insertion point | CRITICAL | **Resolved** | Forge key detection inserted before `validateToken` (section 4.2). |
| 3 | Concurrent campfire_init quota bypass | High | **Resolved** | `singleflight` keyed by `accountID` (section 7.1). |
| 4 | Key sharing / sub-delegation | Medium | **Accepted risk** | Business model risk; Forge rate limits bound exposure. |
| 5 | Azure Table transient fail-open | High | **Resolved** | Fail-closed: Forge error = 401 (section 4.4). |
| 6 | last_used write contention | Low | **Accepted** | Last-writer-wins is correct (section 7.3). |
| 7 | Session tokens stored plaintext | High | **Pre-existing; out of scope** | Track separately as security hardening item. |
| 8 | No session-to-operator index | CRITICAL | **Resolved** | `operatorSessionIndex` with bidirectional mapping (section 5.3). |
| 9 | Per-operator quota race | High | **Resolved** | `singleflight` + atomic check in `operatorSessionIndex` (section 7.1, 7.2). |
| 10 | No API-key-to-Ed25519 binding | Medium | **Accepted risk; documented** | INV-2 documents this. Signed auth is P1 future. |
| 11 | CLI key delivery channel | High | **Mitigated** | Key to stdout, warning to stderr, env-var guidance (section 8). |
| 12 | EnsureOperatorAccount keyed by pubkey | High | **Resolved** | API-key sessions skip EnsureOperatorAccount; use resolved AccountID (section 6). |
| 13 | cfapikeys/cfoperators tables not built | Blocker | **Resolved by design** | Tables not needed; Forge is source of truth (section 2). |

## 12. Implementation Phases

### Phase 1: CLI + Operator Session Index (~160 LOC)

**Files:**
- `cmd/cf/cmd/admin_create_operator.go` (new, ~80 LOC)
- `cmd/cf-mcp/session.go` (~80 LOC) -- `operatorSessionIndex` type + methods

**Done condition:** `cf admin create-operator --name "Test"` returns a valid Forge key. `operatorSessionIndex` has passing unit tests for Associate, TokensForAccount, Remove, RevokeAccount, and concurrent access.

### Phase 2: Bearer Detection + Session Issuance (~220 LOC)

**Files:**
- `cmd/cf-mcp/main.go` (~50 LOC) -- Forge key detection block, `resolveForgeKeyToSession`
- `cmd/cf-mcp/session.go` (~90 LOC) -- `issueOperatorToken` with singleflight, TTL=0 support
- `cmd/cf-mcp/operator_accounts.go` (~40 LOC) -- skip EnsureOperatorAccount for API-key sessions
- `cmd/cf-mcp/main.go` (~40 LOC) -- wire identityResolver into SessionManager

**Done condition:** `Authorization: Bearer forge-tk-*` on campfire_init returns valid session. Same key reuses session. Revoked key returns 401. Forge unreachable returns 401.

### Phase 3: Metering + Structured Response (~55 LOC)

**Files:**
- `cmd/cf-mcp/main.go` (~15 LOC) -- structured init response
- `cmd/cf-mcp/main.go` (~40 LOC) -- synchronous SetForgeAccount wiring

**Done condition:** Usage events appear in Forge with correct AccountID. Init response includes auth_method and operator_account_id.

### Total: ~435 LOC across 3 phases.

## 13. Future Work (Not In Scope)

**Ed25519 Signed Auth (P1):** The `Signed` scheme stub at `main.go:4048-4053` is the protocol-native path per INV-2. No API key, no Forge round-trip, no storage. Ship when a consumer needs it.

**Session token hashing at rest:** Adversary attack #7 is pre-existing. Hash tokens in `TokenRegistry` and `CampfireSessionTokens` using SHA-256 with a per-deployment salt.

**Persistent operator-session index:** If process restarts become problematic, persist to Azure Table Storage. For v1, re-authentication on restart is acceptable.
