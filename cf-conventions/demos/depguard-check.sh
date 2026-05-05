#!/usr/bin/env bash
# cf-conventions/demos/depguard-check.sh
#
# Demo: depguard layer-boundary enforcement (campfireagent-231).
#
# Proves two things:
#   1. The current tree has zero depguard boundary violations (B1, B2, B3).
#   2. An intentional cross-layer import (L1 importing L2) IS caught and rejected.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/depguard-check.sh
#
# Requires: golangci-lint on PATH (or at ~/bin/golangci-lint).
# The script exits non-zero if either assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LINT="${GOLANGCI_LINT:-golangci-lint}"

# Allow ~/bin/golangci-lint if golangci-lint is not on PATH
if ! command -v golangci-lint &>/dev/null; then
  if [[ -x "$HOME/bin/golangci-lint" ]]; then
    LINT="$HOME/bin/golangci-lint"
  else
    echo "ERROR: golangci-lint not found. Install from https://golangci-lint.run/welcome/install/" >&2
    exit 1
  fi
fi

echo "=== depguard-check.sh ==="
echo "Repo:       $REPO_ROOT"
echo "Linter:     $($LINT version 2>&1 | head -1)"
echo

# ── Case 1: Current tree must be clean ────────────────────────────────────────
echo "Case 1: verify current tree has zero depguard violations..."
cd "$REPO_ROOT"

if "$LINT" run --fast-only 2>&1 | grep -q "depguard"; then
  echo "FAIL: depguard found unexpected violations in the current tree."
  "$LINT" run --fast-only 2>&1 | grep "depguard"
  exit 1
fi

echo "PASS: 0 depguard violations in current tree."
echo

# ── Case 2: Bad import IS rejected ────────────────────────────────────────────
echo "Case 2: verify an L1→L2 cross-layer import is caught and rejected..."

# Write a deliberately-bad Go file to a temp location.
# It imports pkg/convention from a path that matches the B1 rule (pkg/protocol/**).
BAD_DIR=$(mktemp -d)
BAD_FILE="$BAD_DIR/violation.go"
trap 'rm -rf "$BAD_DIR"' EXIT

cat > "$BAD_FILE" <<'GOEOF'
// DO NOT COMMIT: deliberate B1 boundary violation for depguard demo.
// This file is created in /tmp by depguard-check.sh and deleted immediately.
package depguard_violation_demo

import (
	// B1 violation: L1 (pkg/protocol context) importing L2 (pkg/convention).
	_ "github.com/campfire-net/campfire/pkg/convention"
)
GOEOF

# We can't lint a file outside the module directly via golangci-lint.
# Instead, verify the rule fires by temporarily placing the file in the repo,
# running depguard on only that path, then removing it.
#
# Note: directory names starting with _ are ignored by the Go toolchain.
# Use a plain name inside pkg/protocol.
TEMP_PKG="$REPO_ROOT/pkg/protocol/depguard-demo-violation"
mkdir -p "$TEMP_PKG"
cp "$BAD_FILE" "$TEMP_PKG/violation.go"
# Rewrite the package name so Go accepts the file
sed -i 's/package depguard_violation_demo/package protocol/' "$TEMP_PKG/violation.go"
trap 'rm -rf "$TEMP_PKG" "$BAD_DIR"' EXIT

lint_output=$("$LINT" run --fast-only ./pkg/protocol/depguard-demo-violation/... 2>&1 || true)

if echo "$lint_output" | grep -q "L1-no-convention\|depguard"; then
  echo "PASS: depguard correctly rejected the L1→L2 cross-layer import:"
  echo "$lint_output" | grep "depguard" | head -3 | sed 's/^/  /'
else
  echo "FAIL: depguard did NOT catch the L1→L2 violation. Output:"
  echo "$lint_output"
  exit 1
fi

echo
echo "=== depguard-check.sh PASSED ==="
echo "Both assertions hold:"
echo "  1. Current tree: 0 depguard violations."
echo "  2. Bad cross-layer import: caught and rejected."
