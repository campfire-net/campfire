#!/usr/bin/env bash
# 27-probe-timeout-disambiguation.sh — §11.3 probe timeout false-positive risk.
#
# Verifies that cf-discovery-spec.md §11.3 contains a subsection that disambiguates
# two distinct timeout failure modes:
#   (a) timeout-due-to-suppression  — campfire drops the probe (honeypot)
#   (b) timeout-due-to-latency      — probe accepted but read lags on high-latency network
#
# The two cases require different handling and MUST be explicitly documented.
# Without this disambiguation, a legitimate high-latency open campfire can trigger
# a false-positive unjoin-declaration.
#
# DONE conditions:
#   1. The spec file contains a §11.3.1 subsection (or equivalent) with both
#      "latency" and "suppression" keywords — confirming the two failure modes
#      are distinguished.
#   2. The existing §11.3 timeout language is preserved (the "5 seconds" default
#      mention still appears) — we did not accidentally delete the original text.
#
# Exit 0 = spec correctly disambiguates the two timeout failure modes.
# Exit 1 = spec is missing the disambiguation (pre-edit failure expected on HEAD).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SPEC="$REPO_ROOT/docs/cf-discovery-spec.md"

echo "=== Demo 26: §11.3 Probe Timeout Disambiguation ==="
echo ""
echo "Spec: $SPEC"
echo ""

FAIL=0

# --- Check 1: New §11.3.1 subsection distinguishes suppression vs latency ---
echo "Check 1: Spec contains disambiguation of 'suppression' vs 'latency' timeout failure modes"
if grep -qi "suppression" "$SPEC" && grep -qi "latency" "$SPEC"; then
    echo "  PASS: both keywords ('suppression', 'latency') found in spec"
else
    echo "  FAIL: spec is missing at least one keyword ('suppression' or 'latency')"
    echo "        §11.3.1 probe-timeout false-positive subsection is absent."
    FAIL=1
fi

# --- Check 2: §11.3.1 subsection header is present ---
echo ""
echo "Check 2: §11.3.1 subsection header is present"
if grep -q "11\.3\.1" "$SPEC"; then
    echo "  PASS: §11.3.1 subsection found"
else
    echo "  FAIL: §11.3.1 subsection header not found"
    FAIL=1
fi

# --- Check 3: Existing §11.3 timeout language preserved (5-second default) ---
echo ""
echo "Check 3: Existing §11.3 verification timeout language is preserved"
if grep -q "5 second" "$SPEC" || grep -q "5s" "$SPEC" || grep -q "5-second" "$SPEC"; then
    echo "  PASS: §11.3 original 5-second timeout reference preserved"
else
    echo "  FAIL: §11.3 original timeout language appears to have been removed"
    FAIL=1
fi

echo ""
if [ "$FAIL" -eq 0 ]; then
    echo "=== PASS: §11.3 correctly disambiguates suppression-timeout vs latency-timeout ==="
    exit 0
else
    echo "=== FAIL: §11.3 probe timeout disambiguation is missing or incomplete ==="
    exit 1
fi
