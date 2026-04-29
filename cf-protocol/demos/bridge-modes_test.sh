#!/usr/bin/env bash
# cf-protocol/demos/bridge-modes_test.sh
#
# TDD test for campfireagent-7a4: asserts bridge.md exists and contains
# all six required sections before the demo script is run.
#
# Run:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/bridge-modes_test.sh
#
# Exits non-zero if bridge.md is missing or any required section is absent.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BRIDGE_DOC="$REPO_ROOT/cf-protocol/docs/bridge.md"

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

# ── Step 2: six required sections ─────────────────────────────────────────
echo "Checking required sections:"
assert_contains "Section: Modes"                         "## Modes"
assert_contains "Section: When to Use"                   "## When to Use"
assert_contains "Section: The Blind-Relay Role"          "## The Blind-Relay Role"
assert_contains "Section: What Bridge Does NOT Do"       "## What Bridge Does NOT Do"
assert_contains "Section: Pass-Through Threat Model"     "## Pass-Through Threat Model"
assert_contains "Section: Hosted-Reader Case Study"      "## Hosted-Reader Case Study"
echo ""

# ── Step 3: non-goals — adversarial condition ──────────────────────────────
echo "Checking adversarial (non-goals) content:"
assert_contains "States bridge is not a federation primitive" "Not a federation primitive"
assert_contains "States bridge does not decrypt/re-sign"      "decrypt"
assert_contains "States bridge does not cache"                "cache"
echo ""

# ── Step 4: cross-links ────────────────────────────────────────────────────
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
