#!/usr/bin/env bash
# Demo: §12 config-over-beacon endpoint precedence
#
# Shows that when ConfigTransportFunc returns a transport for a campfire ID,
# that config-declared transport is used instead of the beacon-advertised one.
#
# This demo runs the four adversarial test cases and verifies they all pass,
# demonstrating the implemented §12 behaviour.
#
# Prerequisites: Go toolchain at /usr/local/go/bin/go
# Usage: bash docs/demos/section12-config-over-beacon.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GO=/usr/local/go/bin/go

echo "=== §12 Config-over-beacon endpoint precedence demo ==="
echo ""
echo "Running TestAutoJoinPrefersConfigOverBeacon (cases A, B, C, D)..."
echo ""

cd "$REPO_ROOT"
$GO test ./pkg/naming/... -run TestAutoJoinPrefersConfigOverBeacon -v 2>&1 | grep -E "^(=== RUN|--- |PASS|FAIL|ok )" || true

# Capture exit code
$GO test ./pkg/naming/... -run TestAutoJoinPrefersConfigOverBeacon -count=1 >/dev/null 2>&1
STATUS=$?

echo ""
if [ $STATUS -eq 0 ]; then
    echo "✓ All §12 cases pass — config-over-beacon precedence is enforced."
    echo ""
    echo "  Case (a): config endpoint matches beacon   → config used (confirmed)"
    echo "  Case (b): config endpoint differs from beacon → config wins (§12 rule)"
    echo "  Case (c): only beacon, no config           → beacon used (existing behaviour preserved)"
    echo "  Case (d): only config, no beacon           → config used (beacon scan skipped)"
else
    echo "✗ Tests failed — see output above."
    exit 1
fi

echo ""
echo "Implementation: pkg/naming/client_transport.go::autoJoinViaClient"
echo "Spec:           docs/cf-discovery-spec.md §12 (Status: IMPLEMENTED)"
echo "Test:           pkg/naming/client_transport_test.go TestAutoJoinPrefersConfigOverBeacon_Case{A,B,C,D}"
