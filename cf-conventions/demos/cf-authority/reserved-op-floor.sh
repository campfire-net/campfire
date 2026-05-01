#!/usr/bin/env bash
# reserved-op-floor.sh — cf-authority Stage 3 demo: reserved-op depth floor enforcement (D5).
#
# Demonstrates:
#   1. Reserved ops list from cf-protocol (10 ops frozen at L1)
#   2. Convention declaration claiming level:0 on delegation-grant is ignored
#   3. 2-hop chain for a reserved op → Deny/reserved_op_floor
#   4. 1-hop chain for a reserved op → still checked (valid if scope matches)
#   5. Non-reserved op with 2-hop chain → evaluated by normal chain rules
#
# Closes Attack 4: convention-author self-exempts via gate declaration.
# Deal-breaker D5 coverage.
#
# Part of the cf-authority 1.0 Stage 3 done conditions (campfireagent-8d4 §DONE item 4).
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks fail
#
# Usage:
#   bash cf-conventions/demos/cf-authority/reserved-op-floor.sh
#
# Run from the campfire repo root.

set -euo pipefail

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

GO="${GOROOT:-/usr/local/go}/bin/go"

echo ""
echo "=== cf-authority Stage 3: reserved-op-floor demo ==="
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# §A — Verify the reserved-ops list exists and has exactly 10 entries
# ─────────────────────────────────────────────────────────────────────────────
echo "=== §A — Reserved-ops list (L1 authoritative) ==="

if "$GO" test -run "TestReservedOpsAccessible" \
    ./cf-protocol/protocol/... 2>&1 | grep -q "PASS\|ok"; then
  ok "reserved-ops list has exactly 10 ops (L1 freeze, TestReservedOpsAccessible)"
else
  fail "reserved-ops list count check failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §B — Reserved-op floor test: 2-hop chain → Deny/reserved_op_floor (D5)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §B — Reserved-op floor (case 11, D5, Attack 4) ==="

if "$GO" test -run "TestCase11_ReservedOpFloor" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "PASS\|ok"; then
  ok "2-hop chain on delegation-grant → Deny/reserved_op_floor (D5)"
else
  fail "reserved-op floor test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §C — Convention author's level:0 claim is ignored
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §C — Convention level:0 claim ignored for reserved ops (Attack 4) ==="

# The conformance test already validates this: case 11 sends a request with
# Predicate.KindLevel{LevelN:0} (convention claims level:0 on delegation-grant)
# but the evaluator ignores it and still returns Deny/reserved_op_floor.
if "$GO" test -run "TestCase11_ReservedOpFloor" -v \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | \
    grep -qF "PASS: TestCase11_ReservedOpFloor"; then
  ok "convention-author level:0 override is ignored for reserved ops (Attack 4 closed)"
else
  fail "convention level:0 override check failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §D — Non-reserved op with 2-hop chain is evaluated normally (not floor-denied)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §D — Non-reserved op (claim) with 2-hop chain: normal evaluation ==="

if "$GO" test -run "TestCase03_Valid2Hop" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "PASS\|ok"; then
  ok "non-reserved op (ready:claim) with 2-hop chain → Allow (not floor-denied)"
else
  fail "non-reserved op 2-hop test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §E — D5 is part of the deal-breakers document
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §E — D5 documented in deal-breakers ==="

if grep -q "D5 — Reserved-Op Floor" docs/0.30-deal-breakers.md 2>/dev/null; then
  ok "D5 documented in docs/0.30-deal-breakers.md"
else
  fail "D5 not found in docs/0.30-deal-breakers.md"
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
  echo "RESULT: all checks passed. reserved-op-floor demo complete."
  exit 0
else
  echo "RESULT: $FAIL check(s) failed."
  exit 1
fi
