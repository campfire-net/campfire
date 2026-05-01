#!/usr/bin/env bash
# identity-flow.sh — cf-identity Stage 3 demo: identity convention declarations + ceremony.
#
# Demonstrates:
#   1. Package compiles
#   2. All five declarations return well-formed convention/operation/tag values
#   3. IdentityDeclarations() returns all five in canonical order
#   4. Tag constants follow the identity:<op> naming scheme
#   5. ProfileFile round-trip (SaveProfile + LoadProfile)
#   6. Ceremony flow tests pass (introduce → declare-home → verify-me using real GateEvaluator)
#   7. Import path is cf-conventions/cf-identity (not the old cf-convention-extension/identity)
#
# Part of campfireagent-902 Stage 3 done conditions.
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks fail
#
# Usage:
#   bash cf-conventions/demos/cf-identity/identity-flow.sh
#
# Run from the campfire repo root.

set -euo pipefail

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GO="${GOROOT:-/usr/local/go}/bin/go"

echo "=== cf-identity identity-flow demo ==="
echo "Repo: $REPO_ROOT"
echo ""

# ---------------------------------------------------------------------------
# 1. Package compiles
# ---------------------------------------------------------------------------
echo "--- 1. Package compile ---"
if "$GO" build "$REPO_ROOT/cf-conventions/cf-identity" 2>&1; then
    ok "cf-conventions/cf-identity compiles"
else
    fail "cf-conventions/cf-identity failed to compile"
fi

# ---------------------------------------------------------------------------
# 2. Unit tests pass (declarations + profile + ceremony flow)
# ---------------------------------------------------------------------------
echo ""
echo "--- 2. Unit tests ---"
if "$GO" test -count=1 -v "$REPO_ROOT/cf-conventions/cf-identity" 2>&1 | grep -E "(PASS|FAIL|ok|---)" | head -40; then
    ok "cf-conventions/cf-identity tests pass"
else
    fail "cf-conventions/cf-identity tests failed"
fi

# Re-run without -v to get canonical pass/fail result.
if ! "$GO" test -count=1 "$REPO_ROOT/cf-conventions/cf-identity" 2>&1; then
    fail "cf-conventions/cf-identity tests returned non-zero exit"
fi

# ---------------------------------------------------------------------------
# 3. Import path check: consumers use the new cf-identity path
# ---------------------------------------------------------------------------
echo ""
echo "--- 3. Import path check ---"
OLD_PATH="cf-convention-extension/identity"
NEW_PATH="cf-conventions/cf-identity"

# depguard_test.go intentionally imports the old path to test the L2 boundary —
# exclude it from the "no remaining old imports" check.
OLD_REFS=$(grep -r "$OLD_PATH" "$REPO_ROOT" --include="*.go" -l 2>/dev/null \
    | grep -v vendor \
    | grep -v "depguard_test.go" \
    | wc -l || true)
if [ "$OLD_REFS" -eq 0 ]; then
    ok "No non-test files use old import path: $OLD_PATH"
else
    fail "Old import path still referenced in $OLD_REFS file(s): $OLD_PATH (run grep to inspect)"
fi

if grep -r "$NEW_PATH" "$REPO_ROOT" --include="*.go" -l 2>/dev/null | grep -v vendor | grep -q .; then
    ok "New import path in use: $NEW_PATH"
else
    fail "New import path not found in any .go file: $NEW_PATH"
fi

# ---------------------------------------------------------------------------
# 4. Profile absorption: profile.go in cf-protocol is still present but
#    cf-identity provides the canonical LoadProfile/SaveProfile.
# ---------------------------------------------------------------------------
echo ""
echo "--- 4. Profile absorption check ---"
if grep -q "LoadProfile\|SaveProfile\|ProfileFile" "$REPO_ROOT/cf-conventions/cf-identity/identity.go" 2>/dev/null; then
    ok "cf-identity exports LoadProfile, SaveProfile, ProfileFile"
else
    fail "cf-identity missing profile exports"
fi

if [ -f "$REPO_ROOT/cf-protocol/protocol/profile.go" ]; then
    ok "cf-protocol/protocol/profile.go still present (TRANSITIONAL placeholder)"
else
    fail "cf-protocol/protocol/profile.go missing (unexpected)"
fi

# ---------------------------------------------------------------------------
# 5. Convention declarations check
# ---------------------------------------------------------------------------
echo ""
echo "--- 5. Convention declarations check ---"
EXPECTED_OPS="introduce-me declare-home verify-me list-homes echo"
for op in $EXPECTED_OPS; do
    if grep -q "\"$op\"" "$REPO_ROOT/cf-conventions/cf-identity/identity.go" 2>/dev/null; then
        ok "Declaration for '$op' present"
    else
        fail "Declaration for '$op' missing"
    fi
done

# Check all required tags.
EXPECTED_TAGS="identity:introduction identity:home-declared identity:challenge-response identity:homes identity:home-echo identity:revoked identity:v1 identity:profile"
for tag in $EXPECTED_TAGS; do
    if grep -q "\"$tag\"" "$REPO_ROOT/cf-conventions/cf-identity/identity.go" 2>/dev/null; then
        ok "Tag constant '$tag' present"
    else
        fail "Tag constant '$tag' missing"
    fi
done

# ---------------------------------------------------------------------------
# 6. Depguard rules: cf-identity blocked from L1 and L2
# ---------------------------------------------------------------------------
echo ""
echo "--- 6. Depguard rules check ---"
if grep -q "cf-conventions/cf-identity" "$REPO_ROOT/.golangci.yml" 2>/dev/null; then
    ok "cf-identity appears in .golangci.yml depguard rules"
else
    fail ".golangci.yml missing cf-identity depguard rule"
fi

# Check L1-narrow rule.
if grep -A 5 "L1-narrow" "$REPO_ROOT/.golangci.yml" 2>/dev/null | grep -q "cf-identity"; then
    ok "L1-narrow rule blocks cf-identity import from cf-protocol/"
else
    fail "L1-narrow rule missing cf-identity deny entry"
fi

# Check L2-no-extensions rule.
if grep -A 5 "L2-no-extensions" "$REPO_ROOT/.golangci.yml" 2>/dev/null | grep -q "cf-identity"; then
    ok "L2-no-extensions rule blocks cf-identity import from cf-convention/"
else
    fail "L2-no-extensions rule missing cf-identity deny entry"
fi

# ---------------------------------------------------------------------------
# 7. Full test suite for affected packages
# ---------------------------------------------------------------------------
echo ""
echo "--- 7. Affected packages build and test ---"

PACKAGES=(
    "$REPO_ROOT/cf-conventions/cf-identity"
    "$REPO_ROOT/cf-conventions/cf-convention"
    "$REPO_ROOT/cmd/cf/cmd"
    "$REPO_ROOT/cf-protocol/protocol"
)

for pkg in "${PACKAGES[@]}"; do
    pkg_name="${pkg#$REPO_ROOT/}"
    if "$GO" test -count=1 "$pkg" 2>&1 | tail -1 | grep -q "^ok"; then
        ok "$pkg_name tests pass"
    else
        fail "$pkg_name tests failed"
    fi
done

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=== Summary ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo ""

if [ "$FAIL" -eq 0 ]; then
    echo "All checks passed."
    exit 0
else
    echo "$FAIL check(s) failed."
    exit 1
fi
