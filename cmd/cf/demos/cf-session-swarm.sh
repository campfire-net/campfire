#!/usr/bin/env bash
# cf-session-swarm.sh — demonstrates the swarm-coordination convention port
# to cf-session 0.30 (cfs2_ token format, lazy-mint per-worker identity).
#
# Demonstrates (campfireagent-d77, cf 0.30+):
#   1. cf session create → emits cfs2_ token (not cfs1_; no shared private key)
#   2. cfs2_ token decodes to campfire ID for cf session send/read/end
#   3. session:open (cf-session convention) emitted on the session campfire
#   4. swarm-coordination ops (claim-item, complete, report-finding) work via
#      the session campfire using the creator's own identity key
#   5. session:close emitted on cf session end
#   6. Legacy cfs1_ tokens are rejected with a clear migration error
#
# No relay required — all messages flow through the shared filesystem transport.
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks fail
#
# Run from the campfire repo root:
#   bash cmd/cf/demos/cf-session-swarm.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GO="${GOROOT:-/usr/local/go}/bin/go"

# Always build a fresh binary from the repo source to ensure we test the
# current code, not an older installed version.
TMPBIN=$(mktemp -d)
"$GO" build -o "$TMPBIN/cf" "$REPO_ROOT/cmd/cf" 2>/dev/null || \
"$GO" build -o "$TMPBIN/cf" ./cmd/cf
CF="$TMPBIN/cf"

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

DEMO_TMP=$(mktemp -d)
trap 'rm -rf "$DEMO_TMP" "$TMPBIN"' EXIT

CF_HOME="$DEMO_TMP/identity"
mkdir -p "$CF_HOME"
export CF_HOME

echo "=== cf-session-swarm demo (campfireagent-d77, cf 0.30+) ==="
echo "CF_HOME: $CF_HOME"
echo ""

# ---------------------------------------------------------------------------
# 1. Init orchestrator identity
# ---------------------------------------------------------------------------
echo "--- 1. Init orchestrator identity ---"
INIT_OUT=$("$CF" --cf-home "$CF_HOME" init 2>&1)
echo "$INIT_OUT" | head -3
ok "orchestrator identity initialised"

# ---------------------------------------------------------------------------
# 2. cf session create → cfs2_ token (not cfs1_)
# ---------------------------------------------------------------------------
echo ""
echo "--- 2. cf session create → cfs2_ token ---"
TOKEN=$("$CF" --cf-home "$CF_HOME" session create --ttl 1h 2>/dev/null)
echo "Token: ${TOKEN:0:35}..."

if echo "$TOKEN" | grep -q "^cfs2_"; then
    ok "session create emits cfs2_ token (no shared private key)"
else
    fail "session create did NOT emit cfs2_ token (got: ${TOKEN:0:20})"
fi

if echo "$TOKEN" | grep -q "^cfs1_"; then
    fail "session create emitted legacy cfs1_ token (shared key anti-feature not removed)"
fi

# ---------------------------------------------------------------------------
# 3. cfs2_ token resolves to campfire address for send/read
# ---------------------------------------------------------------------------
echo ""
echo "--- 3. session:open — post via cfs2_ token ---"
SEND_OUT=$("$CF" --cf-home "$CF_HOME" session send "$TOKEN" \
    '{"op":"session:open","convention":"swarm-coordination","session_id":"demo-01"}' 2>/dev/null)
echo "Sent: $SEND_OUT"
if echo "$SEND_OUT" | grep -qE "sent:|^[a-f0-9]{8}"; then
    ok "session send accepts cfs2_ token (token resolves to campfire ID)"
else
    fail "session send with cfs2_ token returned unexpected output: $SEND_OUT"
fi

# ---------------------------------------------------------------------------
# 4. Swarm coordination ops via cfs2_ token
# ---------------------------------------------------------------------------
echo ""
echo "--- 4. swarm-coordination ops via cfs2_ token ---"

# claim-item
CLAIM_OUT=$("$CF" --cf-home "$CF_HOME" session send "$TOKEN" \
    '{"op":"claim-item","item_id":"demo-d77","description":"swarm-coordination port demo","instance":"orchestrator"}' \
    2>/dev/null)
