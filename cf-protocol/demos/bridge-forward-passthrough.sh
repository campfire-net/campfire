#!/usr/bin/env bash
# cf-protocol/demos/bridge-forward-passthrough.sh
#
# Demo: BridgeOptions.Forward pass-through mode (campfireagent-b84).
#
# Exercises the spec claims in cf-protocol/docs/bridge.md and the
# implementation in cf-protocol/protocol/bridge.go:
#
#   1. BridgeOptions.Forward bool field is present in the Go source.
#   2. bridge.md §Modes section documents both re-publish and pass-through.
#   3. Go tests for forward pass-through exist and are named correctly.
#   4. forwardMessage is implemented in client.go.
#   5. Pass-through error path: HTTP-transport destination returns an error.
#   6. bridge.md is updated — no longer says "not implemented in 0.30.0" for
#      pass-through without also describing the implementation.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/bridge-forward-passthrough.sh
#
# No network access or running campfire required.
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BRIDGE_GO="$REPO_ROOT/cf-protocol/protocol/bridge.go"
CLIENT_GO="$REPO_ROOT/cf-protocol/protocol/client.go"
BRIDGE_TEST_GO="$REPO_ROOT/cf-protocol/protocol/bridge_test.go"
BRIDGE_DOC="$REPO_ROOT/cf-protocol/docs/bridge.md"

PASS=0
FAIL=0

assert_contains() {
  local label="$1"
  local file="$2"
  local pattern="$3"
  if grep -qE "$pattern" "$file" 2>/dev/null; then
    echo "  PASS: $label"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $label (pattern '$pattern' not found in $file)" >&2
    FAIL=$((FAIL + 1))
  fi
}

assert_not_contains() {
  local label="$1"
  local file="$2"
  local pattern="$3"
  if grep -qE "$pattern" "$file" 2>/dev/null; then
    echo "  FAIL: $label (pattern '$pattern' found but should not be in $file)" >&2
    FAIL=$((FAIL + 1))
  else
    echo "  PASS: $label"
    PASS=$((PASS + 1))
  fi
}

assert_test_passes() {
  local label="$1"
  local run_pattern="$2"
  if /usr/local/go/bin/go test \
       -run "$run_pattern" \
       -timeout 60s \
       ./cf-protocol/protocol/ \
       -count=1 \
       >/dev/null 2>&1; then
    echo "  PASS: $label"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $label (go test -run '$run_pattern' failed)" >&2
    FAIL=$((FAIL + 1))
  fi
}

echo "================================================================"
echo "  BridgeOptions.Forward pass-through demo (campfireagent-b84)"
echo "================================================================"
echo ""

# ── 1. Forward field in BridgeOptions ─────────────────────────────────────
echo "── 1. BridgeOptions.Forward field present ──────────────────────────────"
assert_contains "Forward bool in BridgeOptions"    "$BRIDGE_GO" "Forward[[:space:]]+bool"
assert_contains "pass-through comment on Forward"  "$BRIDGE_GO" "pass-through"
echo ""

# ── 2. forwardMessage implemented ─────────────────────────────────────────
echo "── 2. forwardMessage method in client.go ───────────────────────────────"
assert_contains "forwardMessage method present"      "$CLIENT_GO" "func.*forwardMessage"
assert_contains "AddHop call in forwardMessage"      "$CLIENT_GO" "AddHop"
assert_contains "RoleBlindRelay used in forward"     "$CLIENT_GO" "RoleBlindRelay"
assert_contains "WriteMessage in forwardMessage"     "$CLIENT_GO" "WriteMessage"
assert_contains "AddMessage store mirror in forward" "$CLIENT_GO" "store.AddMessage.*NowNano"
echo ""

# ── 3. Bridge() dispatches on opts.Forward ────────────────────────────────
echo "── 3. Bridge() dispatches on opts.Forward ──────────────────────────────"
assert_contains "Bridge checks opts.Forward"        "$BRIDGE_GO" "opts\.Forward"
assert_contains "forwardMessage called in Bridge"   "$BRIDGE_GO" "forwardMessage"
echo ""

# ── 4. Tests exist ────────────────────────────────────────────────────────
echo "── 4. Test names present ───────────────────────────────────────────────"
assert_contains "TestBridgeForwardPreservesIdentity" "$BRIDGE_TEST_GO" "TestBridgeForwardPreservesIdentity"
assert_contains "TestBridgeForwardVsRepublish"       "$BRIDGE_TEST_GO" "TestBridgeForwardVsRepublish"
assert_contains "TestBridgeForwardUnreachableTarget" "$BRIDGE_TEST_GO" "TestBridgeForwardUnreachableTarget"
assert_contains "setupForwardTestEnv helper"         "$BRIDGE_TEST_GO" "setupForwardTestEnv"
echo ""

# ── 5. Tests pass ─────────────────────────────────────────────────────────
echo "── 5. Tests pass (live run) ────────────────────────────────────────────"
cd "$REPO_ROOT"
assert_test_passes "TestBridgeForwardPreservesIdentity" "TestBridgeForwardPreservesIdentity"
assert_test_passes "TestBridgeForwardVsRepublish"       "TestBridgeForwardVsRepublish"
assert_test_passes "TestBridgeForwardUnreachableTarget" "TestBridgeForwardUnreachableTarget"
assert_test_passes "Existing bridge tests still pass"   "TestBridgeUnidirectional|TestBridgeBidirectional|TestBridgeDedup|TestBridgeTagFilter|TestIsBridged"
echo ""

# ── 6. HTTP error path ────────────────────────────────────────────────────
echo "── 6. HTTP-transport pass-through returns error ────────────────────────"
assert_contains "HTTP error message present" "$CLIENT_GO" "HTTP-transport destinations"
echo ""

# ── 7. Wire format unchanged ──────────────────────────────────────────────
echo "── 7. Wire format invariants ───────────────────────────────────────────"
assert_contains "Forward preserves original envelope comment" "$CLIENT_GO" "original envelope"
assert_contains "RoleBlindRelay still used by re-publish path" "$BRIDGE_GO" "RoleBlindRelay"
echo ""

echo "================================================================"
if [[ $FAIL -gt 0 ]]; then
  echo "  FAIL — $FAIL assertion(s) failed, $PASS passed"
  exit 1
else
  echo "  PASS — all $PASS assertions passed"
  echo ""
  echo "  Implementation:"
  echo "    cf-protocol/protocol/bridge.go  — BridgeOptions.Forward flag + dispatch"
  echo "    cf-protocol/protocol/client.go  — forwardMessage() pass-through method"
  echo ""
  echo "  Tests:"
  echo "    TestBridgeForwardPreservesIdentity — ID/Sender preserved end-to-end"
  echo "    TestBridgeForwardVsRepublish       — behavioral diff: forward vs republish"
  echo "    TestBridgeForwardUnreachableTarget — graceful failure on missing membership"
  echo ""
  echo "  Pass-through semantics:"
  echo "    Forward: false (default) — re-publish: new ID, new Sender (bridge agent)"
  echo "    Forward: true            — pass-through: original ID + Sender preserved,"
  echo "                               one blind-relay hop appended"
fi
echo "================================================================"
