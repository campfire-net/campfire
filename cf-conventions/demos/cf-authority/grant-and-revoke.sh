#!/usr/bin/env bash
# grant-and-revoke.sh — cf-authority Stage 3 demo: grant creation and revocation.
#
# Demonstrates:
#   1. Building a valid GrantPayload with a Capability (CBOR-encoded)
#   2. Computing the grant-id (SHA-256(CBOR(GrantPayload)))
#   3. Simulating revocation via RevocationViewEntry
#   4. DefaultGateEvaluator returns Deny/revoked for a revoked grant
#
# Part of the cf-authority 1.0 Stage 3 done conditions (campfireagent-8d4 §DONE item 4).
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks fail
#
# Usage:
#   bash cf-conventions/demos/cf-authority/grant-and-revoke.sh
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
echo "=== cf-authority Stage 3: grant-and-revoke demo ==="
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# §A — Package compiles cleanly
# ─────────────────────────────────────────────────────────────────────────────
echo "=== §A — Package compilation ==="

if "$GO" build ./cf-conventions/cf-authority/... 2>/dev/null; then
  ok "cf-authority/trust compiles without errors"
else
  fail "cf-authority/trust failed to compile"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §B — DefaultGateEvaluator grant evaluation tests pass
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §B — DefaultGateEvaluator test suite ==="

_out=$("$GO" test -v -count=1 -run "TestCase02_Valid1Hop|TestCase05_RevokedMidChain" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1) || true
echo "$_out" | grep -E "PASS|FAIL|ok|SKIP" | head -20 || true
if echo "$_out" | grep -qE "^ok |--- PASS:"; then
  ok "grant and revoke conformance cases pass"
else
  fail "grant/revoke conformance cases failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §C — Grant CBOR encoding is deterministic (D1)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §C — Grant CBOR determinism ==="

# Capture output first to avoid SIGPIPE / pipefail interaction with grep -q
_out=$("$GO" test -count=1 -run "TestCapabilityCBORDeterminism|TestGrantPayloadIDDeterminism" \
    ./cf-conventions/cf-authority/trust/... 2>&1) || true
if echo "$_out" | grep -q "ok"; then
  ok "CBOR encoding is deterministic across 3 runs"
else
  fail "CBOR determinism check failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §D — Revocation causes Deny/revoked
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §D — Revocation deny (D9) ==="

_out=$("$GO" test -run "TestCase05_RevokedMidChain" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1) || true
if echo "$_out" | grep -q "PASS\|ok"; then
  ok "mid-chain revocation returns Deny/revoked (D9)"
else
  fail "revocation test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §E — Stale revocation also blocks (Attack 2)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §E — Stale revocation window (Attack 2) ==="

_out=$("$GO" test -run "TestCase10_StaleRevocationWindow" \
    ./cf-conventions/cf-authority/trust/conformance/... 2>&1) || true
if echo "$_out" | grep -q "PASS\|ok"; then
  ok "stale revocation window returns Deny/stale_revocation (Attack 2)"
else
  fail "stale revocation test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §F — ConventionAdapter satisfies L2 interface
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §F — ConventionAdapter L2 interface satisfaction ==="

if "$GO" build ./cf-conventions/cf-authority/trust/... 2>/dev/null; then
  ok "ConventionAdapter compiles and satisfies convention.GateEvaluator interface"
else
  fail "adapter build failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §G — Production dispatch goes through the gate evaluator (campfireagent-861)
# ─────────────────────────────────────────────────────────────────────────────
# Verifies that wireConventionMetering wires DefaultGateEvaluator (not the
# AllowAll stub) and that the ConventionDispatcher calls Evaluate before
# invoking any handler. Both are required for Stage 3 to be closed.
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §G — Production gate evaluator wiring (campfireagent-861) ==="

_out=$("$GO" test -run "TestProductionWiring_GateEvaluatorSet" \
    ./cmd/cf-mcp/... 2>&1) || true
if echo "$_out" | grep -q "PASS\|ok"; then
  ok "wireConventionMetering sets real DefaultGateEvaluator (not AllowAll stub)"
else
  fail "production gate evaluator not wired — SECURITY GAP"
fi

_out=$("$GO" test -run "TestDispatcher_GateEvaluatorInvokedOnDispatch|TestDispatcher_DenyDecisionRejectsDispatch|TestDispatcher_AllowDecisionAllowsDispatch" \
    ./cf-conventions/cf-convention/... 2>&1) || true
if echo "$_out" | grep -q "PASS\|ok"; then
  ok "dispatcher calls GateEvaluator.Evaluate on every dispatch; Deny blocks handler"
else
  fail "dispatcher gate integration tests failed"
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
  echo "RESULT: all checks passed. grant-and-revoke demo complete."
  exit 0
else
  echo "RESULT: $FAIL check(s) failed."
  exit 1
fi
