#!/usr/bin/env bash
# gate-evaluator-stage2.sh — Demo: GateEvaluator interface declared in L2 (campfireagent-2b9).
#
# Proves that:
#   1. cf-conventions/cf-convention/ compiles cleanly with GateEvaluator interface.
#   2. GateEvaluator interface signature matches design v2 §2.5.1 exactly.
#   3. AllowAllGateEvaluator stub returns GateAllow unconditionally (3× determinism check).
#   4. All ten stable DenyReason codes are present.
#   5. Decision constants are ordered and frozen at expected numeric values.
#   6. depguard L2-no-authority rule is present and fires on intentional L3 import.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/gate-evaluator-stage2.sh
#
# Requires: Go toolchain on PATH, golangci-lint (optional — depguard tests skip if absent).

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
cd "$REPO_ROOT"

PASS=0
FAIL=0

check() {
    local label="$1"
    shift
    local err_file
    err_file="$(mktemp)"
    local rc=0
    if "$@" >/dev/null 2>"$err_file"; then
        echo "  PASS: $label"
        PASS=$((PASS + 1))
    else
        rc=$?
        echo "  FAIL ($rc): $label"
        if [[ -s "$err_file" ]]; then
            echo "    --- stderr ---"
            sed 's/^/    /' "$err_file"
            echo "    --- end stderr ---"
        fi
        FAIL=$((FAIL + 1))
    fi
    rm -f "$err_file"
}

check_output() {
    local label="$1"
    local expected="$2"
    shift 2
    local err_file out
    err_file="$(mktemp)"
    out=$("$@" 2>"$err_file")
    if echo "$out" | grep -q "$expected"; then
        echo "  PASS: $label"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: $label (expected pattern: $expected)"
        echo "        actual output: $out"
        if [[ -s "$err_file" ]]; then
            echo "    --- stderr ---"
            sed 's/^/    /' "$err_file"
            echo "    --- end stderr ---"
        fi
        FAIL=$((FAIL + 1))
    fi
    rm -f "$err_file"
}

# fast_fail_check runs a check and exits immediately if it fails.
# Use for prerequisite steps where subsequent checks are meaningless on failure
# (e.g., compilation — all grep/test checks are irrelevant if the build is broken).
fast_fail_check() {
    local label="$1"
    shift
    local err_file
    err_file="$(mktemp)"
    local rc=0
    if "$@" >/dev/null 2>"$err_file"; then
        echo "  PASS: $label"
        PASS=$((PASS + 1))
    else
        rc=$?
        echo "  FAIL ($rc): $label"
        if [[ -s "$err_file" ]]; then
            echo "    --- stderr ---"
            sed 's/^/    /' "$err_file"
            echo "    --- end stderr ---"
        fi
        FAIL=$((FAIL + 1))
        rm -f "$err_file"
        echo ""
        echo "=== Results: $PASS passed, $FAIL failed ==="
        echo "Fatal: compilation failure — remaining checks skipped (they depend on a working build)."
        exit 1
    fi
    rm -f "$err_file"
}

echo "=== Stage 2: GateEvaluator interface (campfireagent-2b9) ==="
echo ""

# ── 1. Package compiles cleanly ───────────────────────────────────────────────
echo "── Compilation ──"
fast_fail_check "cf-conventions/cf-convention builds cleanly" \
    go build ./cf-conventions/cf-convention/...
check "gate_evaluator.go has GateEvaluator interface" \
    grep -q "type GateEvaluator interface" cf-conventions/cf-convention/gate_evaluator.go
check "gate_evaluator.go has AllowAllGateEvaluator stub" \
    grep -q "type AllowAllGateEvaluator struct" cf-conventions/cf-convention/gate_evaluator.go
check "gate_evaluator.go does not import cf-authority (only comments reference it)" \
    bash -c '! go list -f "{{.Imports}}" ./cf-conventions/cf-convention/ | grep -q "cf-authority"'

