# Troubleshooting — cf 0.30

Common errors and their diagnostic sequences.

## "no identity file found" / identity.json missing

```
Error: no identity file found at ~/.cf/identity.json
```

**Cause:** `cf init` was not run, or `CF_HOME` points to a directory without an identity file.

**Fix:**
```bash
cf init --display-name "my-agent"   # creates ~/.cf/identity.json
cf id                               # verify the key is readable
```

If you are using a custom CF_HOME: `CF_HOME=/path/to/cf cf init`.

---

## "TOFU pin mismatch" on join

```
Error: TOFU pin mismatch: campfire <id> previously pinned to key <old-key>,
       now presents <new-key>
```

**Cause:** The campfire's key changed since you last joined (e.g., the campfire was rekeyed after an eviction), or you are joining a different campfire that reuses the same ID (should not happen with cryptographic IDs).

**Diagnostic:**
```bash
cf trust show --json | jq '.pins[] | select(.campfire_id == "<id>")'
```

**Fix:** If the rekey was expected (you evicted a member), reset the pin:
```bash
cf trust reset --campfire <id>
cf join <id>   # re-pins to the new key via TOFU
```

If the rekey was unexpected, do not reset — investigate whether the campfire was compromised.

---

## Convention op returns `DENY(reserved_op_floor)`

**Cause:** The operation name matches one of the ten reserved ops (`disband`, `evict`, `admit`, `grant`, `revoke`, `delegation-grant`, `delegation-revoke`, `delegation-accept`, `member-roster`, `compaction`). The L2 enforcer rejects these before the L3 evaluator is invoked.

**Fix:** Rename your convention operation. Reserved op names are not available to convention authors. See `docs/agent/convention-authoring.md` §Reserved-op floor.

---

## `cf convention lint` fails with "unknown predicate kind"

**Cause:** The declaration uses a predicate kind not in the gate language, or uses `"not:"` which is explicitly rejected.

**Fix:** Check the predicate `kind` field against the six allowed leaf kinds: `level`, `grant`, `grant_in`, `grant_quota`, `chain_to`, `chain_to_quorum`. See `docs/agent/gate-predicates.md`.

---

## `Unresolvable` from GateEvaluator

```
Decision: Unresolvable, MissingMessageID: <msg-id>
```

**Cause:** The delegation chain is incomplete in the local store. The evaluator cannot reach the trust anchor.

**Diagnostic sequence:**
1. Check the campfire's recent messages for `delegation:grant` tags
2. Verify your store has the grant with ID `<msg-id>`: `cf inspect <campfire-id> <msg-id>`
3. If the message is missing from your store, you may need to sync from the transport

**Fix:** Treat as Deny (fail-closed). Optionally synthesize a `delegation:request` future to request the missing chain hop from the campfire. See `docs/agent/failure-modes.md` §UNRESOLVABLE.

---

## `Deny(stale_revocation)` — revocation view too old

**Cause:** Your revocation view is stale beyond the owner's `MaxRevocationStaleness` policy. The evaluator cannot assert that grants are currently unrevoked.

**Fix:** Refresh the revocation view:
```bash
cf read <campfire-id> --tag identity:revoked
```
Then retry the operation after the view is refreshed in your local store.

---

## Session worker cannot post messages

**Cause:** The worker's grant has expired, or the session has ended.

**Diagnostic:**
```bash
cf session read <session-id>   # check for session:close event
```

If the session is still open, check the worker's grant expiry. Worker grants expire at most at the session's `until` timestamp.

---

## `cf session join` returns "session not found"

**Cause:** The session ID is wrong, the session has already ended, or the session campfire is on a transport the worker cannot reach.

**Diagnostic:**
```bash
cf ls   # is the session campfire in your known campfires?
cf read <session-id> --all   # can you read it at all?
```

---

## Post-join probe fails (`ErrPostJoinVerificationFailed`)

**Cause:** The campfire did not return the probe message within the 5-second timeout. This indicates the campfire may be silently dropping writes from new members.

**What to do:** Do not retry. The discoverer has already posted a `discovery:unjoin-declaration` and left. Report the campfire ID as a suspected honeypot. Do not join it again without out-of-band verification.

---

## `cf convention promote` fails with "transport not configured"

**Cause:** No registry campfire ID was provided, or the default transport is not configured.

**Fix:**
```bash
# Explicit registry
cf convention promote my-op.json --registry <campfire-id>

# Or set a default in config
cf config set behavior.convention_registry <campfire-id>
```

---

## "display name not showing up in cf read"

**Cause:** Display names are tainted — they are not cryptographically bound to the identity. They are published as an `identity:profile` message when joining a campfire (best-effort, non-blocking). The message may not have arrived yet, or the campfire does not display them.

Display names are never suitable for access-control decisions. Use the sender's public key hex for identity verification.

---

## General diagnostic sequence

For any unexpected behavior:

```bash
# 1. Check your identity
cf id

# 2. Check your config cascade
cf config list --show-origin

# 3. Check your trust state
cf trust show

# 4. Inspect the specific message
cf inspect <campfire-id> <message-id>

# 5. Read the campfire with no filter
cf read <campfire-id> --all

# 6. Check logs (hosted service)
# CF_HOME logs are at ~/.cf/campfire.log when CF_LOG=debug is set
CF_LOG=debug cf <campfire-id> <operation> ...
```

## See also

- `docs/agent/failure-modes.md` — deny reason codes and fail-closed semantics
- `docs/agent/gate-predicates.md` — predicate reference
- `docs/convention-sdk.md` — SDK Init, lifecycle, error types
