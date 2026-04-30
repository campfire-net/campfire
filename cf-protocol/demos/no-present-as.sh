#!/usr/bin/env bash
# no-present-as.sh — verifies present_as is fully removed from all non-test Go sources.
# Zero grep hits = pass. Any hit = fail.
#
# Requirement: campfireagent-703 Stage 1 done condition.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

echo "Checking for present_as / PresentAs / presentAs in non-test Go files..."

hits=$(grep -rn "present_as\|PresentAs\|presentAs" \
  --include="*.go" \
  "${REPO_ROOT}" \
  | grep -v "_test\.go" \
  | grep -v "/vendor/" \
  | grep -v "\.git/" \
  || true)

if [ -n "$hits" ]; then
  echo "FAIL: present_as still present in non-test Go files:"
  echo "$hits"
  exit 1
fi

echo "PASS: zero present_as hits in non-test Go files."
