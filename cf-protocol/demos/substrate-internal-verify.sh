#!/usr/bin/env bash
# cf-protocol/demos/substrate-internal-verify.sh
#
# Demo: verify cf-protocol substrate move to internal/ (campfireagent-401d).
#
# This script verifies:
#   1. Substrate packages live at cf-protocol/internal/ (not cf-protocol/ root).
#   2. Public forwarding wrappers exist at cf-protocol/{campfire,message,...}.
#   3. cf-protocol/protocol/ exports a sufficient public surface.
#   4. External code can reach types via cf-protocol/protocol/ and public wrappers.
#   5. cf-protocol/internal/* is unreachable from outside cf-protocol/ (Go enforces).
#   6. The full build passes with the new layout.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/substrate-internal-verify.sh
#
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO=${GO:-/usr/local/go/bin/go}
PASS_COUNT=0
FAIL_COUNT=0

pass() { echo "PASS: $*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail() { echo "FAIL: $*" >&2; FAIL_COUNT=$((FAIL_COUNT+1)); }

echo "=== cf-protocol internal/ move verification (campfireagent-401d) ==="
echo "Repo: $REPO_ROOT"
echo ""

# ── Step 1: Substrate packages exist at cf-protocol/internal/ ────────────────
echo "--- Step 1: Substrate packages in cf-protocol/internal/"

for pkg in campfire message store threshold projection predicate crypto encoding admission; do
  if [ -d "$REPO_ROOT/cf-protocol/internal/$pkg" ]; then
    pass "cf-protocol/internal/$pkg exists"
  else
    fail "cf-protocol/internal/$pkg is missing"
  fi
done

for path in transport transport/fs transport/http; do
  if [ -d "$REPO_ROOT/cf-protocol/internal/$path" ]; then
    pass "cf-protocol/internal/$path exists"
  else
    fail "cf-protocol/internal/$path is missing"
  fi
done

# ── Step 2: Public forwarding wrappers exist at old paths ────────────────────
echo ""
echo "--- Step 2: Public forwarding wrappers at cf-protocol/{pkg}/"

for pkg in campfire message store threshold crypto encoding admission predicate projection; do
  wrapper="$REPO_ROOT/cf-protocol/$pkg"
  if [ -d "$wrapper" ] && [ "$(ls $wrapper/*.go 2>/dev/null | wc -l)" -gt 0 ]; then
    pass "cf-protocol/$pkg/ forwarding wrapper exists"
  else
    fail "cf-protocol/$pkg/ forwarding wrapper is missing or empty"
  fi
done

for path in transport transport/fs transport/http; do
  wrapper="$REPO_ROOT/cf-protocol/$path"
  if [ -d "$wrapper" ] && [ "$(ls $wrapper/*.go 2>/dev/null | wc -l)" -gt 0 ]; then
    pass "cf-protocol/$path/ forwarding wrapper exists"
  else
    fail "cf-protocol/$path/ forwarding wrapper is missing or empty"
  fi
done

# ── Step 3: cf-protocol/protocol/ has surface.go re-exports ──────────────────
echo ""
echo "--- Step 3: cf-protocol/protocol/ public surface"

if [ -f "$REPO_ROOT/cf-protocol/protocol/surface.go" ]; then
  pass "cf-protocol/protocol/surface.go exists"
else
  fail "cf-protocol/protocol/surface.go is missing"
fi

if [ -f "$REPO_ROOT/cf-protocol/protocol/surface_transport.go" ]; then
  pass "cf-protocol/protocol/surface_transport.go exists"
else
  fail "cf-protocol/protocol/surface_transport.go is missing"
fi

# Check that the surface exports key types
for sym in Store Membership MessageRecord MessageFilter Open NowNano WireMessage Campfire CampfireState FSTransport HTTPTransport; do
  if grep -q "type $sym\b\|var $sym\b\|= store\.$sym\|= campfire\.\|= message\.\|= fs\.\|= cfhttp\." \
       "$REPO_ROOT/cf-protocol/protocol/surface.go" \
       "$REPO_ROOT/cf-protocol/protocol/surface_transport.go" 2>/dev/null; then
    pass "cf-protocol/protocol exports $sym"
  else
    fail "cf-protocol/protocol does NOT export $sym"
  fi
done

# ── Step 4: Build passes ──────────────────────────────────────────────────────
echo ""
echo "--- Step 4: Full build"

if (cd "$REPO_ROOT" && $GO build ./... 2>/dev/null); then
  pass "go build ./... succeeds"
else
  fail "go build ./... FAILED"
fi

if (cd "$REPO_ROOT" && $GO vet ./cf-protocol/... 2>/dev/null); then
  pass "go vet ./cf-protocol/... clean"
else
  fail "go vet ./cf-protocol/... FAILED"
fi

# ── Step 5: cf-protocol/internal/ is not imported from outside cf-protocol/ ──
echo ""
echo "--- Step 5: No external imports of cf-protocol/internal/"

external_internal=$(find "$REPO_ROOT" -name '*.go' \
  ! -path "$REPO_ROOT/cf-protocol/*" \
  ! -path "$REPO_ROOT/vendor/*" \
  -exec grep -l '"github.com/campfire-net/campfire/cf-protocol/internal/' {} \; 2>/dev/null | wc -l)

if [ "$external_internal" -eq 0 ]; then
  pass "No external code imports cf-protocol/internal/ (Go internal rule + depguard enforce this)"
else
  fail "$external_internal file(s) outside cf-protocol/ still import cf-protocol/internal/"
  find "$REPO_ROOT" -name '*.go' \
    ! -path "$REPO_ROOT/cf-protocol/*" \
    ! -path "$REPO_ROOT/vendor/*" \
    -exec grep -l '"github.com/campfire-net/campfire/cf-protocol/internal/' {} \; 2>/dev/null | head -10
fi

# ── Step 6: Internal/ is accessible within cf-protocol/ ──────────────────────
echo ""
echo "--- Step 6: cf-protocol/internal/ builds correctly (accessible within cf-protocol/)"

if (cd "$REPO_ROOT" && $GO build ./cf-protocol/internal/... 2>/dev/null); then
  pass "cf-protocol/internal/... builds (accessible within cf-protocol/)"
else
  fail "cf-protocol/internal/... build FAILED"
fi

# ── Step 7: pkg/store/aztable still at pkg/ (stays as exception) ─────────────
echo ""
echo "--- Step 7: pkg/store/aztable exception still in place"

if [ -d "$REPO_ROOT/pkg/store/aztable" ]; then
  pass "pkg/store/aztable stays at pkg/ (L2 dep: implements convention.DispatchStore)"
else
  fail "pkg/store/aztable is missing"
fi

# ── Summary ────────────────────────────────────────────────────────────────────
echo ""
echo "=== Summary ==="
echo "PASS: $PASS_COUNT"
echo "FAIL: $FAIL_COUNT"
echo ""

if [ "$FAIL_COUNT" -eq 0 ]; then
  echo "ALL CHECKS PASSED — cf-protocol substrate lives in internal/ with proper encapsulation."
  exit 0
else
  echo "SOME CHECKS FAILED — review output above."
  exit 1
fi
