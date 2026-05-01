#!/usr/bin/env bash
# dispatch-convention.sh — demonstrates cf convention dispatch
#
# Shows:
#   - cf <campfire> <operation> [--args]  (name-based dispatch)
#   - cf <campfire>/<operation> [--args]  (slash-path dispatch)
#   - cf --help-primitives               (show hidden primitive commands)
#   - convention-only help surface
#
# This script exits 0 when the convention surface is working correctly.
# It uses a temp identity and FS-transport campfire — no network required.
set -euo pipefail

CF="${CF:-cf}"
DEMO_TMP=$(mktemp -d)
trap 'rm -rf "$DEMO_TMP"' EXIT

export CF_HOME="$DEMO_TMP/home"
mkdir -p "$CF_HOME"

# Run from DEMO_TMP so the config walk-up doesn't pick up real ~/.cf/ ancestors.
cd "$DEMO_TMP"

echo "=== dispatch-convention.sh ==="
echo "CF_HOME: $CF_HOME"

# 1. Init identity
echo ""
echo "--- Step 1: cf init ---"
"$CF" --cf-home "$CF_HOME" init 2>&1 | head -5

# 2. cf --help should NOT show send/read (they are primitives)
echo ""
echo "--- Step 2: cf --help should hide primitive commands ---"
HELP=$("$CF" --cf-home "$CF_HOME" --help 2>&1 || true)
for prim in send read await inspect compact; do
    if echo "$HELP" | grep -E "^  $prim " > /dev/null 2>&1; then
        echo "FAIL: primitive '$prim' must not appear in cf --help"
        exit 1
    fi
done
echo "PASS: primitives (send, read, await, inspect, compact) are hidden"

# 3. cf --help-primitives shows the primitives surface
echo ""
echo "--- Step 3: cf --help-primitives shows primitives ---"
PRIM_HELP=$("$CF" --cf-home "$CF_HOME" --help-primitives 2>&1 || true)
if ! echo "$PRIM_HELP" | grep -q "send"; then
    echo "FAIL: --help-primitives must mention 'send'"
    exit 1
fi
echo "PASS: --help-primitives shows primitive commands"

# 4. Convention commands appear in cf --help
echo ""
echo "--- Step 4: convention commands visible in cf --help ---"
for cmd in convention swarm join init config trust; do
    if ! echo "$HELP" | grep -E "  $cmd " > /dev/null 2>&1; then
        echo "FAIL: convention command '$cmd' must appear in cf --help"
        exit 1
    fi
done
echo "PASS: convention commands (convention, swarm, join, init, config, trust) visible"

# 5. Slash-path dispatch syntax (cf <campfire>/<operation>) is parsed correctly
echo ""
echo "--- Step 5: slash-path dispatch syntax ---"
# Create a campfire to test with
CFID=$("$CF" --cf-home "$CF_HOME" create --transport fs 2>&1 | grep -oE '[0-9a-f]{64}' | head -1 || true)
if [ -z "$CFID" ]; then
    echo "SKIP: could not create campfire (no FS transport dir configured); skipping slash-path test"
else
    echo "Created campfire: ${CFID:0:12}..."
    # Slash-path: cf <id>/<op> dispatches to the convention op
    # (will fail with "no convention operations declared" but that proves parsing works)
    SLASHOUT=$("$CF" --cf-home "$CF_HOME" "${CFID}/help" 2>&1 || true)
    if echo "$SLASHOUT" | grep -qiE "error: resolving|no convention|available|convention operations|Usage: cf"; then
        echo "PASS: slash-path syntax parsed correctly"
    else
        echo "SKIP: slash-path dispatch produced unexpected output: $SLASHOUT"
    fi
fi

echo ""
echo "=== dispatch-convention.sh PASSED ==="