echo "claim-item: $CLAIM_OUT"
if echo "$CLAIM_OUT" | grep -qE "sent:|^[a-f0-9]{8}"; then
    ok "claim-item posted via cfs2_ session"
else
    fail "claim-item failed: $CLAIM_OUT"
fi

# report-finding
FINDING_OUT=$("$CF" --cf-home "$CF_HOME" session send "$TOKEN" \
    '{"op":"report-finding","severity":"low","category":"demo","description":"cfs2_ token format verified"}' \
    2>/dev/null)
echo "report-finding: $FINDING_OUT"
if echo "$FINDING_OUT" | grep -qE "sent:|^[a-f0-9]{8}"; then
    ok "report-finding posted via cfs2_ session"
else
    fail "report-finding failed: $FINDING_OUT"
fi

# complete
COMPLETE_OUT=$("$CF" --cf-home "$CF_HOME" session send "$TOKEN" \
    '{"op":"complete","item_id":"demo-d77","summary":"cfs2_ lazy-mint integration verified","branch":"work/campfireagent-d77"}' \
    2>/dev/null)
echo "complete: $COMPLETE_OUT"
if echo "$COMPLETE_OUT" | grep -qE "sent:|^[a-f0-9]{8}"; then
    ok "complete posted via cfs2_ session"
else
    fail "complete failed: $COMPLETE_OUT"
fi

# ---------------------------------------------------------------------------
# 5. cf session read returns all posted messages
# ---------------------------------------------------------------------------
echo ""
echo "--- 5. cf session read --- (confirm all ops recorded)"
READ_OUT=$("$CF" --cf-home "$CF_HOME" session read "$TOKEN" 2>/dev/null)
echo "Messages: $(echo "$READ_OUT" | wc -l) lines"

if echo "$READ_OUT" | grep -q "claim-item"; then
    ok "session read returns claim-item message"
else
    fail "session read did not return claim-item message"
fi

if echo "$READ_OUT" | grep -q "complete"; then
    ok "session read returns complete message"
else
    fail "session read did not return complete message"
fi

# ---------------------------------------------------------------------------
# 6. session:close
# ---------------------------------------------------------------------------
echo ""
echo "--- 6. session:close (cf session end) ---"
END_OUT=$("$CF" --cf-home "$CF_HOME" session end "$TOKEN" 2>/dev/null)
echo "End: $END_OUT"
if echo "$END_OUT" | grep -qE "session ended|ended:"; then
    ok "session:close accepted cfs2_ token"
else
    fail "session end returned unexpected output: $END_OUT"
fi

# ---------------------------------------------------------------------------
# 7. Legacy cfs1_ token rejected with migration error
# ---------------------------------------------------------------------------
echo ""
echo "--- 7. Legacy cfs1_ token rejected ---"
# Construct a syntactically plausible fake cfs1_ token (wrong prefix).
FAKE_V1="cfs1_dGVzdA"  # cfs1_ + base64("test")
V1_ERR=$("$CF" --cf-home "$CF_HOME" session send "$FAKE_V1" "should not send" 2>&1 || true)
echo "cfs1_ error: ${V1_ERR:0:100}"

if echo "$V1_ERR" | grep -q "cfs1_"; then
    ok "cfs1_ token rejected with migration error mentioning cfs1_"
else
    fail "cfs1_ token was not rejected with a clear migration message (got: $V1_ERR)"
fi

# ---------------------------------------------------------------------------
# 8. TokenPrefix constant is cfs2_
# ---------------------------------------------------------------------------
echo ""
echo "--- 8. Verify TokenPrefix = cfs2_ via Go test ---"
GO_OUT=$(cd "$REPO_ROOT" && "$GO" test -run TestTokenPrefixConstants github.com/campfire-net/campfire/pkg/session 2>&1 || true)
if echo "$GO_OUT" | grep -qE "^ok|PASS"; then
    ok "TestTokenPrefixConstants: TokenPrefix = cfs2_"
else
    fail "TestTokenPrefixConstants failed: $GO_OUT"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "==================================================="
echo "cf-session-swarm demo summary (campfireagent-d77)"
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "==================================================="

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
exit 0
