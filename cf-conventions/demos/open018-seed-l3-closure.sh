#!/usr/bin/env bash
# cf-conventions/demos/open018-seed-l3-closure.sh
#
# Demo: OPEN-018 carveout closure (campfireagent-c72).
#
# Proves that seed.go has been moved from L2 (cf-convention/) to L3
# (cf-convention-extensions/seed/) and that the L2 package no longer
# exports or depends on seed-declaration symbols.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/open018-seed-l3-closure.sh
#
# Requires: go on PATH.
# The script exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-}"
if [[ -z "$GO" ]]; then
  if command -v go &>/dev/null; then
    GO="go"
  elif [[ -x /usr/local/go/bin/go ]]; then
    GO="/usr/local/go/bin/go"
  else
    echo "ERROR: go binary not found" >&2
    exit 1
  fi
fi

echo "=== open018-seed-l3-closure.sh ==="
echo "Repo: $REPO_ROOT"
echo

# ── Case 1: L2 package does NOT depend on the L3 seed package ────────────────
echo "Case 1: cf-convention (L2) must NOT import cf-convention-extensions/seed (L3)..."

cd "$REPO_ROOT"
L2_DEPS=$("$GO" list -f '{{join .Deps "\n"}}' \
  github.com/campfire-net/campfire/cf-conventions/cf-convention)

SEED_PKG="github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/seed"
if echo "$L2_DEPS" | grep -qF "$SEED_PKG"; then
  echo "FAIL: cf-convention (L2) depends on cf-convention-extensions/seed (L3)."
  echo "  This is an OPEN-018 regression — seed.go is back in L2."
  exit 1
fi

echo "PASS: L2 does not import L3 seed package."
echo

# ── Case 2: L3 seed package sources exist ────────────────────────────────────
echo "Case 2: cf-convention-extensions/seed/seed.go must exist in L3..."

SEED_DIR="$REPO_ROOT/cf-conventions/cf-convention-extensions/seed"
if [[ ! -f "$SEED_DIR/seed.go" ]]; then
  echo "FAIL: $SEED_DIR/seed.go not found."
  exit 1
fi
echo "PASS: seed.go exists in L3 at $SEED_DIR"

# Verify key functions exist in the file.
for fn in PromoteDeclaration SupersedeDeclaration RevokeDeclaration NamingRegisterDeclaration InfrastructureSeedDeclarations; do
  if grep -q "^func $fn" "$SEED_DIR/seed.go"; then
    echo "  PASS: $fn declared in L3 seed.go"
  else
    echo "  FAIL: $fn not found in $SEED_DIR/seed.go"
    exit 1
  fi
done

echo

# ── Case 3: L2 convention package does NOT contain seed functions ─────────────
echo "Case 3: cf-convention (L2) must NOT contain seed declaration functions..."

L2_DIR="$REPO_ROOT/cf-conventions/cf-convention"
if [[ -f "$L2_DIR/seed.go" ]]; then
  echo "FAIL: $L2_DIR/seed.go still exists — OPEN-018 was NOT closed."
  exit 1
fi
echo "PASS: seed.go absent from L2 directory."

for fn in PromoteDeclaration SupersedeDeclaration RevokeDeclaration NamingRegisterDeclaration; do
  if grep -rq "^func $fn" "$L2_DIR/"; then
    echo "  FAIL: $fn found in L2 — seed declarations still in L2."
    exit 1
  else
    echo "  PASS: $fn absent from L2 package."
  fi
done

echo

# ── Case 4: ConventionRevokeTag is exported from L2 ──────────────────────────
echo "Case 4: ConventionRevokeTag must be exported from cf-convention (L2)..."
if grep -rq "ConventionRevokeTag" "$L2_DIR/parser.go"; then
  echo "PASS: ConventionRevokeTag is declared in L2 parser.go."
else
  echo "FAIL: ConventionRevokeTag not found in L2 parser.go."
  exit 1
fi

echo
echo "=== open018-seed-l3-closure.sh PASSED ==="
echo "All four assertions hold:"
echo "  1. L2 does not import L3 seed package."
echo "  2. Seed functions exported from L3 seed package."
echo "  3. Seed functions absent from L2 convention package."
echo "  4. ConventionRevokeTag exported from L2."
