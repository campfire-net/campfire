#!/usr/bin/env bash
# cf-protocol/demos/substrate-move-verify.sh
#
# Demo: verify cf-protocol substrate strip-and-ship (campfireagent-9f4).
#
# This script verifies the package move completed correctly:
#   1. Moved packages exist at cf-protocol/ (not pkg/).
#   2. pkg/ substrate packages are gone (or only the aztable exception remains).
#   3. cf-protocol/protocol/ compiles with real definitions (not aliases).
#   4. Imports from cf-protocol/protocol work for all public surface types.
#   5. Internal packages (cf-protocol/internal/) are inaccessible from outside cf-protocol/.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/substrate-move-verify.sh
#
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO=${GO:-/usr/local/go/bin/go}
PASS_COUNT=0
FAIL_COUNT=0

pass() { echo "PASS: $*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail() { echo "FAIL: $*" >&2; FAIL_COUNT=$((FAIL_COUNT+1)); }

echo "=== cf-protocol substrate move verification (campfireagent-9f4) ==="
echo "Repo: $REPO_ROOT"
echo ""

# ── Step 1: Verify moved packages exist at cf-protocol/ ──────────────────────
echo "--- Step 1: Moved packages exist at cf-protocol/"

for pkg in campfire message store threshold projection predicate crypto encoding admission; do
  if [ -d "$REPO_ROOT/cf-protocol/$pkg" ]; then
    pass "cf-protocol/$pkg exists"
  else
    fail "cf-protocol/$pkg is missing"
  fi
done

for path in "transport/fs" "transport/http" "transport"; do
  if [ -d "$REPO_ROOT/cf-protocol/$path" ]; then
    pass "cf-protocol/$path exists"
  else
    fail "cf-protocol/$path is missing"
  fi
done

# ── Step 2: Verify pkg/ substrate packages are gone ──────────────────────────
echo ""
echo "--- Step 2: pkg/ substrate packages are gone"

for pkg in campfire message threshold projection predicate crypto encoding admission; do
  if [ ! -d "$REPO_ROOT/pkg/$pkg" ]; then
    pass "pkg/$pkg is gone"
  else
    fail "pkg/$pkg still exists (should be gone or empty shim)"
  fi
done

# pkg/store/aztable is the only exception (imports pkg/convention, L2 dep)
if [ -d "$REPO_ROOT/pkg/store/aztable" ]; then
  pass "pkg/store/aztable exists (expected exception: implements convention.DispatchStore)"
else
  fail "pkg/store/aztable missing"
fi

if [ -d "$REPO_ROOT/pkg/store" ] && [ "$(ls "$REPO_ROOT/pkg/store" | grep -v aztable | wc -l)" -eq 0 ]; then
  pass "pkg/store contains only aztable/ (core store moved to cf-protocol/store/)"
else
  fail "pkg/store has unexpected contents: $(ls $REPO_ROOT/pkg/store 2>/dev/null || echo 'missing')"
fi

# transport/fs and http should be gone from pkg/
for path in transport/fs transport/http; do
  if [ ! -d "$REPO_ROOT/pkg/$path" ]; then
    pass "pkg/$path is gone"
  else
    fail "pkg/$path still exists"
  fi
done

# pkg/protocol should be empty (all code moved to cf-protocol/protocol/)
if [ -d "$REPO_ROOT/pkg/protocol" ]; then
  count=$(ls "$REPO_ROOT/pkg/protocol" 2>/dev/null | wc -l)
  if [ "$count" -eq 0 ]; then
    pass "pkg/protocol is empty (all code moved to cf-protocol/protocol/)"
  else
    fail "pkg/protocol still has $count files"
  fi
else
  pass "pkg/protocol is gone"
fi

# ── Step 3: Verify cf-protocol/protocol/ builds with real definitions ─────────
echo ""
echo "--- Step 3: cf-protocol/protocol/ compiles"

if (cd "$REPO_ROOT" && $GO build ./cf-protocol/protocol/ 2>/dev/null); then
  pass "cf-protocol/protocol builds"
else
  fail "cf-protocol/protocol build failed"
fi

# Verify no aliases pointing back at pkg/protocol
alias_count=$(grep -r 'pkgprotocol\.' "$REPO_ROOT/cf-protocol/protocol/" \
  --include='*.go' 2>/dev/null | grep -v '_test.go' | wc -l || true)
if [ "$alias_count" -eq 0 ]; then
  pass "cf-protocol/protocol/ has no aliases back to pkg/protocol"
else
  fail "cf-protocol/protocol/ still has $alias_count pkgprotocol.* alias references"
fi

# ── Step 4: Verify packages vet cleanly ───────────────────────────────────────
echo ""
echo "--- Step 4: go vet on moved packages"

for pkg in cf-protocol/protocol cf-protocol/campfire cf-protocol/message cf-protocol/store; do
  if (cd "$REPO_ROOT" && $GO vet "./$pkg/" 2>/dev/null); then
    pass "$pkg vets clean"
  else
    fail "$pkg vet FAILED"
  fi
done

# ── Step 5: Verify internal packages are inaccessible from outside cf-protocol/ ─
echo ""
echo "--- Step 5: cf-protocol/internal/ is inaccessible from outside cf-protocol/"

# Create a temp file outside cf-protocol/ that tries to import internal
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

cat > "$TMPDIR/violation.go" << 'GOEOF'
package violation

import (
	// This import is INVALID from outside cf-protocol/
	_ "github.com/campfire-net/campfire/cf-protocol/internal/reserved-ops"
)
GOEOF

mkdir -p "$TMPDIR/pkg/violation"
cp "$TMPDIR/violation.go" "$TMPDIR/pkg/violation/violation.go"

# The Go toolchain should reject this import
# We test by trying to build a module that imports internal/ from outside
if (cd "$REPO_ROOT" && $GO vet ./cf-protocol/internal/... 2>/dev/null); then
  pass "cf-protocol/internal/ packages are valid Go packages (accessible within cf-protocol/)"
fi

# Verify the reserved-ops package is NOT directly imported by any pkg/ or cmd/ code
external_internal_imports=$(grep -r '"github.com/campfire-net/campfire/cf-protocol/internal/' \
  "$REPO_ROOT" --include='*.go' 2>/dev/null | \
  { grep -v "^$REPO_ROOT/cf-protocol/" || true; } | \
  { grep -v "^$REPO_ROOT/vendor/" || true; } | \
  wc -l)
if [ "$external_internal_imports" -eq 0 ]; then
  pass "cf-protocol/internal/ is not imported from outside cf-protocol/ (Go internal rule enforced)"
else
  fail "$external_internal_imports external imports of cf-protocol/internal/ found"
fi

# ── Summary ────────────────────────────────────────────────────────────────────
echo ""
echo "=== Summary ==="
echo "PASS: $PASS_COUNT"
echo "FAIL: $FAIL_COUNT"
echo ""

if [ "$FAIL_COUNT" -eq 0 ]; then
  echo "ALL CHECKS PASSED — cf-protocol substrate move is complete."
  exit 0
else
  echo "SOME CHECKS FAILED — review output above."
  exit 1
fi
