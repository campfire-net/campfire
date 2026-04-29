#!/usr/bin/env bash
# cf-protocol/demos/bridge-modes_test.sh
#
# TDD test for campfireagent-7a4: asserts bridge.md exists and contains
# all required sections. Updated post-veracity-finding to assert:
#   - 0.30.0 documents re-publish mode only
#   - pass-through is documented as planned (not implemented)
#   - no fictional Forward field claims as implemented API
#
# Run:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/bridge-modes_test.sh
#
# Exits non-zero if bridge.md is missing or any required section is absent.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BRIDGE_DOC="$REPO_ROOT/cf-protocol/docs/bridge.md"
BRIDGE_GO="$REPO_ROOT/pkg/protocol/bridge.go"

PASS=0
FAIL=0

assert_contains() {
  local description="$1"
  local pattern="$2"
  if grep -q "$pattern" "$BRIDGE_DOC" 2>/dev/null; then
    echo "  PASS: $description"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $description" >&2
    FAIL=$((FAIL + 1))
  fi
}

assert_not_contains() {
  local description="$1"
  local pattern="$2"
  if grep -q "$pattern" "$BRIDGE_DOC" 2>/dev/null; then
    echo "  FAIL: $description (found but should not exist)" >&2
    FAIL=$((FAIL + 1))
  else
    echo "  PASS: $description"
    PASS=$((PASS + 1))
  fi
}

assert_go_not_contains() {
  local description="$1"
  local pattern="$2"
  if grep -q "$pattern" "$BRIDGE_GO" 2>/dev/null; then
    echo "  FAIL: $description (found in bridge.go but should not exist)" >&2
    FAIL=$((FAIL + 1))
  else
    echo "  PASS: $description"
    PASS=$((PASS + 1))
  fi
}

echo "================================================================"
echo "  bridge.md TDD test (campfireagent-7a4)"
echo "================================================================"
echo ""

# ── Step 1: file must exist ────────────────────────────────────────────────
if [[ -f "$BRIDGE_DOC" ]]; then
  echo "  PASS: cf-protocol/docs/bridge.md exists"
  PASS=$((PASS + 1))
else
  echo "  FAIL: cf-protocol/docs/bridge.md does not exist" >&2
  echo ""
  echo "FAIL — $FAIL test(s) failed, $PASS passed"
  exit 1
fi
echo ""

# ── Step 2: required sections ─────────────────────────────────────────────
echo "Checking required sections:"
assert_contains "Section: Mode (re-publish)"              "## Mode"
assert_contains "Section: When to Use"                    "## When to Use"
assert_contains "Section: The Blind-Relay Role"           "## The Blind-Relay Role"
assert_contains "Section: What Bridge Does NOT Do"        "## What Bridge Does NOT Do"
assert_contains "Section: Planned for 0.30.x"             "## Planned for 0.30.x"
assert_contains "Section: Hosted-Reader Case Study"       "## Hosted-Reader Case Study"
echo ""

# ── Step 3: non-goals — adversarial condition ──────────────────────────────
echo "Checking adversarial (non-goals) content:"
assert_contains "States bridge is not a federation primitive" "Not a federation primitive"
assert_contains "States bridge does not decrypt/re-sign"      "decrypt"
assert_contains "States bridge does not cache"                "cache"
echo ""

# ── Step 4: 0.30.0 is re-publish only; pass-through is planned ────────────
echo "Checking 0.30.0 implementation status:"
assert_contains "0.30.0 mentioned as re-publish baseline"     "0.30.0"
assert_contains "Pass-through documented as planned"          "Planned"
assert_contains "rd item campfireagent-4a0 referenced"        "campfireagent-4a0"
echo ""

# ── Step 5: no fictional Forward field claims ──────────────────────────────
echo "Checking no fictional Forward flag documentation:"
assert_not_contains "No 'Forward: true' as implemented API"  "Forward: true"
assert_not_contains "No 'Forward: false' as implemented API" "Forward: false"
assert_go_not_contains "Forward field absent from BridgeOptions struct" \
  "Forward[[:space:]]bool"
echo ""

# ── Step 6: cross-links ────────────────────────────────────────────────────
echo "Checking cross-links:"
assert_contains "Links to protocol-spec.md"   "protocol-spec.md"
echo ""

echo "================================================================"
if [[ $FAIL -gt 0 ]]; then
  echo "  FAIL — $FAIL test(s) failed, $PASS passed"
  exit 1
else
  echo "  PASS — all $PASS tests passed"
fi
