#!/usr/bin/env bash
# Demo 28 — Reserved-Op Floor: L2 DENY(reserved_op_floor) Behavioral Tests (campfireagent-cb3)
#
# Verifies the Stage 1 L2 enforcer behavioral contract:
#   — A reserved op dispatched WITHOUT a grant chain returns DENY(reserved_op_floor).
#   — A reserved op dispatched WITH a grant chain (empty, wrong op, expired) STILL
#     returns DENY(reserved_op_floor) — L2 fires before L3 is invoked.
#   — DispatchWithCancel enforces the same floor and calls the cancel func on deny.
#   — ErrReservedOp carries the wire-frozen "reserved_op_floor" prefix (underscores).
#
# Adversarial cases (spec §DONE condition 3):
#   A. No grant chain                  — baseline DENY
#   B. Empty grant chain []            — still DENY (not a valid owner grant)
#   C. Chain for different op          — still DENY (scope mismatch; floor fires first)
#   D. Expired chain entries           — still DENY (expiry check is L3's job; L2 fires first)
#   E. DispatchWithCancel path         — same floor, cancel func called
#
# Real dispatcher, no mock interceptor. Exit 0 when all suites pass.
#
# Run from the campfire repo root:
#   bash test/demo/28-reserved-op-floor-behavioral.sh

set -euo pipefail

export PATH=/usr/local/go/bin:$PATH

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
fail=0
pass=0

run_test_suite() {
  local label="$1"
  local pkg="$2"
  local run_filter="$3"
  shift 3

  echo ""
  echo "--- $label"
  if go test "$pkg" -run "$run_filter" -count=1 -timeout 120s "$@" 2>&1 | tee /tmp/demo28-go-output.txt; then
    echo "PASS: $label"
    ((pass++)) || true
  else
    echo "FAIL: $label"
    ((fail++)) || true
  fi
}

cd "$REPO_ROOT"

echo "=========================================="
echo "Demo 28 — L2 DENY(reserved_op_floor) Behavioral Tests"
echo "=========================================="

# --- Suite A: Behavioral floor tests (campfireagent-cb3)
run_test_suite \
  "L2 behavioral floor: no chain (baseline DENY)" \
  "./pkg/convention/..." \
  "TestReservedOpFloor_NoChain_ReturnsBaseDeny"

run_test_suite \
  "L2 behavioral floor: all 10 ops, no chain" \
  "./pkg/convention/..." \
  "TestReservedOpFloor_NoChain_AllTenOps"

run_test_suite \
  "L2 behavioral floor: empty chain still DENY" \
  "./pkg/convention/..." \
  "TestReservedOpFloor_EmptyChain_StillDenied"

run_test_suite \
  "L2 behavioral floor: chain for wrong op still DENY" \
  "./pkg/convention/..." \
  "TestReservedOpFloor_ChainForDifferentOp_StillDenied"

run_test_suite \
  "L2 behavioral floor: expired chain still DENY" \
  "./pkg/convention/..." \
  "TestReservedOpFloor_ExpiredChain_StillDenied"

run_test_suite \
  "L2 behavioral floor: DispatchWithCancel DENY + cancel called" \
  "./pkg/convention/..." \
  "TestReservedOpFloor_DispatchWithCancel_Denied"

run_test_suite \
  "L2 behavioral floor: DispatchWithCancel + chain still DENY" \
  "./pkg/convention/..." \
  "TestReservedOpFloor_DispatchWithCancel_WithChain_Denied"

run_test_suite \
  "DenyReason string stability: wire-frozen 'reserved_op_floor' prefix" \
  "./pkg/convention/..." \
  "TestReservedOpFloorDenyReasonString"

# --- Suite B: Regression — original enforcement tests (campfireagent-935) still green
run_test_suite \
  "Regression: registration-time enforcement (Tier1/Tier2)" \
  "./pkg/convention/..." \
  "TestReservedOpBlockedAtRegistration_Tier1|TestReservedOpBlockedAtRegistration_Tier2"

run_test_suite \
  "Regression: dispatch-time enforcement (original)" \
  "./pkg/convention/..." \
  "TestReservedOpBlockedAtDispatch|TestAllTenReservedOpsBlocked|TestNonReservedOpRegistrationSucceeds"

echo ""
echo "=========================================="
echo "Results: $pass passed, $fail failed"
echo "=========================================="

if [[ $fail -ne 0 ]]; then
  exit 1
fi
exit 0
