#!/usr/bin/env bash
# cf-binary-surface.sh — comprehensive demo: convention surface, argv[0], config overlay
#
# This is the top-level demo that exercises all three Stage 4 features:
#   1. Convention surface only: cf exposes convention ops, hides primitives
#   2. argv[0] dispatch: symlink-as-app-name routes to convention dispatch
#   3. Per-app config overlay: app-specific config at ~/.cf/apps/<app>/config.toml
#
# Runs the three sub-demos and exits 0 if all pass.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=== cf-binary-surface.sh ==="
echo "Running Stage 4 demos..."
echo ""

run_demo() {
    local name="$1"
    local script="$SCRIPT_DIR/$name"
    echo "--- $name ---"
    if bash "$script"; then
        echo "OK: $name"
    else
        echo "FAIL: $name"
        exit 1
    fi
    echo ""
}

run_demo "dispatch-convention.sh"
run_demo "argv0-namespace.sh"
run_demo "per-app-config-overlay.sh"

echo "=== cf-binary-surface.sh PASSED ==="
