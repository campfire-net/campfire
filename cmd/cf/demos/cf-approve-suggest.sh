#!/usr/bin/env bash
# REQUIRES_FIX: cf approve command not yet implemented in HEAD; demo written ahead of OPEN-021 feature. See campfireagent-3f3-approve-suggest.
# cf-approve-suggest.sh — demonstrates cf approve scope auto-suggestion (OPEN-021)
#
# Shows the complete flow:
#  1. Operator initializes an identity campfire.
#  2. An agent (or the convention executor) posts a delegation:request future
#     to the identity campfire, carrying a failed predicate and requested scope.
#  3. Operator runs cf approve --accept (non-interactive, accepts suggestion).
#  4. Grant is posted to the identity campfire, future is fulfilled.
#
# Design v2 §2.6: "cf approve includes scope auto-suggestion: the prompt is
# pre-filled with scope inferred from the failed predicate. Default --persist 7d."
#
# OPEN-021: scope auto-suggestion narrows the requested scope (rd:*) to the
# minimum required by the failed predicate (rd:claim), plus similar-op allowance.
#
# This script exits 0 when the demo runs end-to-end successfully.
set -euo pipefail

CF="${CF:-cf}"
DEMO_TMP=$(mktemp -d)
trap 'rm -rf "$DEMO_TMP"' EXIT

echo "=== cf-approve-suggest.sh ==="
echo "Demonstrates cf approve scope auto-suggestion (OPEN-021)"

# --- Setup: create operator identity campfire ---
echo ""
echo "--- Setup: operator identity ---"
OP_HOME="$DEMO_TMP/operator"
mkdir -p "$OP_HOME"

# Initialize operator identity (creates identity campfire, sets 'home' alias).
CF_HOME="$OP_HOME" "$CF" init --json > "$DEMO_TMP/init.json" 2>/dev/null
IDENTITY_CF=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/init.json'))['identity_campfire_id'])")
OP_PUBKEY=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/init.json'))['public_key'])")
echo "Identity campfire: ${IDENTITY_CF:0:12}..."
echo "Operator pubkey  : ${OP_PUBKEY:0:12}..."

# --- Step 1: Simulate a delegation:request from an agent ---
echo ""
echo "--- Step 1: agent posts delegation:request (simulating gate denial) ---"
echo "  Scenario: agent tried rd:claim but has no grant covering that operation."
echo "  Agent requested scope: rd:* (too wide)"
echo "  Failed predicate: grant:rd:claim"

# Build the delegation:request payload.
# In production the convention executor synthesizes this when a gate denies.
# Here we simulate it directly via cf send.
python3 - "$OP_PUBKEY" "$IDENTITY_CF" > "$DEMO_TMP/req.json" <<'EOF'
import json, sys
pubkey, cfid = sys.argv[1], sys.argv[2]
print(json.dumps({
    "agent_pubkey": pubkey,
    "future_id": "",
    "failed_predicate": {"kind": "grant", "convention": "rd", "op": "claim"},
    "requested_scope": "rd:*",
    "campfire_id": cfid
}))
EOF

# Post the delegation:request message as a future.
CF_HOME="$OP_HOME" "$CF" send "$IDENTITY_CF" \
    --tag delegation:request \
    --future \
    --json \
    "$(cat "$DEMO_TMP/req.json")" > "$DEMO_TMP/send.json" 2>/dev/null

MSG_ID=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/send.json'))['id'])")
echo "delegation:request posted: msg=${MSG_ID:0:12}..."

# --- Step 2: operator runs cf approve --accept (non-interactive) ---
echo ""
echo "--- Step 2: operator approves with scope auto-suggestion ---"
echo "  cf approve will suggest rd:claim (from predicate) + rd:* (same convention, from request)"
echo "  The suggestion is pre-filled; --accept accepts it without prompting."

CF_HOME="$OP_HOME" "$CF" approve "$MSG_ID" --accept --json > "$DEMO_TMP/approve.json" 2>&1
echo "Approval output:"
cat "$DEMO_TMP/approve.json"

# --- Step 3: verify the approval result ---
echo ""
echo "--- Step 3: verify approval ---"

PERSIST_TTL=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/approve.json'))['persist_ttl'])")
APPROVED_SCOPE=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/approve.json'))['approved_scope'])")

# Verify the approved scope includes rd:claim (required by predicate).
if python3 -c "import json; s=json.load(open('$DEMO_TMP/approve.json'))['approved_scope']; assert any('rd:claim' in x for x in s), f'rd:claim not in {s}'" 2>/dev/null; then
    echo "PASS: approved scope includes rd:claim (predicate-required entry)"
else
    echo "FAIL: expected rd:claim in approved scope, got: $APPROVED_SCOPE"
    exit 1
fi

# Verify default persist TTL = 7d.
if [ "$PERSIST_TTL" = "7d" ]; then
    echo "PASS: default persist TTL = 7d"
else
    echo "FAIL: expected persist_ttl = '7d', got: $PERSIST_TTL"
    exit 1
fi

# Verify the approved_scope_msg_id is set.
GRANT_MSG_ID=$(python3 -c "import json; d=json.load(open('$DEMO_TMP/approve.json')); print(d.get('msg_id',''))" 2>/dev/null)
if [ -n "$GRANT_MSG_ID" ]; then
    echo "PASS: grant message ID present: ${GRANT_MSG_ID:0:12}..."
else
    echo "FAIL: expected msg_id in approve output"
    exit 1
fi

# --- Step 4: verify --persist flag works ---
echo ""
echo "--- Step 4: verify --persist TTL override ---"

# Post another request.
CF_HOME="$OP_HOME" "$CF" send "$IDENTITY_CF" \
    --tag delegation:request \
    --future \
    --json \
    "$(cat "$DEMO_TMP/req.json")" > "$DEMO_TMP/send2.json" 2>/dev/null
MSG_ID2=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/send2.json'))['id'])")

CF_HOME="$OP_HOME" "$CF" approve "$MSG_ID2" --accept --persist 1d --json > "$DEMO_TMP/approve2.json" 2>&1
PERSIST_TTL2=$(python3 -c "import json; print(json.load(open('$DEMO_TMP/approve2.json'))['persist_ttl'])")
if [ "$PERSIST_TTL2" = "1d" ]; then
    echo "PASS: --persist 1d overrides default 7d TTL"
else
    echo "FAIL: expected persist_ttl = '1d', got: $PERSIST_TTL2"
    exit 1
fi

echo ""
echo "=== cf-approve-suggest.sh PASSED ==="
