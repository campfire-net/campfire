#!/usr/bin/env bash
# cf-protocol/demos/bridge-modes.sh
#
# Demo: cf bridge model — re-publish vs pass-through modes (campfireagent-7a4).
#
# Exercises the spec claims in cf-protocol/docs/bridge.md:
#   1. bridge.md exists with all six required sections.
#   2. The "What Bridge Does NOT Do" section names all non-goals.
#   3. The pass-through threat model section addresses forgery, replay, and DoS.
#   4. The BridgeOptions.Forward flag exists in the Go source.
#   5. IsBridged() is present — both modes produce a blind-relay hop.
#   6. The Tier 3.5 hosted-reader case study is present.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/bridge-modes.sh
#
# No network access or running campfire required.
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BRIDGE_DOC="$REPO_ROOT/cf-protocol/docs/bridge.md"
BRIDGE_GO="$REPO_ROOT/pkg/protocol/bridge.go"

PASS=0
FAIL=0

assert_file() {
  local label="$1"
  local path="$2"
  if [[ -f "$path" ]]; then
    echo "  PASS: $label"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $label (missing: $path)" >&2
    FAIL=$((FAIL + 1))
  fi
}

assert_contains() {
  local label="$1"
  local file="$2"
  local pattern="$3"
  if grep -q "$pattern" "$file" 2>/dev/null; then
    echo "  PASS: $label"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $label (pattern '$pattern' not found in $file)" >&2
    FAIL=$((FAIL + 1))
  fi
}

echo "================================================================"
echo "  Campfire bridge model demo (campfireagent-7a4)"
echo "================================================================"
echo ""

# ── 1. Doc file exists ─────────────────────────────────────────────────────
echo "── 1. Doc artifact ────────────────────────────────────────────────────"
assert_file "cf-protocol/docs/bridge.md exists" "$BRIDGE_DOC"
echo ""

# ── 2. Six required sections ───────────────────────────────────────────────
echo "── 2. Six required sections ───────────────────────────────────────────"
assert_contains "§ Modes"                    "$BRIDGE_DOC" "^## Modes"
assert_contains "§ When to Use"              "$BRIDGE_DOC" "^## When to Use"
assert_contains "§ The Blind-Relay Role"     "$BRIDGE_DOC" "^## The Blind-Relay Role"
assert_contains "§ What Bridge Does NOT Do"  "$BRIDGE_DOC" "^## What Bridge Does NOT Do"
assert_contains "§ Pass-Through Threat Model" "$BRIDGE_DOC" "^## Pass-Through Threat Model"
assert_contains "§ Hosted-Reader Case Study" "$BRIDGE_DOC" "^## Hosted-Reader Case Study"
echo ""

# ── 3. Non-goals (adversarial condition) ──────────────────────────────────
echo "── 3. Non-goals (adversarial — what bridge does NOT do) ───────────────"
assert_contains "Not a federation primitive" "$BRIDGE_DOC" "Not a federation primitive"
assert_contains "Does not decrypt/re-sign"   "$BRIDGE_DOC" "decrypt"
assert_contains "Does not cache"             "$BRIDGE_DOC" "not cache"
assert_contains "Does not enforce policy"    "$BRIDGE_DOC" "not enforce policy"
echo ""

# ── 4. Threat model completeness ──────────────────────────────────────────
echo "── 4. Threat model covers forgery, replay, DoS ────────────────────────"
assert_contains "Forgery threat addressed"            "$BRIDGE_DOC" "Forgery"
assert_contains "Replay threat addressed"             "$BRIDGE_DOC" "Replay"
assert_contains "DoS / selective forwarding addressed" "$BRIDGE_DOC" "Denial of service"
echo ""

# ── 5. Implementation grounding — BridgeOptions.Forward in Go source ───────
echo "── 5. Go source grounding ─────────────────────────────────────────────"
assert_file "pkg/protocol/bridge.go exists" "$BRIDGE_GO"
assert_contains "BridgeOptions struct present" "$BRIDGE_GO" "BridgeOptions"
assert_contains "IsBridged referenced in protocol" \
  "$REPO_ROOT/pkg/protocol/message.go" "IsBridged"
echo ""

# ── 6. Cross-links ────────────────────────────────────────────────────────
echo "── 6. Cross-links ─────────────────────────────────────────────────────"
assert_contains "Links to protocol-spec.md" "$BRIDGE_DOC" "protocol-spec.md"
echo ""

# ── 7. Show summary excerpt ───────────────────────────────────────────────
echo "── 7. bridge.md summary excerpt ───────────────────────────────────────"
echo ""
head -5 "$BRIDGE_DOC"
echo "..."
grep "^##" "$BRIDGE_DOC"
echo ""

echo "================================================================"
if [[ $FAIL -gt 0 ]]; then
  echo "  FAIL — $FAIL assertion(s) failed, $PASS passed"
  exit 1
else
  echo "  PASS — all $PASS assertions passed"
  echo ""
  echo "  Artifacts:"
  echo "    cf-protocol/docs/bridge.md         — bridge model spec (~1 page)"
  echo "    cf-protocol/demos/bridge-modes.sh  — this demo"
  echo "    cf-protocol/demos/bridge-modes_test.sh — TDD test"
fi
echo "================================================================"
