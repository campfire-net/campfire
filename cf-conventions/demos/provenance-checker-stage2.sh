#!/usr/bin/env bash
# provenance-checker-stage2.sh — Demo: ProvenanceCheckerV2 interface declared in L2 (campfireagent-2ac).
#
# Proves that:
#   1. cf-conventions/cf-convention/ compiles cleanly with ProvenanceCheckerV2 interface.
#   2. ProvenanceCheckerV2 interface signature matches design v2 §4.2 exactly.
#   3. ProvenanceLevel constants (0–3) match design v2 §2.1 exactly.
#   4. AllowAllProvenanceChecker stub returns ProvenanceLevelPresent (3) unconditionally.
#   5. WithProvenanceV2 wires the stub to the dispatcher and passes level gates.
#   6. depguard L2-no-extensions rule holds (cf-convention does not import L3 packages).
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/provenance-checker-stage2.sh
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
    if "$@" >/dev/null 2>&1; then
        echo "  PASS: $label"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: $label"
        FAIL=$((FAIL + 1))
    fi
}

echo "=== Stage 2: ProvenanceCheckerV2 interface (campfireagent-2ac) ==="
echo ""

# ── 1. Package compiles cleanly ───────────────────────────────────────────────
echo "── Compilation ──"
check "cf-conventions/cf-convention builds cleanly" \
    go build ./cf-conventions/cf-convention/...
check "provenance_checker.go has ProvenanceCheckerV2 interface" \
    grep -q "type ProvenanceCheckerV2 interface" cf-conventions/cf-convention/provenance_checker.go
check "provenance_checker.go has AllowAllProvenanceChecker stub" \
    grep -q "allowAllProvenanceChecker" cf-conventions/cf-convention/provenance_checker.go
check "provenance_checker.go does not import cf-authority (only stdlib)" \
    bash -c '! go list -f "{{.Imports}}" ./cf-conventions/cf-convention/ | grep -q "cf-authority"'

# ── 2. Interface signature matches design v2 §4.2 ────────────────────────────
echo ""
echo "── Interface signature (design v2 §4.2) ──"
check "CheckProvenance(ctx context.Context, req ProvenanceRequest) ProvenanceResult signature present" \
    grep -q "CheckProvenance(ctx context.Context, req ProvenanceRequest) ProvenanceResult" \
    cf-conventions/cf-convention/provenance_checker.go
check "ProvenanceRequest struct declared" \
    grep -q "type ProvenanceRequest struct" cf-conventions/cf-convention/provenance_checker.go
check "ProvenanceResult struct declared" \
    grep -q "type ProvenanceResult struct" cf-conventions/cf-convention/provenance_checker.go
check "ProvenanceLevel type declared" \
    grep -q "type ProvenanceLevel int" cf-conventions/cf-convention/provenance_checker.go

# ── 3. ProvenanceLevel constants (design v2 §2.1) ────────────────────────────
echo ""
echo "── ProvenanceLevel constants ──"
check "ProvenanceLevelAnonymous = 0" \
    grep -q "ProvenanceLevelAnonymous.*ProvenanceLevel = 0" \
    cf-conventions/cf-convention/provenance_checker.go
check "ProvenanceLevelClaimed = 1" \
    grep -q "ProvenanceLevelClaimed.*ProvenanceLevel = 1" \
    cf-conventions/cf-convention/provenance_checker.go
check "ProvenanceLevelContactable = 2" \
    grep -q "ProvenanceLevelContactable.*ProvenanceLevel = 2" \
    cf-conventions/cf-convention/provenance_checker.go
check "ProvenanceLevelPresent = 3" \
    grep -q "ProvenanceLevelPresent.*ProvenanceLevel = 3" \
    cf-conventions/cf-convention/provenance_checker.go

# ── 4. Executor v2 integration ───────────────────────────────────────────────
echo ""
echo "── Executor v2 integration ──"
check "Executor has provenanceV2 field" \
    grep -q "provenanceV2.*ProvenanceCheckerV2" cf-conventions/cf-convention/executor.go
check "WithProvenanceV2 method declared on Executor" \
    grep -q "func.*WithProvenanceV2" cf-conventions/cf-convention/executor.go

# ── 5. Run targeted unit tests ────────────────────────────────────────────────
echo ""
echo "── Unit tests ──"
check "TestProvenanceCheckerTypes" \
    go test -run TestProvenanceCheckerTypes ./cf-conventions/cf-convention/...
check "TestProvenanceLevelConstants (0=Anonymous, 1=Claimed, 2=Contactable, 3=Present)" \
    go test -run TestProvenanceLevelConstants ./cf-conventions/cf-convention/...
check "TestAllowAllProvenanceChecker_AlwaysReturnsPresent" \
    go test -run TestAllowAllProvenanceChecker_AlwaysReturnsPresent ./cf-conventions/cf-convention/...
check "TestAllowAllProvenanceChecker_ImplementsInterface (compile-time)" \
    go test -run TestAllowAllProvenanceChecker_ImplementsInterface ./cf-conventions/cf-convention/...
check "TestProvenanceCheckerV2_StubInDispatcher (v2 wired to executor)" \
    go test -run TestProvenanceCheckerV2_StubInDispatcher ./cf-conventions/cf-convention/...

# ── 6. Depguard L2-no-extensions rule ────────────────────────────────────────
echo ""
echo "── Depguard layer-boundary enforcement ──"
if command -v golangci-lint >/dev/null 2>&1 || [ -x "$HOME/bin/golangci-lint" ]; then
    check "L2-no-extensions rule: current cf-convention/ tree is clean" \
        go test -run TestDepguardL2NoExtensionsClean ./cf-conventions/cf-convention/...
else
    echo "  SKIP: golangci-lint not found — depguard checks skipped"
    echo "        Install: https://golangci-lint.run/usage/install/"
fi

# ── 7. Layer-separation invariant ────────────────────────────────────────────
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
echo "Stage 2 ProvenanceCheckerV2 interface verification complete."
echo "Next: Stage 3 (cf-authority/provenance) provides a real implementation."
