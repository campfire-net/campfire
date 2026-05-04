#!/usr/bin/env bash
# 24-evict-rekey-lock.sh — FIX-1 (MB1): per-campfire write lock in rekeyAfterEvict.
#
# Exercises the lock contract via the Go test suite directly:
#   - TestEvictConcurrency/ConcurrentEvictEvict: two concurrent Evicts serialize
#   - TestEvictConcurrency/ConcurrentAdmitEvict: Admit+Evict serialize
#   - TestEvictConcurrency/AdmitLeaveEvictTriple: Admit+Leave+Evict triple serialize
#
# All tests run under -race (data race detector). Exit 0 = lock contract holds.
#
# This demo delegates to `go test` because the lock behavior is an in-process
# concurrency invariant — not observable via the cf CLI (which spawns separate
# processes with separate Client instances).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Ensure Go is on PATH
if ! command -v go &>/dev/null; then
    export PATH=/usr/local/go/bin:$PATH
fi

echo "=== Demo 24: Evict Rekey Lock (FIX-1/MB1) ==="
echo ""
echo "Test: per-campfire write lock serializes concurrent membership mutations"
echo "Scope: cf-protocol/protocol/evict_concurrency_test.go"
echo "Flags: -race -count=3"
echo ""

cd "$REPO_ROOT"

echo "--- Running TestEvictConcurrency under -race -count=3 ---"
if go test ./cf-protocol/protocol/... \
    -run "TestEvictConcurrency" \
    -race \
    -count=3 \
    -timeout 120s \
    -v 2>&1 | grep -E "^(=== RUN|--- (PASS|FAIL)|PASS|FAIL|ok )"; then
    echo ""
    echo "PASS: lock contract holds — no data races, serial outcome for all concurrent-evict cases"
    exit 0
else
    echo ""
    echo "FAIL: lock contract violation detected"
    exit 1
fi
