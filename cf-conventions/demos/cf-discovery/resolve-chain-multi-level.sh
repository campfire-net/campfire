#!/usr/bin/env bash
# cf-conventions/demos/cf-discovery/resolve-chain-multi-level.sh
#
# Demo: ResolveChain multi-level chain walks — campfireagent-750
#
# Proves:
#   1. cf-discovery/naming package compiles
#   2. 3-hop resolution (cf://org.team.service) resolves to the correct leaf campfire ID
#   3. Intermediate campfires (org, team) are auto-joined during the walk
#   4. Each hop in isolation resolves to the correct campfire ID
#   5. 4-hop resolution (cf://org.team.squad.service) generalises the chain walk
#   6. All three intermediate campfires are auto-joined in the 4-hop walk
#
# Chain topology:
#   root → org (level 1) → team (level 2) → service (level 3, leaf)
#   root → org → team → squad → service  [4-hop variant]
#
# Uses real protocol.Client instances with filesystem transport. No mocks.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/cf-discovery/resolve-chain-multi-level.sh
#
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GO="${GOROOT:-/usr/local/go}/bin/go"

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

echo "=== cf-discovery demo: resolve-chain-multi-level.sh ==="
echo "Repo: $REPO_ROOT"
echo ""

# ── Case 1: Package compiles ─────────────────────────────────────────────────
echo "--- 1. Package compile ---"
if "$GO" build "$REPO_ROOT/cf-conventions/cf-discovery/naming" 2>&1; then
    ok "cf-discovery/naming compiles"
else
    fail "cf-discovery/naming failed to compile"
fi
echo ""

# ── Case 2-4: 3-hop resolution (TestResolveChain_MultiLevel_Tracked) ─────────
echo "--- 2-4. 3-hop chain walk (root → org → team → service) ---"
echo "    Chain: cf://org.team.service"
echo "    Resolves hop-by-hop: org, org.team, org.team.service"
echo "    Auto-joins org and team during the walk"
echo ""

RESULT=$(
    "$GO" test \
        -count=1 \
        -run "TestResolveChain_MultiLevel_Tracked" \
        -v \
        "$REPO_ROOT/cf-conventions/cf-discovery/naming" 2>&1
)

if echo "$RESULT" | grep -q "^--- PASS: TestResolveChain_MultiLevel_Tracked"; then
    ok "3-hop resolve: PASS (TestResolveChain_MultiLevel_Tracked)"
else
    fail "3-hop resolve: FAIL (TestResolveChain_MultiLevel_Tracked)"
    echo "Output:"
    echo "$RESULT" | tail -20
fi

# Extract and display the key assertions from test output.
for line in \
    "Hop 1: cf://org" \
    "Hop 2: cf://org.team" \
    "Hop 3: cf://org.team.service" \
    "Auto-join: B is now a member of org" \
    "Auto-join: B is now a member of team"
do
    if echo "$RESULT" | grep -q "$line"; then
        ok "  $line"
    else
        fail "  $line (assertion not found in output)"
    fi
done
echo ""

# ── Case 5-6: 4-hop resolution (TestResolveChain_FourLevel) ──────────────────
echo "--- 5-6. 4-hop chain walk (root → org → team → squad → service) ---"
echo "    Chain: cf://org.team.squad.service"
echo "    Generalises beyond 3 hops"
echo ""

RESULT4=$(
    "$GO" test \
        -count=1 \
        -run "TestResolveChain_FourLevel" \
        -v \
        "$REPO_ROOT/cf-conventions/cf-discovery/naming" 2>&1
)

if echo "$RESULT4" | grep -q "^--- PASS: TestResolveChain_FourLevel"; then
    ok "4-hop resolve: PASS (TestResolveChain_FourLevel)"
else
    fail "4-hop resolve: FAIL (TestResolveChain_FourLevel)"
    echo "Output:"
    echo "$RESULT4" | tail -20
fi

# Extract key assertions.
for line in \
    "4-level resolve: cf://org.team.squad.service" \
    "Auto-join org" \
    "Auto-join team" \
    "Auto-join squad"
do
    if echo "$RESULT4" | grep -q "$line"; then
        ok "  $line"
    else
        fail "  $line (assertion not found in output)"
    fi
done
echo ""

# ── Full naming suite regression check ───────────────────────────────────────
echo "--- Full naming package regression check ---"
if "$GO" test -count=1 "$REPO_ROOT/cf-conventions/cf-discovery/naming" 2>&1 | tail -1 | grep -q "^ok"; then
    ok "Full naming package tests pass (no regressions)"
else
    fail "Full naming package tests failed"
fi
echo ""

# ── Summary ───────────────────────────────────────────────────────────────────
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
