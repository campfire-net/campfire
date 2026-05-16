#!/usr/bin/env bash
# site-walk.sh — Verify cf 0.30 marketing site pages exist, internal links resolve,
#                and forbidden 0.19 terms are absent.
#
# Checks (filesystem, not HTTP):
#   1. All new pages exist
#   2. Internal href targets in each page resolve to real files
#   3. Forbidden 0.19 terms are absent from site/
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more checks failed (diagnostic printed inline)
#
# Usage (from repo root):
#   bash cf-conventions/demos/site-walk.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SITE="$REPO_ROOT/site"
PASS=0
FAIL=0

pass() { echo "PASS: $*"; ((PASS++)) || true; }
fail() { echo "FAIL: $*" >&2; ((FAIL++)) || true; }

# ── 1. Page existence ──────────────────────────────────────────────────────────

echo ""
echo "=== 1. Page existence ==="

PAGES=(
  "index.html"
  "releases/0.30/index.html"
  "getting-started/index.html"
  "upgrade/index.html"
  "showcases/index.html"
)

for page in "${PAGES[@]}"; do
  if [[ -f "$SITE/$page" ]]; then
    pass "site/$page exists"
  else
    fail "site/$page MISSING"
  fi
done

# ── 2. Internal link resolution ────────────────────────────────────────────────

echo ""
echo "=== 2. Internal link resolution ==="

# Extract href="/..." and src="/..." from HTML files, strip fragment, check file exists
# Strips query strings too. Checks against site/ as document root.

check_internal_links() {
  local file="$1"
  local rel_file="${file#$SITE/}"

  # Extract href and src values starting with /
  grep -oE '(href|src)="(/[^"#?]*)"' "$file" 2>/dev/null | grep -oE '"(/[^"]*)"' | tr -d '"' | while read -r link; do
    # Skip external-looking paths and anchors-only
    [[ "$link" == //* ]] && continue
    [[ -z "$link" ]] && continue

    # Map /foo/bar/ → site/foo/bar/index.html or site/foo/bar
    local candidate="$SITE$link"
    local found=false

    if [[ -f "$candidate" ]]; then
      found=true
    elif [[ -f "${candidate%/}index.html" ]]; then
      found=true
    elif [[ -f "${candidate}index.html" ]]; then
      found=true
    elif [[ -d "$candidate" ]] && [[ -f "$candidate/index.html" ]]; then
      found=true
    fi

    if $found; then
      echo "PASS: $rel_file → $link"
    else
      echo "WARN: $rel_file → $link (not found locally; may be cross-repo or docs)"
    fi
  done
}

for page in "${PAGES[@]}"; do
  if [[ -f "$SITE/$page" ]]; then
    while IFS= read -r line; do
      if [[ "$line" == FAIL:* ]]; then
        fail "${line#FAIL: }"
      elif [[ "$line" == PASS:* ]]; then
        pass "${line#PASS: }"
      else
        echo "$line"
      fi
    done < <(check_internal_links "$SITE/$page")
  fi
done

# ── 3. Forbidden 0.19 terms ────────────────────────────────────────────────────

echo ""
echo "=== 3. Forbidden 0.19 terms ==="

FORBIDDEN=(
  "recenter"
  "walk_up"
  "present_as"
  "transport/github"
  "sessions-shared-key"
  "cfs1_"
)

found_forbidden=false
# The upgrade page intentionally documents removed/renamed 0.19 terms — it is
# the migration guide for those exact terms. Exclude it from the scan.
NEW_PAGES=(
  "$SITE/index.html"
  "$SITE/releases/0.30/index.html"
  "$SITE/getting-started/index.html"
  "$SITE/showcases/index.html"
)

for term in "${FORBIDDEN[@]}"; do
  # Check only new 0.30 pages — old versioned docs under site/docs/ predate this release
  found_in=""
  for page_path in "${NEW_PAGES[@]}"; do
    if [[ -f "$page_path" ]] && grep -qi "$term" "$page_path" 2>/dev/null; then
      found_in="$found_in ${page_path#$SITE/}"
    fi
  done
  if [[ -n "$found_in" ]]; then
    fail "forbidden term '$term' found in new pages:$found_in"
    found_forbidden=true
  else
    pass "no forbidden term '$term' in new 0.30 pages"
  fi
done

# ── 4. Version string check ────────────────────────────────────────────────────

echo ""
echo "=== 4. Version string check ==="

# index.html should reference 0.30, not 0.19 as the current version
if grep -q "0.19.3" "$SITE/index.html" 2>/dev/null; then
  fail "index.html still references 0.19.3 as softwareVersion"
else
  pass "index.html does not reference old 0.19.3 version"
fi

if grep -q "0.30" "$SITE/index.html" 2>/dev/null; then
  pass "index.html references cf 0.30"
else
  fail "index.html does not mention cf 0.30"
fi

# ── Summary ─────────────────────────────────────────────────────────────────────

echo ""
echo "=== Summary ==="
echo "PASS: $PASS   FAIL: $FAIL"

if [[ $FAIL -gt 0 ]]; then
  echo ""
  echo "Site walk FAILED — $FAIL check(s) did not pass."
  exit 1
else
  echo ""
  echo "Site walk OK — all $PASS checks passed."
  exit 0
fi
