#!/usr/bin/env bash
# cf-trust-pins.sh — demonstrates cf trust pin/unpin/list/prune
#
# Exercises all four TOFU key pin management subcommands:
#   cf trust pin <campfire-id> <pubkey>  — pin a key for a campfire
#   cf trust unpin <campfire-id>         — remove a key pin
#   cf trust list                        — list all key pins
#   cf trust prune                       — prune pins for left/disbanded campfires
#
# The script exits 0 when all assertions pass.
set -euo pipefail

CF="${CF:-cf}"
DEMO_TMP=$(mktemp -d)
trap 'rm -rf "$DEMO_TMP"' EXIT

echo "=== cf-trust-pins.sh ==="

# Initialize a fresh CF_HOME with its own identity.
CF_HOME_A="$DEMO_TMP/agent-a"
mkdir -p "$CF_HOME_A"
CF_HOME="$CF_HOME_A" "$CF" init --quiet 2>/dev/null || CF_HOME="$CF_HOME_A" "$CF" init 2>/dev/null || true

# Generate two synthetic campfire IDs (64 hex chars each) and a pubkey.
CAMPFIRE_A=$(python3 -c "import secrets; print(secrets.token_hex(32))")
CAMPFIRE_B=$(python3 -c "import secrets; print(secrets.token_hex(32))")
PUBKEY_A=$(python3 -c "import secrets; print(secrets.token_hex(32))")
PUBKEY_B=$(python3 -c "import secrets; print(secrets.token_hex(32))")

echo ""
echo "--- 1. Pin a key for campfire A ---"
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust pin "$CAMPFIRE_A" "$PUBKEY_A" 2>&1)
echo "$OUT"
if echo "$OUT" | grep -q "pinned:"; then
    echo "PASS: pin reported success"
else
    echo "FAIL: expected 'pinned:' in output, got:"
    echo "$OUT"
    exit 1
fi

echo ""
echo "--- 2. List shows campfire A pin ---"
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust list 2>&1)
echo "$OUT"
SHORT_A="${CAMPFIRE_A:0:12}"
if echo "$OUT" | grep -q "$SHORT_A"; then
    echo "PASS: campfire A appears in list"
else
    echo "FAIL: expected campfire A short ID '$SHORT_A' in list output"
    exit 1
fi
if echo "$OUT" | grep -q "Key pins (1)"; then
    echo "PASS: pin count = 1"
else
    echo "FAIL: expected 'Key pins (1)' header"
    exit 1
fi

echo ""
echo "--- 3. Pin a second key for campfire B ---"
CF_HOME="$CF_HOME_A" "$CF" trust pin "$CAMPFIRE_B" "$PUBKEY_B" 2>/dev/null
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust list 2>&1)
if echo "$OUT" | grep -q "Key pins (2)"; then
    echo "PASS: pin count = 2 after second pin"
else
    echo "FAIL: expected 'Key pins (2)' after second pin, got:"
    echo "$OUT"
    exit 1
fi

echo ""
echo "--- 4. cf trust list --json returns valid JSON array ---"
JSON_OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust list --json 2>&1)
echo "$JSON_OUT"
COUNT=$(python3 -c "import json,sys; data=json.loads(sys.stdin.read()); print(len(data))" <<< "$JSON_OUT")
if [ "$COUNT" = "2" ]; then
    echo "PASS: JSON array has 2 entries"
else
    echo "FAIL: expected 2 entries in JSON array, got $COUNT"
    exit 1
fi

# Verify required fields in each JSON entry.
for field in campfire_id pubkey pinned_at; do
    FIELD_COUNT=$(python3 -c "
import json,sys
data=json.loads(sys.stdin.read())
count = sum(1 for e in data if '$field' in e)
print(count)
" <<< "$JSON_OUT")
    if [ "$FIELD_COUNT" = "2" ]; then
        echo "PASS: all JSON entries have field '$field'"
    else
        echo "FAIL: not all JSON entries have field '$field' (got $FIELD_COUNT/2)"
        exit 1
    fi
done

echo ""
echo "--- 5. cf trust unpin removes campfire A ---"
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust unpin "$CAMPFIRE_A" 2>&1)
echo "$OUT"
if echo "$OUT" | grep -q "unpinned:"; then
    echo "PASS: unpin reported success"
else
    echo "FAIL: expected 'unpinned:' in output"
    exit 1
fi

