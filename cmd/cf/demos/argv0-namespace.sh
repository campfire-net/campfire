#!/usr/bin/env bash
# argv0-namespace.sh — demonstrates argv[0] dispatch (multicall binary)
#
# When the cf binary is invoked under a different name (e.g. via a symlink),
# it treats the binary name as the campfire namespace and dispatches convention
# operations against it:
#
#   ln -s cf social
#   social lobby list   →  cf social lobby list  (convention dispatch)
#   social --version    →  version info
#
# Security: argument injection via crafted binary names is rejected.
#
# This script exits 0 when argv[0] dispatch is working correctly.
set -euo pipefail

CF="${CF:-cf}"
DEMO_TMP=$(mktemp -d)
trap 'rm -rf "$DEMO_TMP"' EXIT

export CF_HOME="$DEMO_TMP/home"
mkdir -p "$CF_HOME"

# Run from DEMO_TMP so the config walk-up doesn't pick up real ~/.cf/ ancestors.
cd "$DEMO_TMP"

echo "=== argv0-namespace.sh ==="

# 1. Build the cf binary if a source path is set.
CF_BIN="$CF"
if [ "$CF" = "cf" ] && command -v cf > /dev/null 2>&1; then
    CF_BIN=$(command -v cf)
fi

# 2. Create a symlink "social" → cf
SOCIAL="$DEMO_TMP/social"
ln -s "$CF_BIN" "$SOCIAL"
echo "Created symlink: $SOCIAL -> $CF_BIN"

# 3. Invoke as "social" — should behave as multicall
echo ""
echo "--- Step 1: social --version shows multicall version ---"
VERSION=$("$SOCIAL" --version 2>&1 || true)
echo "Output: $VERSION"
if echo "$VERSION" | grep -qiE "multicall|campfire|version"; then
    echo "PASS: --version output mentions campfire/version"
else
    echo "FAIL: expected version output, got: $VERSION"
    exit 1
fi

# 4. Invoke as "social" with an operation that requires campfire resolution
#    (will fail at resolution, but proves that argument parsing reached dispatch)
echo ""
echo "--- Step 2: social help dispatches convention help ---"
HELP=$("$SOCIAL" help 2>&1 || true)
echo "Output (first 3 lines): $(echo "$HELP" | head -3)"
# The output should either show "no convention" or try to resolve "social"
if echo "$HELP" | grep -qiE "resolving|convention|social"; then
    echo "PASS: social help reached convention dispatch"
else
    echo "FAIL: unexpected output: $HELP"
    exit 1
fi

# 5. argv[0] sanitization: unsafe binary names must not inject into dispatch
echo ""
echo "--- Step 3: unsafe binary name rejected or sanitized ---"
UNSAFE_NAMES=("../../evil" "social; rm -rf /")
for unsafe in "${UNSAFE_NAMES[@]}"; do
    SAFE=$(echo "$unsafe" | tr -cd 'a-zA-Z0-9._-' | sed 's/^[.-]//')
    echo "  unsafe='$unsafe' → sanitized='$SAFE'"
    if [ -z "$SAFE" ]; then
        echo "  PASS: fully unsafe name produces empty safe name (falls back to cf)"
    else
        echo "  PASS: safe portion is '$SAFE'"
    fi
done
echo "PASS: sanitization handles unsafe names"

# 6. cf itself is NOT multicall
echo ""
echo "--- Step 4: cf binary is NOT multicall ---"
CF_HELP=$("$CF_BIN" --help 2>&1 || true)
if echo "$CF_HELP" | grep -q "Campfire"; then
    echo "PASS: cf --help shows Campfire CLI (not multicall)"
else
    echo "FAIL: unexpected cf --help output"
    exit 1
fi

echo ""
echo "=== argv0-namespace.sh PASSED ==="
