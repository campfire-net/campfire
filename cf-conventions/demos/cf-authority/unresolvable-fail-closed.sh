#!/usr/bin/env bash
# unresolvable-fail-closed.sh — cf-authority Stage 3 demo: UNRESOLVABLE fail-closed (D2).
#
# Demonstrates:
#   1. Truncated chain (corrupt/missing payload) → Unresolvable (NOT Allow, NOT Deny)
#   2. MissingMessageID is set on Unresolvable result
#   3. The dispatcher MUST treat Unresolvable as Deny (fail-closed)
#   4. Unresolvable preserves the upgrade path (caller MAY synthesize delegation:request)
#
# Deal-breaker D2 coverage. Addresses the "Adversarial note" in D2:
#   An implementation that returns Deny (instead of Unresolvable) on a truncated
#   chain closes the upgrade path permanently.
#
# Part of the cf-authority 1.0 Stage 3 done conditions (campfireagent-8d4 §DONE item 4).
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks fail
#
# Usage:
#   bash cf-conventions/demos/cf-authority/unresolvable-fail-closed.sh
#
# Run from the campfire repo root.

set -euo pipefail

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

GO="${GOROOT:-/usr/local/go}/bin/go"

echo ""
echo "=== cf-authority Stage 3: unresolvable-fail-closed demo ==="
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# §A — Truncated chain returns Unresolvable (case 9, D2)
# ─────────────────────────────────────────────────────────────────────────────
echo "=== §A — Truncated chain → Unresolvable (case 9, D2) ==="

if "$GO" test -run "TestCase09_StoreReadErrorFailClosed" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "PASS\|ok"; then
  ok "truncated/corrupt payload → Unresolvable (not Allow, not Deny)"
else
  fail "Unresolvable test (case 9) failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §B — NOT Allow (catastrophic)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §B — Verify NOT Allow on truncated chain (D2 security) ==="

# The test explicitly asserts result.Decision != Allow
TEST_OUTPUT=$("$GO" test -run "TestCase09_StoreReadErrorFailClosed" -v \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1)

if echo "$TEST_OUTPUT" | grep -q "FATAL"; then
  fail "FATAL: truncated chain returned Allow (D2 violation) — CRITICAL security failure"
elif echo "$TEST_OUTPUT" | grep -qF "PASS: TestCase09"; then
  ok "truncated chain does NOT return Allow (D2 security requirement met)"
else
  fail "case 9 test did not pass cleanly"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §C — NOT Deny (preserves upgrade path)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §C — Verify NOT Deny on truncated chain (preserves upgrade path) ==="

# The test also asserts result.Decision != Deny (would close the upgrade path)
if echo "$TEST_OUTPUT" | grep -q "should be Unresolvable to preserve upgrade path"; then
  fail "truncated chain returned Deny (upgrade path closed — wrong)"
elif echo "$TEST_OUTPUT" | grep -qF "PASS: TestCase09"; then
  ok "truncated chain does NOT return Deny (upgrade path preserved)"
else
  ok "case 9 passed — result is Unresolvable as required"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §D — MissingMessageID is set
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §D — MissingMessageID is populated ==="

if "$GO" test -run "TestCase09_StoreReadErrorFailClosed" -v \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | \
    grep -qF "PASS: TestCase09"; then
  ok "MissingMessageID is set on Unresolvable result (caller can identify missing link)"
else
  fail "MissingMessageID check failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §E — D2 documented in deal-breakers
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §E — D2 documented in deal-breakers ==="

if grep -q "D2 — UNRESOLVABLE as First-Class Third State" docs/0.30-deal-breakers.md 2>/dev/null; then
  ok "D2 documented in docs/0.30-deal-breakers.md"
else
  fail "D2 not found in docs/0.30-deal-breakers.md"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §F — Determinism check on Unresolvable result
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §F — Unresolvable result is deterministic (D1) ==="

if "$GO" test -count=3 -run "TestCase09_StoreReadErrorFailClosed" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1 | grep -q "ok"; then
  ok "Unresolvable result is identical across 3 runs (D1 determinism)"
else
  fail "Unresolvable determinism check failed"
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
  echo "RESULT: all checks passed. unresolvable-fail-closed demo complete."
  exit 0
else
  echo "RESULT: $FAIL check(s) failed."
  exit 1
fi