# Verify list now shows only 1 pin.
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust list 2>&1)
if echo "$OUT" | grep -q "Key pins (1)"; then
    echo "PASS: pin count = 1 after unpin"
else
    echo "FAIL: expected 'Key pins (1)' after unpin, got:"
    echo "$OUT"
    exit 1
fi

echo ""
echo "--- 6. cf trust unpin on non-existent pin reports clearly ---"
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust unpin "$CAMPFIRE_A" 2>&1)
echo "$OUT"
if echo "$OUT" | grep -q "no pin found"; then
    echo "PASS: unpin on missing pin reports clearly"
else
    echo "FAIL: expected 'no pin found' message, got: $OUT"
    exit 1
fi

echo ""
echo "--- 7. cf trust pin is idempotent (re-pin replaces) ---"
PUBKEY_A2=$(python3 -c "import secrets; print(secrets.token_hex(32))")
CF_HOME="$CF_HOME_A" "$CF" trust pin "$CAMPFIRE_A" "$PUBKEY_A" 2>/dev/null
CF_HOME="$CF_HOME_A" "$CF" trust pin "$CAMPFIRE_A" "$PUBKEY_A2" 2>/dev/null

JSON_OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust list --json 2>&1)
# Should still be 2 pins (A was re-added), and A's pubkey should be PUBKEY_A2.
COUNT=$(python3 -c "import json,sys; data=json.loads(sys.stdin.read()); print(len(data))" <<< "$JSON_OUT")
if [ "$COUNT" = "2" ]; then
    echo "PASS: still 2 pins after re-pin (idempotent)"
else
    echo "FAIL: expected 2 pins after re-pin, got $COUNT"
    exit 1
fi

FOUND_KEY=$(python3 -c "
import json, sys
data = json.loads(sys.stdin.read())
for e in data:
    if e['campfire_id'] == '$CAMPFIRE_A':
        print(e['pubkey'])
        break
" <<< "$JSON_OUT")
if [ "$FOUND_KEY" = "$PUBKEY_A2" ]; then
    echo "PASS: re-pin replaced the pubkey correctly"
else
    echo "FAIL: expected re-pinned pubkey '$PUBKEY_A2', got '$FOUND_KEY'"
    exit 1
fi

echo ""
echo "--- 8. cf trust prune removes stale pins (not in membership) ---"
# CAMPFIRE_A and CAMPFIRE_B are currently pinned.
# Neither is in the local store membership (we only ran `cf init`, not `cf join`).
# So prune should remove both.
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust prune 2>&1)
echo "$OUT"
if echo "$OUT" | grep -qE "Pruned [0-9]+ pin"; then
    echo "PASS: prune removed stale pins"
else
    echo "FAIL: expected 'Pruned N pin(s)' in prune output, got:"
    echo "$OUT"
    exit 1
fi

# After prune: list should be empty.
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust list 2>&1)
if echo "$OUT" | grep -q "No key pins"; then
    echo "PASS: list empty after prune"
else
    echo "FAIL: expected 'No key pins' after prune, got:"
    echo "$OUT"
    exit 1
fi

echo ""
echo "--- 9. cf trust prune --dry-run previews without removing ---"
# Re-pin one campfire to test dry-run.
CF_HOME="$CF_HOME_A" "$CF" trust pin "$CAMPFIRE_A" "$PUBKEY_A" 2>/dev/null
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust prune --dry-run 2>&1)
echo "$OUT"
if echo "$OUT" | grep -q "Would prune"; then
    echo "PASS: dry-run shows preview"
else
    echo "FAIL: expected 'Would prune' in dry-run output, got: $OUT"
    exit 1
fi

# Pin should still be there after dry-run.
OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust list 2>&1)
if echo "$OUT" | grep -q "Key pins (1)"; then
    echo "PASS: pin not removed after --dry-run"
else
    echo "FAIL: pin should still exist after dry-run, got:"
    echo "$OUT"
    exit 1
fi

echo ""
echo "--- 10. Invalid pubkey is rejected ---"
BAD_OUT=$(CF_HOME="$CF_HOME_A" "$CF" trust pin "$CAMPFIRE_A" "not-valid-hex" 2>&1 || true)
if echo "$BAD_OUT" | grep -q "invalid pubkey"; then
    echo "PASS: invalid pubkey rejected"
else
    echo "FAIL: expected 'invalid pubkey' error, got: $BAD_OUT"
    exit 1
fi

echo ""
echo "=== cf-trust-pins.sh PASSED ==="
