#!/usr/bin/env bash
# reserved-op-enforcer.sh — Demo: D5 extended reserved-op enforcement (campfireagent-c85)
#
# Demonstrates two rejection cases added by D5:
#   1. Convention declaration sets min_operator_level: 0 on a reserved op → Parse rejects (D5b)
#   2. Reserved op GrantChain depth > 1 → Dispatch rejects (D5a)
#
# Architecture (reserved-ops-chain.md):
#   L1 — cf-protocol     LIST — IsReservedOp(op)
#   L2 — cf-conventions  ENFORCER — fires BEFORE GateEvaluator
#                          D5a: GrantChain depth > 1 → DENY (depth-1 cap)
#                          D5b: min_operator_level: 0 on reserved op → parse error
#   L3 — cf-authority    EVALUATOR — future (campfireagent-02a)
#
# Exit codes:
#   0 — both rejection cases exhibited as expected
#   1 — a rejection case did not fire
#
# Usage:
#   bash cf-conventions/demos/reserved-op-enforcer.sh
#
# Run from the repo root.

set -euo pipefail
REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

# run_test captures go test output and greps for the PASS line.
# Using output capture avoids pipefail interaction with grep's exit code.
run_test() {
  local test_name="$1"
  local out
  out=$(/usr/local/go/bin/go test ./cf-conventions/cf-convention/... \
    -run "$test_name" -v -count=1 2>&1) || true
  if echo "$out" | grep -q "PASS.*${test_name}"; then
    return 0
  else
    return 1
  fi
}

echo ""
echo "=== reserved-op enforcer D5 demo (campfireagent-c85) ==="
echo ""

# ---------------------------------------------------------------------------
# Case 1: D5b — Parse-time rejection: reserved op with min_operator_level: 0
# ---------------------------------------------------------------------------
echo "--- D5b: Parse-time rejection — reserved op with min_operator_level: 0 ---"
echo ""
echo "  Scenario: convention 'bad-convention' declares 'disband' with level 0."
echo "  Spec: reserved ops require owner-level authority. Level 0 = anonymous."
echo "  Expected: Parse returns error containing 'reserved' or 'reserved_op_floor'."
echo ""

if run_test "TestD5_ParseRejectsReservedOpWithLevelZeroExplicit_Disband"; then
  ok "D5b: Parse rejects 'disband' with min_operator_level:0 (reserved-op floor)"
else
  fail "D5b: Parse did NOT reject 'disband' with min_operator_level:0"
fi

echo ""

# ---------------------------------------------------------------------------
# Case 2: D5a — Dispatch-time rejection: reserved op with depth-2 GrantChain
# ---------------------------------------------------------------------------
echo "--- D5a: Dispatch-time rejection — reserved op with depth-2 GrantChain ---"
echo ""
echo "  Scenario: caller dispatches 'delegation-grant' with 2 GrantChain entries."
echo "  Spec: reserved ops are capped at delegation depth 1 (reserved-ops-chain.md §L2)."
echo "  Expected: Dispatch returns false (DENY), handler not invoked."
echo ""

if run_test "TestD5_DispatchRejectsReservedOpWithDepth2Chain"; then
  ok "D5a: Dispatch rejects 'delegation-grant' with depth-2 GrantChain (depth-1 cap)"
else
  fail "D5a: Dispatch did NOT reject 'delegation-grant' with depth-2 GrantChain"
fi

echo ""

# ---------------------------------------------------------------------------
# Case 3: Happy path — non-reserved op, any depth, any level → no enforcement
# ---------------------------------------------------------------------------
echo "--- Happy path: non-reserved op — no D5 enforcement fires ---"
echo ""
echo "  Scenario: 'swarm-coordination:claim-item' with depth-3 chain dispatches normally."
echo "  Expected: handler IS called; D5 enforcement does not fire for non-reserved ops."
echo ""

if run_test "TestD5_HappyPath_NonReservedOpAnyDepthAnyLevel"; then
  ok "Happy path: non-reserved op with depth-3 chain dispatches normally (D5 enforcement does not fire)"
else
  fail "Happy path: unexpected rejection of non-reserved op"
fi

echo ""

# ---------------------------------------------------------------------------
# Case 4: Adversarial — "looks like depth-1 but actually depth-2"
# ---------------------------------------------------------------------------
echo "--- Adversarial: chain appears depth-1 but has 2 hops — enforcer catches it ---"
echo ""
echo "  Scenario: GrantChain has 2 entries; outer entry claims 'nested_depth=1'."
echo "  Spec: enforcer counts len(GrantChain), not inner hop claims."
echo "  Expected: Dispatch returns false (DENY); encoding tricks don't fool the enforcer."
echo ""

if run_test "TestD5_Adversarial_ImplicitDepth2_CaughtByEnforcer"; then
  ok "Adversarial: reserved op with apparent-depth-1 but 2-entry chain is caught by enforcer"
else
  fail "Adversarial: implicit depth-2 chain was NOT caught"
fi

echo ""

# ---------------------------------------------------------------------------
# Exception: InfrastructureConvention ("convention-extension") is exempt
# ---------------------------------------------------------------------------
echo "--- Infrastructure convention exemption (convention-extension:revoke) ---"
echo ""
echo "  Scenario: 'convention-extension' declares 'revoke' at level 0."
echo "  Spec: infrastructure convention is exempt — it IS the convention lifecycle manager."
echo "  Expected: Parse succeeds (no error)."
echo ""

if run_test "TestD5_ParseInfrastructureConventionExempt"; then
  ok "Infrastructure exception: convention-extension may declare 'revoke' at level 0"
else
  fail "Infrastructure exception: convention-extension 'revoke' was incorrectly rejected"
fi

echo ""
echo "================================================================"
TOTAL=$((PASS + FAIL))
echo "Results: $PASS/$TOTAL checks passed"
if [[ $FAIL -gt 0 ]]; then
  echo "FAILED: $FAIL check(s) failed"
  exit 1
else
  echo "ALL CHECKS PASSED — reserved-op enforcer D5 demo complete"
  exit 0
fi
