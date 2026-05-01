#!/usr/bin/env bash
# chain-walk.sh — cf-authority Stage 3 demo: delegation chain walking.
#
# Demonstrates:
#   1. Valid 1-hop chain: root → sender → Allow
#   2. Valid 2-hop chain: root → intermediate → sender → Allow
#   3. Depth exceeded: 3-hop chain → Deny/depth_exceeded (D10)
#   4. Scope narrowing (valid): parent grants *, child grants :claim → Allow (D4)
#   5. Scope widening (rejected): parent grants :claim, child claims * → Deny/scope_widening (D4)
#
# Part of the cf-authority 1.0 Stage 3 done conditions (campfireagent-8d4 §DONE item 4).
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks fail
#
# Usage:
#   bash cf-conventions/demos/cf-authority/chain-walk.sh
#
# Run from the campfire repo root.

set -euo pipefail

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GOROOT:-/usr/local/go}/bin/go"

echo ""
echo "=== cf-authority Stage 3: chain-walk demo ==="
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# §A — Anchor-self (empty chain, sender == root)
# ─────────────────────────────────────────────────────────────────────────────
echo "=== §A — Anchor-self (case 1) ==="

if "$GO" test -run "TestCase01_AnchorSelf" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "PASS\|ok"; then
  ok "anchor-self (empty chain, sender==root) → Allow"
else
  fail "anchor-self test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §B — Valid 1-hop chain
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §B — Valid 1-hop chain (case 2) ==="

if "$GO" test -run "TestCase02_Valid1Hop" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "PASS\|ok"; then
  ok "valid 1-hop chain (root→sender) → Allow"
else
  fail "1-hop chain test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §C — Valid 2-hop chain
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §C — Valid 2-hop chain (case 3) ==="

if "$GO" test -run "TestCase03_Valid2Hop" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "PASS\|ok"; then
  ok "valid 2-hop chain (root→inter→sender) → Allow"
else
  fail "2-hop chain test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §D — Depth exceeded (D10)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §D — Depth exceeded / 3-hop chain (case 6, D10) ==="

if "$GO" test -run "TestCase06_DepthExceeded" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "PASS\|ok"; then
  ok "3-hop chain → Deny/depth_exceeded (D10)"
else
  fail "depth-exceeded test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §E — Scope narrowing (valid — child is ⊆ parent)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §E — Scope narrowing (case 7, D4 correct intersection) ==="

if "$GO" test -run "TestCase07_ScopeNarrowing" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "PASS\|ok"; then
  ok "scope narrowing (parent:*, child:claim) → Allow (valid)"
else
  fail "scope-narrowing test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §F — Scope widening rejected (D4, Attack 3, Attack 5)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §F — Scope widening rejected (case 8, D4, Attack 3+5) ==="

if "$GO" test -run "TestCase08_ScopeWideningRejected" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "PASS\|ok"; then
  ok "scope widening (parent:claim, child:*) → Deny/scope_widening (D4)"
else
  fail "scope-widening-rejected test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §G — Determinism 3× re-run (D1)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §G — Determinism 3× re-run (D1) ==="

if "$GO" test -count=3 -run "TestCase01_AnchorSelf|TestCase02_Valid1Hop|TestCase03_Valid2Hop|TestCase06_DepthExceeded" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "ok"; then
  ok "3× re-run of chain-walk cases produces identical results (D1)"
else
  fail "determinism 3× re-run failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Summary
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== SUMMARY ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"
echo ""

if [[ $FAIL -eq 0 ]]; then
  echo "RESULT: all checks passed. chain-walk demo complete."
  exit 0
else
  echo "RESULT: $FAIL check(s) failed."
  exit 1
fi