# ── 2. Interface signature matches design v2 §2.5.1 ──────────────────────────
echo ""
echo "── Interface signature (design v2 §2.5.1) ──"
check "Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResult signature present" \
    grep -q "Evaluate(ctx context.Context, req EvaluateRequest) EvaluateResult" \
    cf-conventions/cf-convention/gate_evaluator.go
check "EvaluateRequest struct declared" \
    grep -q "type EvaluateRequest struct" cf-conventions/cf-convention/gate_evaluator.go
check "EvaluateResult struct declared" \
    grep -q "type EvaluateResult struct" cf-conventions/cf-convention/gate_evaluator.go
check "Decision type declared" \
    grep -q "type Decision uint8" cf-conventions/cf-convention/gate_evaluator.go
check "DenyReason type declared" \
    grep -q "type DenyReason string" cf-conventions/cf-convention/gate_evaluator.go

# ── 3. Stable DenyReason codes (frozen at cf-authority 1.0) ──────────────────
echo ""
echo "── DenyReason stable codes ──"
for code in expired revoked depth_exceeded scope_mismatch scope_widening \
    stale_revocation reserved_op_floor owner_ceiling predicate_unsatisfied store_read_error; do
    check "DenyReason code '$code' present" \
        grep -q "\"$code\"" cf-conventions/cf-convention/gate_evaluator.go
done

# ── 4. Run targeted unit tests ────────────────────────────────────────────────
echo ""
echo "── Unit tests ──"
check "TestAllowAllGateEvaluator_ReturnsAllow" \
    go test -run TestAllowAllGateEvaluator_ReturnsAllow ./cf-conventions/cf-convention/...
check "TestAllowAllGateEvaluator_AllowIsDeterministic (3× determinism)" \
    go test -run TestAllowAllGateEvaluator_AllowIsDeterministic ./cf-conventions/cf-convention/...
check "TestDecisionConstants_Ordered" \
    go test -run TestDecisionConstants_Ordered ./cf-conventions/cf-convention/...
check "TestDecisionConstants_Values (frozen: Allow=1, Deny=2, Unresolvable=3)" \
    go test -run TestDecisionConstants_Values ./cf-conventions/cf-convention/...
check "TestDenyReasonCodes_AllStableCodesPresent (10 codes)" \
    go test -run TestDenyReasonCodes_AllStableCodesPresent ./cf-conventions/cf-convention/...
check "TestEvaluateRequest_TypesAreStdlibOnly (L2-clean types)" \
    go test -run TestEvaluateRequest_TypesAreStdlibOnly ./cf-conventions/cf-convention/...

# ── 5. Depguard L2-no-authority rule ─────────────────────────────────────────
echo ""
echo "── Depguard layer-boundary enforcement ──"
if command -v golangci-lint >/dev/null 2>&1 || [ -x "$HOME/bin/golangci-lint" ]; then
    check "L2-no-authority rule fires on intentional cf-authority import" \
        go test -run TestDepguardL2NoCfAuthorityCatchesForbiddenImport ./cf-conventions/cf-convention/...
    check "Current cf-convention/ tree is clean (no L2-no-authority violations)" \
        go test -run TestDepguardL2NoCfAuthorityClean ./cf-conventions/cf-convention/...
else
    echo "  SKIP: golangci-lint not found — depguard checks skipped"
    echo "        Install: https://golangci-lint.run/usage/install/"
fi

# ── 6. L2 does not import L3 cf-authority ────────────────────────────────────
echo ""
echo "── Layer-separation invariant ──"
check "cf-convention/ non-test imports do not include cf-authority" \
    bash -c '! go list -f "{{.Imports}}" ./cf-conventions/cf-convention/ | grep -q "cf-authority"'

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "Stage 2 GateEvaluator interface verification complete."
echo "Next: Stage 3 (campfireagent-02a) wires cf-authority/trust implementation to dispatcher."
