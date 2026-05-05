#!/usr/bin/env bash
# docs-cross-references-resolve.sh — Walk every *.md under docs/, cf-protocol/docs/,
#   cf-conventions/, and site/docs/ and verify that every internal relative link
#   target exists on disk.
#
# For each markdown file the script:
#   1. Extracts all markdown links of the form ](path) or ](path#anchor).
#   2. Skips external links (http:// / https://) — prints a note.
#   3. Skips pure anchor links (#anchor) — internal anchors are not verified
#      (headings can be spelled many ways; false positives outweigh signal).
#   4. Resolves relative paths against the directory of the source file.
#   5. Reports PASS / FAIL / SKIP per link; counts broken links.
#
# Exit codes:
#   0 — all relative links resolve to existing files.
#   1 — one or more relative links are broken.
#
# Excluded directories (archived / build artifacts):
#   site/releases/          — versioned HTML copies; links target external URLs
#   docs/specs/v0.19-*     — superseded historical records
#
# Usage (from repo root):
#   bash cf-conventions/demos/docs-cross-references-resolve.sh
#
# Design: campfireagent-285

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# ---------------------------------------------------------------------------
# Counters
# ---------------------------------------------------------------------------
TOTAL_DOCS=0
TOTAL_LINKS=0
BROKEN=0
EXTERNAL_SKIPPED=0
ANCHOR_SKIPPED=0

pass()     { echo "  PASS: $*"; }
fail()     { echo "  FAIL: $*" >&2; ((BROKEN++)) || true; }
note()     { echo "  NOTE: $*"; }

# ---------------------------------------------------------------------------
# Collect markdown files
# ---------------------------------------------------------------------------
# Search roots — listed explicitly so the script is transparent about scope.
SEARCH_ROOTS=(
  "$REPO_ROOT/docs"
  "$REPO_ROOT/cf-protocol/docs"
  "$REPO_ROOT/cf-conventions"
  "$REPO_ROOT/site/docs"
)

# Exclusion patterns (substring match on absolute path)
EXCLUDE_PATTERNS=(
  "/site/releases/"
  "/docs/specs/v0.19-"
)

is_excluded() {
  local path="$1"
  for pattern in "${EXCLUDE_PATTERNS[@]}"; do
    if [[ "$path" == *"$pattern"* ]]; then
      return 0
    fi
  done
  return 1
}

mapfile -t ALL_DOCS < <(
  for root in "${SEARCH_ROOTS[@]}"; do
    if [[ -d "$root" ]]; then
      find "$root" -name "*.md" -type f
    fi
  done | sort -u
)

# Apply exclusions
DOCS=()
for doc in "${ALL_DOCS[@]}"; do
  if ! is_excluded "$doc"; then
    DOCS+=("$doc")
  fi
done

echo "=== docs-cross-references-resolve ==="
echo ""
echo "Repo root : $REPO_ROOT"
echo "Docs found: ${#DOCS[@]} (after exclusions)"
echo ""

# ---------------------------------------------------------------------------
# Process each doc
# ---------------------------------------------------------------------------
process_doc() {
  local file="$1"
  local rel_file="${file#$REPO_ROOT/}"
  local dir
  dir="$(dirname "$file")"

  # Extract markdown links: ](target) or ](target#anchor)
  # Pattern covers:
  #   [text](path)
  #   [text](path#anchor)
  #   ![img](path)
  # We capture the path portion (before any #).
  # Two-pass filtering to avoid false positives:
  #   Pass 1 (awk): skip fenced code blocks (``` ... ```).
  #   Pass 2 (sed): strip inline code spans (`...`) so that patterns like
  #     `[text](path)` inside backticks are not matched as links.
  local link_targets
  link_targets=$(
    awk '
      /^```/ { in_code = !in_code; next }
      in_code { next }
      { print }
    ' "$file" \
    | sed 's/`[^`]*`//g' \
    | grep -oP '(?<=\])\(([^)]+)\)' \
    | sed 's/^(//; s/)$//' \
    | sed 's/#[^#]*$//'   \
    | sort -u
  ) || true

  if [[ -z "$link_targets" ]]; then
    echo "--- $rel_file (no links)"
    return
  fi

  local doc_pass=0
  local doc_fail=0
  local doc_skip=0

  echo "--- $rel_file"

  while IFS= read -r raw_target; do
    [[ -z "$raw_target" ]] && continue
    ((TOTAL_LINKS++)) || true

    # External link — skip
    if [[ "$raw_target" == http://* || "$raw_target" == https://* ]]; then
      note "external (skipped): $raw_target"
      ((EXTERNAL_SKIPPED++)) || true
      ((doc_skip++)) || true
      continue
    fi

    # HTML links — these are generated site pages, not markdown source files;
    # resolving them against the source tree would yield false positives.
    if [[ "$raw_target" == *.html || "$raw_target" == *.html#* ]]; then
      note "html (skipped — site asset): $raw_target"
      ((EXTERNAL_SKIPPED++)) || true
      ((doc_skip++)) || true
      continue
    fi

    # Pure anchor link (#foo) — skip
    if [[ "$raw_target" == \#* ]]; then
      note "anchor-only (skipped): $raw_target"
      ((ANCHOR_SKIPPED++)) || true
      ((doc_skip++)) || true
      continue
    fi

    # Resolve relative path from the doc's directory
    local resolved
    resolved="$(cd "$dir" && realpath -m "$raw_target" 2>/dev/null || echo "")"

    if [[ -z "$resolved" ]]; then
      fail "$rel_file → $raw_target  (could not resolve)"
      ((doc_fail++)) || true
      continue
    fi

    if [[ -f "$resolved" || -d "$resolved" ]]; then
      pass "$raw_target"
      ((doc_pass++)) || true
    else
      fail "$rel_file → $raw_target  (resolved: ${resolved#$REPO_ROOT/}) — FILE NOT FOUND"
      ((doc_fail++)) || true
    fi
  done <<< "$link_targets"

  echo "  [doc total: ${doc_pass} pass, ${doc_fail} fail, ${doc_skip} skipped]"
  echo ""
}

for doc in "${DOCS[@]}"; do
  ((TOTAL_DOCS++)) || true
  process_doc "$doc"
done

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo "=== Summary ==="
echo "Docs walked     : $TOTAL_DOCS"
echo "Links extracted : $TOTAL_LINKS"
echo "External skipped: $EXTERNAL_SKIPPED"
echo "Anchor skipped  : $ANCHOR_SKIPPED"
echo "Broken links    : $BROKEN"
echo ""

if [[ $BROKEN -gt 0 ]]; then
  echo "FAIL: $BROKEN broken link(s) found — cross-reference integrity check failed."
  exit 1
else
  echo "PASS: all relative links resolve — cross-reference integrity OK."
  exit 0
fi
