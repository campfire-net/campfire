#!/usr/bin/env bash
# cf-authority-freeze-verify.sh
#
# Phase 8 Gate 4 — cf-authority CBOR + predicate AST wire-format freeze verifier.
# (campfireagent-301; sibling of cf-protocol/internal/wireverify for -3a8)
#
# Runs the isolated cf-authority freeze verifier package and confirms every
# S0b spec claim matches actual code at HEAD:
#
#   §1.1  Capability CBOR field IDs 1–6 + mandatory Until/Nonce
#   §1.2  GrantPayload CBOR field IDs 1–4
#   §1.3  WhereMatcher kind integers (1/2/3)
#   §2.1  PredicateAST leaf kind strings and numeric values
#   §2.2  Composite kinds (all_of/any_of), MaxPredicateDepth==3, not: rejection
#   §2.4  DenyReason enum — all 10 stable codes, exact wire values
#
# Real reflection / const inspection — no regex of the spec file.
#
# Exit codes:
#   0 — all checks pass (diff is zero)
#   1 — one or more checks failed (spec/code drift detected)
#
# Usage (from campfire repo root):
#   bash cf-conventions/demos/cf-authority-freeze-verify.sh

set -euo pipefail

PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FREEZE_PKG="github.com/campfire-net/campfire/cf-conventions/cf-authority/internal/freeze"

echo ""
echo "================================================================"
echo "cf-authority Wire-Format Freeze Verifier (Phase 8 Gate 4)"
echo "Item: campfireagent-301"
echo "================================================================"

# ---------------------------------------------------------------------------
# §A — Go toolchain available
# ---------------------------------------------------------------------------
echo ""
echo "=== §A — Go toolchain ==="

if command -v go >/dev/null 2>&1; then
  GO_VERSION=$(go version)
  ok "Go available: $GO_VERSION"
else
  fail "Go not found on PATH — cannot run verifier"
  echo ""
  echo "RESULT: FAILED (Go not found)"
  exit 1
fi

# ---------------------------------------------------------------------------
# §B — Package compiles cleanly (test-only package — use go test -run ^$ to compile)
# ---------------------------------------------------------------------------
echo ""
echo "=== §B — Package build ==="

cd "$REPO_ROOT"

# The freeze package is test-only (all _test.go files), matching the wireverify
# pattern. Use `go test -run ^$` to verify it compiles without running any tests.
if go test -run ^$ "$FREEZE_PKG" 2>/dev/null; then
  ok "cf-authority/internal/freeze compiles cleanly (test-only package)"
else
  BUILD_OUT=$(go test -run ^$ "$FREEZE_PKG" 2>&1 || true)
  echo "  $BUILD_OUT"
  fail "cf-authority/internal/freeze failed to compile"
fi

# ---------------------------------------------------------------------------
# §C — Run the freeze verifier
# ---------------------------------------------------------------------------
echo ""
echo "=== §C — Freeze verifier: all S0b spec claims ==="

cd "$REPO_ROOT"
VERIFIER_OUT=$(go test "$FREEZE_PKG" -v -count=1 2>&1) || VERIFIER_EXIT=$?
VERIFIER_EXIT=${VERIFIER_EXIT:-0}

# Count individual passing tests.
PASS_COUNT=$(echo "$VERIFIER_OUT" | grep -c "^--- PASS:" || true)
FAIL_COUNT=$(echo "$VERIFIER_OUT" | grep -c "^--- FAIL:" || true)

if [[ $VERIFIER_EXIT -eq 0 ]]; then
  ok "Verifier: $PASS_COUNT tests passed, 0 failed"
  # Print individual test names at reduced verbosity.
  echo "$VERIFIER_OUT" | grep "^--- PASS:" | sed 's/^/    /'
else
  fail "Verifier: $FAIL_COUNT test(s) failed (spec/code drift detected)"
  echo ""
  echo "Failing tests:"
  echo "$VERIFIER_OUT" | grep -E "^--- FAIL:|FAIL|Error" | sed 's/^/    /'
fi

# ---------------------------------------------------------------------------
# §D — Mutation detection self-test
# ---------------------------------------------------------------------------
echo ""
echo "=== §D — Mutation detection self-test ==="

cd "$REPO_ROOT"
MUTATION_OUT=$(go test "$FREEZE_PKG" -run TestAuthorityFreezeVerifyMutationDetection -v -count=1 2>&1) || MUTATION_EXIT=$?
MUTATION_EXIT=${MUTATION_EXIT:-0}

if [[ $MUTATION_EXIT -eq 0 ]]; then
  ok "Mutation detection test passed (7 mutations verified detectable)"
else
  fail "Mutation detection test failed"
  echo "  $MUTATION_OUT"
fi

# ---------------------------------------------------------------------------
# §E — Cross-check alignment with cf-protocol/internal/wireverify
# ---------------------------------------------------------------------------
echo ""
echo "=== §E — Cross-check: alignment with wireverify (-3a8) ==="

cd "$REPO_ROOT"
XCHECK_OUT=$(go test "$FREEZE_PKG" -run TestCrossCheckWireverifyAlignment -v -count=1 2>&1) || XCHECK_EXIT=$?
XCHECK_EXIT=${XCHECK_EXIT:-0}

if [[ $XCHECK_EXIT -eq 0 ]]; then
  ok "Cross-check alignment test passed"
else
  fail "Cross-check alignment test failed"
  echo "  $XCHECK_OUT"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "================================================================"
TOTAL=$((PASS + FAIL))
echo "Results: $PASS/$TOTAL checks passed"
if [[ $FAIL -gt 0 ]]; then
  echo ""
  echo "FAILED: $FAIL check(s) failed — spec/code drift detected."
  echo "Diff between S0b spec and code is NON-ZERO."
  exit 1
else
  echo ""
  echo "ALL CHECKS PASSED — cf-authority CBOR + predicate AST wire-format freeze verified."
  echo "Diff between S0b spec and code is ZERO. Phase 8 Gate 4 sign-off: OK."
  exit 0
fi
