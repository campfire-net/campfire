#!/usr/bin/env bash
# run-all-showcases.sh — Run all four cf 0.30 local-shenanigans showcases.
#
# Runs each showcase sequentially. Each showcase is self-contained — it creates
# its own temp state and cleans up on exit. A single failure does NOT abort the
# remaining showcases; all four are run and a summary is printed at the end.
#
# Performance: binaries are built once and shared via build/ directory. A
# temporary empty CF_BEACON_DIR is exported so sub-showcases bypass the
# global ~/.cf/beacons scan (which can contain hundreds of thousands of files
# and accounts for 20–30 s per cf join invocation on active installs).
#
# Exit codes:
#   0 — all showcases passed
#   1 — one or more showcases failed
#
# Usage:
#   bash cf-conventions/demos/showcases/run-all-showcases.sh
#
# Run from the campfire repo root.

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
SHOWCASE_DIR="$REPO_ROOT/cf-conventions/demos/showcases"

# ------------------------------------------------------------------
# Pre-build all binaries once into build/ so each sub-showcase reuses
# the cached artifacts rather than rebuilding from scratch.
# ------------------------------------------------------------------
BUILD_DIR="$REPO_ROOT/build"
mkdir -p "$BUILD_DIR"

_need_build() {
  local bin="$BUILD_DIR/$1"
  # Rebuild if missing or older than any Go source file.
  [[ ! -x "$bin" ]] && return 0
  local newest_src
  newest_src=$(find "$REPO_ROOT" -name '*.go' -newer "$bin" -not -path '*/vendor/*' 2>/dev/null | head -1)
  [[ -n "$newest_src" ]] && return 0
  return 1
}

if _need_build cf || _need_build cf-mcp || _need_build cf-functions; then
  echo "[BUILD] Building cf, cf-mcp, cf-functions into $BUILD_DIR …"
  (cd "$REPO_ROOT" && \
    /usr/local/go/bin/go build -mod=mod -o "$BUILD_DIR/cf"           ./cmd/cf && \
    /usr/local/go/bin/go build -mod=mod -o "$BUILD_DIR/cf-mcp"       ./cmd/cf-mcp && \
    /usr/local/go/bin/go build -mod=mod -o "$BUILD_DIR/cf-functions" ./cmd/cf-functions 2>&1)
  echo "[BUILD] Done."
fi

# ------------------------------------------------------------------
# Use a fresh empty beacon dir so beacon.Scan() never touches the
# operator's ~/.cf/beacons (which can contain 100 K+ entries and
# costs 20+ s per join via Ed25519 batch-verify).
# ------------------------------------------------------------------
SHOWCASE_BEACON_DIR="$(mktemp -d)"
export CF_BEACON_DIR="$SHOWCASE_BEACON_DIR"
cleanup_beacon_dir() { rm -rf "$SHOWCASE_BEACON_DIR"; }
trap cleanup_beacon_dir EXIT

SHOWCASES=(
  "aietf-naming-root.sh"
  "multi-region-failover.sh"
  "cross-operator-namespace.sh"
  "hosted-reader-observer.sh"
)

PASS=0
FAIL=0
SKIPPED=0

echo "=========================================================="
echo " cf 0.30 local-shenanigans showcases"
echo "=========================================================="
echo ""

for showcase in "${SHOWCASES[@]}"; do
  script="$SHOWCASE_DIR/$showcase"
  echo "----------------------------------------------------------"
  echo " Running: $showcase"
  echo "----------------------------------------------------------"
  if [[ ! -f "$script" ]]; then
    echo "  [SKIP] $showcase not found at $script"
    SKIPPED=$((SKIPPED+1))
    continue
  fi

  if bash "$script"; then
    echo ""
    echo "  [SHOWCASE PASS] $showcase"
    PASS=$((PASS+1))
  else
    rc=$?
    echo ""
    echo "  [SHOWCASE FAIL] $showcase exited with code $rc"
    FAIL=$((FAIL+1))
  fi
  echo ""
done

echo "=========================================================="
echo " Results"
echo "=========================================================="
TOTAL=$((PASS + FAIL + SKIPPED))
echo "  Passed:  $PASS / $((PASS + FAIL))"
[[ $SKIPPED -gt 0 ]] && echo "  Skipped: $SKIPPED"
echo ""

if [[ $FAIL -gt 0 ]]; then
  echo "  FAIL: $FAIL showcase(s) failed."
  exit 1
fi

if [[ $SKIPPED -gt 0 && $PASS -eq 0 ]]; then
  echo "  All showcases were skipped — nothing to verify."
  exit 1
fi

echo "  All showcases passed."
exit 0
