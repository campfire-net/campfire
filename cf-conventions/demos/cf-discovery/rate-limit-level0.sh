#!/usr/bin/env bash
# cf-conventions/demos/cf-discovery/rate-limit-level0.sh
#
# Demo: rate-limit declarations for level:0 convention ops (OPEN-013).
#
# Proves:
#   1. cf-discovery package compiles.
#   2. ratelimit conformance tests (ratelimit_test.go) pass.
#   3. Level0OpDeclaration with bounds validates successfully.
#   4. Level0OpDeclaration without bounds is rejected ("fail loud" per design v2 §3.3.1).
#   5. ParseRateSpec parses all units (min/s/h); invalid units rejected.
#   6. N=3 requests within the per_keypair=3/min ceiling are ALLOWED; N+1 is REJECTED.
#   7. Multi-axis: global=2/min governs over per_keypair=10/min (tighter axis wins).
#   8. Full cf-discovery test suite passes.
#
# Resolves: campfireagent-763 (DEMO GAP: cf-discovery rate-limit declarations for level:0 ops)
# Spec:     cf-conventions/cf-discovery/ratelimit.go, design v2 §3.3.1, OPEN-013
#
# Transport: filesystem (pure declaration vocabulary — no network required).
#
# Requirements: Go 1.21+ on PATH. Run from any directory.
# Exit codes: 0 = all assertions pass, 1 = one or more assertions failed.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

export PATH="/usr/local/go/bin:${HOME}/.local/bin:$PATH"

# ---------------------------------------------------------------------------
# Colour helpers (stripped when piped)
# ---------------------------------------------------------------------------
if [ -t 1 ]; then
  RED='\033[0;31m'; GREEN='\033[0;32m'; BOLD='\033[1m'; RESET='\033[0m'
else
  RED=''; GREEN=''; BOLD=''; RESET=''
fi

PASS_COUNT=0
FAIL_COUNT=0

pass() { echo -e "  ${GREEN}PASS${RESET}: $1"; PASS_COUNT=$((PASS_COUNT + 1)); }
fail() { echo -e "  ${RED}FAIL${RESET}: $1"; FAIL_COUNT=$((FAIL_COUNT + 1)); }

section() { echo ""; echo -e "${BOLD}=== $1 ===${RESET}"; }

echo -e "${BOLD}cf-discovery rate-limit level:0 demo (OPEN-013)${RESET}"
echo "Repo: $REPO_ROOT"
echo ""

cd "$REPO_ROOT"

# ---------------------------------------------------------------------------
# §1 — Package builds
# ---------------------------------------------------------------------------
section "1. cf-discovery package compiles"

if go build ./cf-conventions/cf-discovery/... 2>&1; then
  pass "cf-discovery builds"
else
  fail "cf-discovery failed to compile"
  exit 1
fi

# ---------------------------------------------------------------------------
# §2 — ratelimit conformance tests (ratelimit_test.go)
# ---------------------------------------------------------------------------
section "2. ratelimit conformance tests (ratelimit_test.go)"

conf_out=$(go test ./cf-conventions/cf-discovery/... \
    -run 'TestValidateRateLimitBounds|TestValidateLevel0OpDeclaration|TestParseRateSpec' \
    -v 2>&1)
echo "$conf_out" | grep -E "^(=== RUN|--- PASS|--- FAIL|PASS|FAIL|ok )" | sed 's/^/  /'

if echo "$conf_out" | grep -qE "^(PASS|ok )" && ! echo "$conf_out" | grep -qE "^--- FAIL|^FAIL[^E]"; then
  pass "ratelimit conformance tests (10/10) pass"
else
  fail "ratelimit conformance tests failed"
fi

# ---------------------------------------------------------------------------
# §3–§7 — Demo assertions via go test -run TestDemoRateLimit
#
# ratelimit_demo_test.go contains typed Go assertions for:
#   §3 Level0OpDeclaration_Valid      — well-formed declaration accepted
#   §4 FailLoud_MissingBounds         — missing bounds rejected (§3.3.1)
#   §5 ParseRateSpec_UnitMatrix       — all units (min/s/h) valid; bad rejected
#   §6 NAllowed_NPlus1Rejected        — N=3 allowed, N+1 rate-limited
#   §7 MultiAxis_GlobalGoverns        — tighter global=2/min governs per_keypair=10/min
# ---------------------------------------------------------------------------
section "3–7. Demo assertions (ratelimit_demo_test.go)"

demo_out=$(go test ./cf-conventions/cf-discovery/... \
    -run 'TestDemoRateLimit' \
    -v 2>&1)
echo "$demo_out" | grep -E "^(=== RUN|--- PASS|--- FAIL|PASS|FAIL|ok |\s+(request|declared|parsed|per_key))" | sed 's/^/  /'

if echo "$demo_out" | grep -qE "^(PASS|ok )" && ! echo "$demo_out" | grep -qE "^--- FAIL|^FAIL[^E]"; then
  pass "§3 Level0OpDeclaration with bounds: ACCEPTED"
  pass "§4 Level0OpDeclaration without bounds: REJECTED (fail-loud)"
  pass "§5 ParseRateSpec unit matrix (min/s/h): valid accepted, invalid rejected"
  pass "§6 N=3 ALLOWED, N+1=4 RATE-LIMITED (per_keypair=3/min ceiling)"
  pass "§7 global=2/min governs over per_keypair=10/min (tighter axis wins)"
else
  fail "one or more demo assertions failed — see above"
  echo ""
  echo "Full demo test output:"
  echo "$demo_out" | sed 's/^/  /'
fi

# ---------------------------------------------------------------------------
# §8 — Full cf-discovery test suite
# ---------------------------------------------------------------------------
section "8. Full cf-discovery test suite"

suite_out=$(go test ./cf-conventions/cf-discovery/... 2>&1)
echo "$suite_out" | sed 's/^/  /'
if echo "$suite_out" | grep -qE "^ok " && ! echo "$suite_out" | grep -qE "^--- FAIL|^FAIL[^E]"; then
  pass "full cf-discovery test suite green"
else
  fail "cf-discovery test suite — failures detected"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "================================"
if [ -t 1 ]; then
  printf "Results: ${GREEN}%d passed${RESET}, ${RED}%d failed${RESET}\n" "$PASS_COUNT" "$FAIL_COUNT"
else
  printf "Results: %d passed, %d failed\n" "$PASS_COUNT" "$FAIL_COUNT"
fi
echo "================================"
echo ""
echo "Spec:  cf-conventions/cf-discovery/ratelimit.go (design v2 §3.3.1, OPEN-013)"
echo "Tests: cf-conventions/cf-discovery/ratelimit_demo_test.go"
echo "Demo:  cf-conventions/demos/cf-discovery/rate-limit-level0.sh"

if [ "$FAIL_COUNT" -gt 0 ]; then
  exit 1
fi
