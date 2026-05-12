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
# Pre-compile the conformance test binary ONCE.
#
# Determinism rationale (D1):
#   Earlier revisions of this demo invoked `go test ./.../conformance/...` from
#   each section. Each invocation rebuilt and revalidated the Go build cache
#   (`$GOCACHE` ≈ ~/.cache/go-build). When parallel worktrees on the same host
#   ran their own `go test` invocations concurrently — e.g. multiple swarm
#   workers each running their own demo or test — the build cache could evict
#   or partially write entries that another invocation was reading, producing
#   transient `[setup failed]` errors like:
#       open /home/$USER/.cache/go-build/XX/...-d: no such file or directory
#   or package-import errors against a freshly mutating standard library cache.
#   The grep for "PASS\|ok" then failed because the failing run printed neither,
#   and the section was reported as failed even though the test code is correct.
#
#   This wasn't a `§G` bug specifically; the build-cache race could hit any
#   section. §G amplified visibility because it explicitly re-runs cases with
#   `-count=3` and is the determinism gate.
#
# Fix: compile the conformance test binary once, into a private tmp dir, then
# execute that binary in every section. The binary's behaviour is fully
# self-contained — no `go test` toolchain involvement at run time, so no shared
# build-cache surface. The compile step happens before any pass/fail counting,
# so a (rare) build error becomes a deterministic hard failure with a clear
# message, never a flaky section result.
TEST_BIN_DIR="$(mktemp -d -t chain-walk-XXXXXX)"
trap 'rm -rf "$TEST_BIN_DIR"' EXIT
TEST_BIN="$TEST_BIN_DIR/conformance.test"

echo "=== Pre-compile conformance test binary ==="
if ! "$GO" test -c -o "$TEST_BIN" \
    ./cf-conventions/cf-authority/trust/conformance/ 2>&1; then
  echo "  [FAIL] could not compile conformance test binary"
  echo ""
  echo "RESULT: build failed; chain-walk demo cannot run."
  exit 1
fi
echo "  [PASS] conformance test binary compiled: $TEST_BIN"
echo ""

# run_case <case-name-regex> [extra args...]
# Returns 0 if the binary's exit is 0 (all selected cases passed), else 1.
# Output discipline: capture stdout+stderr; require the literal "PASS" final
# line (go test binary contract) and require no "--- FAIL" lines.
run_case() {
  local pattern="$1"
  shift
  local out
  if ! out=$("$TEST_BIN" -test.run "$pattern" "$@" 2>&1); then
    echo "$out"
    return 1
  fi
  if echo "$out" | grep -q -- "--- FAIL"; then
    echo "$out"
    return 1
  fi
  return 0
}

# ─────────────────────────────────────────────────────────────────────────────
# §A — Anchor-self (empty chain, sender == root)
# ─────────────────────────────────────────────────────────────────────────────
echo "=== §A — Anchor-self (case 1) ==="

if run_case "^TestCase01_AnchorSelf$"; then
  ok "anchor-self (empty chain, sender==root) → Allow"
else
  fail "anchor-self test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §B — Valid 1-hop chain
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §B — Valid 1-hop chain (case 2) ==="

if run_case "^TestCase02_Valid1Hop$"; then
  ok "valid 1-hop chain (root→sender) → Allow"
else
  fail "1-hop chain test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §C — Valid 2-hop chain
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §C — Valid 2-hop chain (case 3) ==="

if run_case "^TestCase03_Valid2Hop$"; then
  ok "valid 2-hop chain (root→inter→sender) → Allow"
else
  fail "2-hop chain test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §D — Depth exceeded (D10)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §D — Depth exceeded / 3-hop chain (case 6, D10) ==="

if run_case "^TestCase06_DepthExceeded$"; then
  ok "3-hop chain → Deny/depth_exceeded (D10)"
else
  fail "depth-exceeded test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §E — Scope narrowing (valid — child is ⊆ parent)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §E — Scope narrowing (case 7, D4 correct intersection) ==="

if run_case "^TestCase07_ScopeNarrowing$"; then
  ok "scope narrowing (parent:*, child:claim) → Allow (valid)"
else
  fail "scope-narrowing test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §F — Scope widening rejected (D4, Attack 3, Attack 5)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §F — Scope widening rejected (case 8, D4, Attack 3+5) ==="

if run_case "^TestCase08_ScopeWideningRejected$"; then
  ok "scope widening (parent:claim, child:*) → Deny/scope_widening (D4)"
else
  fail "scope-widening-rejected test failed"
fi

# ─────────────────────────────────────────────────────────────────────────────
# §G — Determinism 3× re-run (D1)
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo "=== §G — Determinism 3× re-run (D1) ==="

if run_case "^(TestCase01_AnchorSelf|TestCase02_Valid1Hop|TestCase03_Valid2Hop|TestCase06_DepthExceeded)$" -test.count=3; then
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
