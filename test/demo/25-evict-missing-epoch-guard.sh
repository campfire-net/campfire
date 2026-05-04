#!/usr/bin/env bash
# 25-evict-missing-epoch-guard.sh — FIX-2 (MB2): epoch_secrets guard in rekeyAfterEvict.
#
# Exercises the epoch_secrets guard via the Go test suite directly:
#   - MB2-1 (MissingEpochSecretsReturnsError): encrypted campfire, absent epoch_secrets →
#     Evict returns an error (not silent divergence).
#   - MB2-2 (PresentEpochSecretsSucceeds): encrypted campfire, present epoch_secrets →
#     Evict succeeds (guard does not block normal path).
#   - MB2-3 (PartialCrashMissingEpochSecrets): simulated crash clears epoch_secrets →
#     subsequent Evict returns an error.
#   - MB2-4 (ConcurrentEvictOneCallerMissingEpochSecrets): concurrent Evict; one caller
#     has epoch_secrets (succeeds), one does not (returns error).
#
# All tests run under -race. Exit 0 = guard contract holds.
#
# The guard is implemented in pkg/protocol/evict.go:rekeyAfterEvict.
# This demo delegates to `go test` because the guard behavior is an in-process
# store-check invariant — not directly observable via the cf CLI.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Ensure Go is on PATH
if ! command -v go &>/dev/null; then
    export PATH=/usr/local/go/bin:$PATH
fi

echo "=== Demo 25: Evict Missing Epoch Guard (FIX-2/MB2) ==="
echo ""
echo "Test: rekeyAfterEvict errors on encrypted campfire with absent epoch_secrets"
echo "Scope: cf-protocol/protocol/evict_missing_epoch_test.go"
echo "Flags: -race -count=1"
echo ""

cd "$REPO_ROOT"

echo "--- Running TestEvictMissingEpochSecrets under -race ---"
if go test ./cf-protocol/protocol/... \
    -run "TestEvictMissingEpochSecrets" \
    -race \
    -count=1 \
    -timeout 120s \
    -v 2>&1 | grep -E "^(=== RUN|--- (PASS|FAIL)|PASS|FAIL|ok )"; then
    echo ""
    echo "PASS: epoch_secrets guard holds — encrypted campfire with absent epoch_secrets"
    echo "      returns an error; encrypted campfire with present epoch_secrets succeeds"
    exit 0
else
    echo ""
    echo "FAIL: epoch_secrets guard violation detected"
    exit 1
fi
