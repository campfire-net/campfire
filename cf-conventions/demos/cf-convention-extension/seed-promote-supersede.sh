#!/usr/bin/env bash
# cf-conventions/demos/cf-convention-extension/seed-promote-supersede.sh
#
# Demo: cf-convention-extension package — seed declarations + promote/supersede ops.
# (campfireagent-a40)
#
# Proves:
#   1. The package builds cleanly at the new singular path (cf-convention-extension/).
#   2. Seed declarations (promote, supersede, revoke, naming-register) are accessible
#      from the L3 seed sub-package.
#   3. The behavioral promote and supersede validators accept valid inputs.
#   4. The promote validator rejects a payload with a supersedes field.
#   5. The supersede validator rejects a version downgrade.
#   6. Path reconciliation: cf-convention-extension/ (singular) is the canonical path
#      per design v2 §4.3; cf-convention-extensions/ (plural) no longer exists.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/cf-convention-extension/seed-promote-supersede.sh
#
# Requires: go on PATH.
# The script exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GO="${GO:-}"

# Find go binary.
if [ -z "$GO" ]; then
    if command -v go &>/dev/null; then
        GO=go
    elif [ -x "/usr/local/go/bin/go" ]; then
        GO=/usr/local/go/bin/go
    else
        echo "ERROR: go not found on PATH and not at /usr/local/go/bin/go" >&2
        exit 1
    fi
fi

cd "$REPO_ROOT"

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; exit 1; }

echo "=== cf-convention-extension: seed-promote-supersede demo ==="
echo "Repo root: $REPO_ROOT"
echo ""

# 1. Package builds cleanly.
echo "--- Check 1: cf-convention-extension/ package builds ---"
if $GO build ./cf-conventions/cf-convention-extension/ 2>&1; then
    pass "cf-convention-extension package builds"
else
    fail "cf-convention-extension package failed to build"
fi

# 2. Seed sub-package builds cleanly.
echo ""
echo "--- Check 2: cf-convention-extension/seed/ sub-package builds ---"
if $GO build ./cf-conventions/cf-convention-extension/seed/ 2>&1; then
    pass "cf-convention-extension/seed sub-package builds"
else
    fail "cf-convention-extension/seed sub-package failed to build"
fi

# 3. All tests in the cf-convention-extension package (including behavioral tests).
echo ""
echo "--- Check 3: promote/supersede behavioral tests pass ---"
if $GO test ./cf-conventions/cf-convention-extension/ -v 2>&1; then
    pass "behavioral promote/supersede tests pass"
else
    fail "behavioral promote/supersede tests failed"
fi

# 4. Seed declaration tests pass.
echo ""
echo "--- Check 4: seed declaration tests pass ---"
if $GO test ./cf-conventions/cf-convention-extension/seed/ -v 2>&1; then
    pass "seed declaration tests pass"
else
    fail "seed declaration tests failed"
fi

# 5. Path reconciliation: singular path must exist, plural must not.
echo ""
echo "--- Check 5: path reconciliation (singular vs plural) ---"
SINGULAR_PATH="$REPO_ROOT/cf-conventions/cf-convention-extension"
PLURAL_PATH="$REPO_ROOT/cf-conventions/cf-convention-extensions"

if [ -d "$SINGULAR_PATH" ]; then
    pass "cf-convention-extension/ (singular) exists"
else
    fail "cf-convention-extension/ (singular) does not exist — path reconciliation failed"
fi

if [ -d "$PLURAL_PATH" ]; then
    fail "cf-convention-extensions/ (plural) still exists — path reconciliation incomplete"
else
    pass "cf-convention-extensions/ (plural) does not exist (correctly removed)"
fi

# 6. No remaining imports of the old plural path.
echo ""
echo "--- Check 6: no remaining imports of old plural path ---"
OLD_IMPORT="cf-convention-extensions"
# Exclude comment lines (lines where the match appears after // or #) to avoid
# false positives from historical comments documenting the rename.
VIOLATIONS=$(grep -rn "$OLD_IMPORT" \
    --include="*.go" --include="*.yml" --include="*.yaml" \
    "$REPO_ROOT" 2>/dev/null | grep -v "^Binary" | grep -v "//.*$OLD_IMPORT" | grep -v "#.*$OLD_IMPORT" || true)

if [ -z "$VIOLATIONS" ]; then
    pass "no remaining imports of cf-convention-extensions (plural)"
else
    echo "FAIL: remaining references to cf-convention-extensions (plural):"
    echo "$VIOLATIONS"
    exit 1
fi

# 7. OPEN-018 regression test still passes.
echo ""
echo "--- Check 7: OPEN-018 regression (seed in L3, not L2) ---"
if $GO test ./cf-conventions/cf-convention-extension/seed/ -run TestOPEN018 -v 2>&1; then
    pass "OPEN-018 regression: seed is in L3, L2 does not import it"
else
    fail "OPEN-018 regression failed"
fi

echo ""
echo "=== All checks passed ==="
echo ""
echo "Summary:"
echo "  - cf-convention-extension/ package: builds + behavioral promote/supersede tests pass"
echo "  - cf-convention-extension/seed/: builds + all declaration tests pass"
echo "  - Path reconciled: singular (cf-convention-extension/) confirmed; plural removed"
echo "  - OPEN-018 closure confirmed: seed is L3, L2 is clean"
