#!/usr/bin/env bash
# conformance-ci-check.sh — Run the GateEvaluator conformance harness locally.
#
# Mirrors the CI step from .github/workflows/ci.yml (campfireagent-1f8).
# Run this to confirm the harness produces exit 0 before pushing.
#
# Usage: ./cf-conventions/demos/cf-authority/conformance-ci-check.sh
#        (run from the repo root)
#
# What it runs:
#   go test -count=3 ./cf-conventions/cf-authority/trust/conformance/...
#
# Expected output:
#   ok  github.com/campfire-net/campfire/cf-conventions/cf-authority/trust/conformance
#   (3 runs, all 12 cases pass, determinism verified)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

echo "==> GateEvaluator conformance harness (12 cases × 3 determinism runs)"
echo "    Repo: ${REPO_ROOT}"
echo "    Package: ./cf-conventions/cf-authority/trust/conformance/..."
echo ""

cd "${REPO_ROOT}"

go test -count=3 -v ./cf-conventions/cf-authority/trust/conformance/...

echo ""
echo "==> PASS — all 12 conformance cases passed with -count=3 determinism"
