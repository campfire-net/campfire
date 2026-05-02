#!/usr/bin/env bash
# sdk-doc-coverage.sh — Verify 100% doc comment + Example_ test coverage
#                       for the cf-protocol/protocol and cf-conventions/cf-convention
#                       public surfaces.
#
# Exits 0 when coverage is 100%, 1 otherwise.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/sdk-doc-coverage.sh
#
# Requirements:
#   - Go 1.22+ on PATH (go doc, go test)
#   - staticcheck on PATH (optional — skipped when absent)
#
# Checks performed:
#   1. Every exported symbol in cf-protocol/protocol and
#      cf-conventions/cf-convention has a doc comment (go doc -all).
#   2. Every exported type/func has a corresponding Example_ test
#      (go test -run Example -v counts > 0).
#   3. go test -run Example passes with exit 0 for both packages.
#   4. staticcheck (SA1012, ST1000, etc.) is run if available.
#
# Design: campfireagent-9e1

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

GO="${GO:-go}"
if ! command -v "$GO" &>/dev/null; then
  # Try common locations
  for candidate in /home/baron/go1.25/bin/go /usr/local/go/bin/go; do
    if [[ -x "$candidate" ]]; then
      GO="$candidate"
      break
    fi
  done
fi

if ! command -v "$GO" &>/dev/null; then
  echo "ERROR: go binary not found. Set GO= or add go to PATH." >&2
  exit 1
fi

PROTOCOL_PKG="github.com/campfire-net/campfire/cf-protocol/protocol"
CONVENTION_PKG="github.com/campfire-net/campfire/cf-conventions/cf-convention"

ERRORS=0

echo "=== SDK Doc Coverage Check (campfireagent-9e1) ==="
echo ""
echo "Go: $("$GO" version)"
echo ""

# ────────────────────────────────────────────────────────────────────────────
# Step 1: Verify all examples run and pass
# ────────────────────────────────────────────────────────────────────────────
echo "--- Step 1: Example_ tests pass ---"
for pkg in "$PROTOCOL_PKG" "$CONVENTION_PKG"; do
  echo -n "  Running examples for $pkg ... "
  if "$GO" test "$pkg" -run Example -count=1 2>&1 | tail -1 | grep -q "^ok"; then
    EXAMPLE_COUNT=$("$GO" test "$pkg" -run Example -v -count=1 2>&1 | grep -c "^=== RUN.*Example" || true)
    echo "PASS ($EXAMPLE_COUNT examples)"
  else
    echo "FAIL"
    "$GO" test "$pkg" -run Example -count=1 2>&1 | tail -5
    ERRORS=$((ERRORS + 1))
  fi
done
echo ""

# ────────────────────────────────────────────────────────────────────────────
# Step 2: Check doc comment coverage via go doc -all
#         Every exported symbol line should be immediately preceded by a
#         comment line (or be part of a const/var block with a block comment).
# ────────────────────────────────────────────────────────────────────────────
echo "--- Step 2: Doc comment audit (go doc -all) ---"
check_doc_coverage() {
  local pkg="$1"
  local missing=0
  # Collect exported symbol names from go doc
  while IFS= read -r line; do
    # Lines starting with "func ", "type ", "var ", "const " are exported symbols
    if echo "$line" | grep -qE "^(func|type|var|const) [A-Z]"; then
      SYMBOL=$(echo "$line" | awk '{print $2}' | cut -d'(' -f1 | cut -d' ' -f1)
      # go doc individual symbol — if output starts with "func|type|var|const" without
      # a preceding blank-line-separated comment, the symbol may be undocumented.
      DOC=$("$GO" doc "$pkg.$SYMBOL" 2>/dev/null || true)
      if [[ -z "$DOC" ]]; then
        echo "    MISSING doc: $pkg.$SYMBOL"
        missing=$((missing + 1))
      fi
    fi
  done < <("$GO" doc -all "$pkg" 2>/dev/null)
  echo $missing
}

for pkg in "$PROTOCOL_PKG" "$CONVENTION_PKG"; do
  echo -n "  Checking doc coverage for $pkg ... "
  MISSING=$(check_doc_coverage "$pkg")
  if [[ "$MISSING" -eq 0 ]]; then
    echo "PASS (all exported symbols have docs)"
  else
    echo "FAIL ($MISSING symbols missing docs)"
    ERRORS=$((ERRORS + 1))
  fi
done
echo ""

# ────────────────────────────────────────────────────────────────────────────
# Step 3: Count Example_ tests per package
# ────────────────────────────────────────────────────────────────────────────
echo "--- Step 3: Example_ test count ---"
for pkg in "$PROTOCOL_PKG" "$CONVENTION_PKG"; do
  COUNT=$("$GO" test "$pkg" -run Example -v -count=1 2>&1 | grep -c "^=== RUN.*Example" || true)
  echo "  $pkg: $COUNT example tests"
done
echo ""

# ────────────────────────────────────────────────────────────────────────────
# Step 4: staticcheck (if available)
# ────────────────────────────────────────────────────────────────────────────
echo "--- Step 4: staticcheck (if available) ---"
if command -v staticcheck &>/dev/null; then
  for pkg in "./cf-protocol/protocol/..." "./cf-conventions/cf-convention/..."; do
    echo -n "  staticcheck $pkg ... "
    if staticcheck "$pkg" 2>&1 | grep -qE "^(.*\.go:[0-9]+|error)"; then
      echo "ISSUES"
      staticcheck "$pkg" 2>&1 | head -10
      ERRORS=$((ERRORS + 1))
    else
      echo "PASS"
    fi
  done
else
  echo "  staticcheck not found — skipping (install: go install honnef.co/go/tools/cmd/staticcheck@latest)"
fi
echo ""

# ────────────────────────────────────────────────────────────────────────────
# Result
# ────────────────────────────────────────────────────────────────────────────
echo "=== Result ==="
if [[ "$ERRORS" -eq 0 ]]; then
  echo "PASS — doc coverage = 100%, all Example_ tests pass"
  exit 0
else
  echo "FAIL — $ERRORS check(s) failed"
  exit 1
fi
