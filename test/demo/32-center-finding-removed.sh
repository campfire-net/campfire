#!/usr/bin/env bash
# 32-center-finding-removed.sh — demo/probe: verifies that center-finding symbols
# (recenter.go + walk_up.go) are absent from cf-protocol/protocol after campfireagent-db1.
#
# Exit 0 = absence confirmed (symbols removed)
# Exit 1 = symbols still present (removal incomplete)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PKG_DIR="$REPO_ROOT/cf-protocol/protocol"

echo "=== Stage 1 — center-finding removed (campfireagent-db1) ==="
echo ""

FAILURES=0

# 1. Check that the source files are deleted.
echo "Checking deleted files..."
for f in recenter.go walk_up.go; do
  if [ -f "$PKG_DIR/$f" ]; then
    echo "  FAIL: $f still exists (should be deleted)"
    FAILURES=$((FAILURES + 1))
  else
    echo "  OK:   $f absent"
  fi
done

# 2. Check that banned symbols are absent from non-test production Go files.
echo ""
echo "Checking symbol absence..."

check_absent() {
  local pattern="$1"
  local desc="$2"
  local matches
  matches=$(find "$PKG_DIR" -name '*.go' ! -name '*_test.go' -exec grep -l "$pattern" {} \; 2>/dev/null | wc -l)
  if [ "$matches" -gt 0 ]; then
    echo "  FAIL: symbol still present — $desc"
    find "$PKG_DIR" -name '*.go' ! -name '*_test.go' -exec grep -n "$pattern" {} + 2>/dev/null || true
    FAILURES=$((FAILURES + 1))
  else
    echo "  OK:   absent — $desc"
  fi
}

check_absent "func RecenterCanonicalPayload(" "RecenterCanonicalPayload (recenter.go)"
check_absent "type RecenterClaim " "RecenterClaim type (recenter.go)"
check_absent "func (c \*Client) maybeRecenter(" "maybeRecenter method (recenter.go)"
check_absent "func walkUpForCenter(" "walkUpForCenter function (walk_up.go)"
check_absent "func (c \*Client) WalkUpEnabled()" "WalkUpEnabled method (authorize.go)"
check_absent "func WithWalkUp()" "WithWalkUp option (options.go)"
check_absent "func WithNoWalkUp()" "WithNoWalkUp option (options.go)"

echo ""

# 3. Check that the package still compiles (builds clean).
echo "Building cf-protocol/protocol..."
if (cd "$REPO_ROOT" && /usr/local/go/bin/go build ./cf-protocol/protocol/) 2>/tmp/build-err.txt; then
  echo "  OK:   build clean"
else
  echo "  FAIL: build errors:"
  cat /tmp/build-err.txt
  FAILURES=$((FAILURES + 1))
fi

echo ""

if [ "$FAILURES" -eq 0 ]; then
  echo "PASS: center-finding symbols removed from cf-protocol/protocol (campfireagent-db1 complete)"
  exit 0
else
  echo "FAIL: $FAILURES check(s) failed — removal incomplete"
  exit 1
fi
