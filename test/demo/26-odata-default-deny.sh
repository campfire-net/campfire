#!/usr/bin/env bash
# demo/26-odata-default-deny.sh
#
# Demonstrates that casMockTable.evalODataPrimitive defaults to deny (false)
# on unsupported OData filter expressions instead of silently returning all rows.
#
# This is a Go-level property verified by TestODataFilter_UnsupportedExpr.
# The demo runs the test in verbose mode and confirms expected sub-test outcomes.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
export PATH=/usr/local/go/bin:$PATH

echo "=== demo/26-odata-default-deny.sh ==="
echo "Verifying evalODataPrimitive default-deny for unsupported OData filter expressions"
echo ""

cd "$REPO_ROOT"

# Run only the new filter test.
go test ./pkg/store/aztable/... -v -run "TestODataFilter_UnsupportedExpr" -count=1 2>&1

echo ""
echo "All sub-tests passed: unsupported filters return 0 rows (default-deny)."
echo "Supported filter 'Status eq' still returns matching rows (no regression)."
echo ""
echo "PASS"
