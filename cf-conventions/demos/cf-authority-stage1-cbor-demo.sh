#!/usr/bin/env bash
# cf-authority-stage1-cbor-demo.sh
#
# Stage 1 demo for campfireagent-02a: GateEvaluator predicate parse + CBOR roundtrip.
#
# Verifies three spec conditions using the Go test binary directly:
#   (a) 'not:' AST node is rejected at parse time (§2.2)
#   (b) CBOR encode/decode roundtrip is byte-identical (§4.4 canonical encoding)
#   (c) Integer field IDs 1-6 are present in Capability CBOR output (§1.1)
#
# Uses real Go/CBOR tooling (github.com/fxamacker/cbor/v2) — NOT Python dict validation.
#
# Exit codes:
#   0 — all checks pass
#   1 — a check failed
#
# Usage (from campfire repo root):
#   bash cf-conventions/demos/cf-authority-stage1-cbor-demo.sh
#
# Run from the campfire repo root.

set -euo pipefail

PASS=0
FAIL=0

ok()  { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail(){ echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# ---------------------------------------------------------------------------
# §A — Go toolchain available
# ---------------------------------------------------------------------------
echo ""
echo "=== §A — Go toolchain ==="

if command -v go >/dev/null 2>&1; then
  GO_VERSION=$(go version)
  ok "Go available: $GO_VERSION"
else
  fail "Go not found on PATH; cannot run demo"
  echo "RESULT: 1 check failed."
  exit 1
fi

# ---------------------------------------------------------------------------
# §B — Package builds cleanly
# ---------------------------------------------------------------------------
echo ""
echo "=== §B — Package build ==="

PACKAGE="github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"

if go build "$PACKAGE" 2>/dev/null; then
  ok "cf-conventions/cf-authority/trust builds cleanly"
else
  fail "cf-conventions/cf-authority/trust failed to build"
  FAIL=$((FAIL + 1))
fi

# ---------------------------------------------------------------------------
# §C — Run Go tests for conditions (a), (b), (c)
# ---------------------------------------------------------------------------
echo ""
echo "=== §C — Go tests: condition (a) — 'not:' predicate rejected at parse time ==="

if go test -run "TestParsePredicateAST_NotRejected" \
    -v "$PACKAGE" 2>&1 \
    | grep -q "PASS: TestParsePredicateAST_NotRejected"; then
  ok "TestParsePredicateAST_NotRejected* passed"
elif go test -run "TestParsePredicateAST_NotRejected" \
    "$PACKAGE" 2>&1 | grep -q "^ok"; then
  ok "'not:' predicate rejection tests passed"
else
  # Run verbosely to show details
  echo ""
  go test -run "TestParsePredicateAST_NotRejected" -v "$PACKAGE" 2>&1 || true
  fail "'not:' predicate rejection tests failed"
fi

echo ""
echo "=== §C — Go tests: condition (b) — CBOR roundtrip byte-identical ==="

if go test -run "TestCapabilityCBORRoundtrip|TestCapabilityCBORDeterminism|TestGrantPayloadCBORRoundtrip" \
    "$PACKAGE" 2>&1 | grep -q "^ok"; then
  ok "CBOR roundtrip + determinism tests passed"
else
  echo ""
  go test -run "TestCapabilityCBORRoundtrip|TestCapabilityCBORDeterminism|TestGrantPayloadCBORRoundtrip" \
    -v "$PACKAGE" 2>&1 || true
  fail "CBOR roundtrip/determinism tests failed"
fi

echo ""
echo "=== §C — Go tests: condition (c) — CBOR integer field IDs 1-6 present ==="

if go test -run "TestCapabilityCBORFieldIDs" \
    "$PACKAGE" 2>&1 | grep -q "^ok"; then
  ok "TestCapabilityCBORFieldIDs passed"
else
  echo ""
  go test -run "TestCapabilityCBORFieldIDs" -v "$PACKAGE" 2>&1 || true
  fail "TestCapabilityCBORFieldIDs failed"
fi

# ---------------------------------------------------------------------------
# §D — Full test suite for the package
# ---------------------------------------------------------------------------
echo ""
echo "=== §D — Full package test suite ==="

if go test "$PACKAGE" 2>&1 | grep -q "^ok"; then
  TEST_COUNT=$(go test -v "$PACKAGE" 2>&1 | grep -c "^--- PASS" || echo "?")
  ok "All tests pass (${TEST_COUNT} test(s))"
else
  echo ""
  go test -v "$PACKAGE" 2>&1 || true
  fail "Package test suite failed"
fi

# ---------------------------------------------------------------------------
# §E — Verify CBOR field IDs match spec §1.1 integer layout
#
# We use a Go program to encode a sample Capability and verify the output
# contains the integer CBOR keys 1-6 in canonical order.
# ---------------------------------------------------------------------------
echo ""
echo "=== §E — CBOR field key canonical ordering (spec §1.1, §4.4) ==="

# Write a temporary Go program to print CBOR bytes as hex for inspection.
TMPDIR_DEMO=$(mktemp -d)
trap 'rm -rf "$TMPDIR_DEMO"' EXIT

cat > "$TMPDIR_DEMO/main.go" <<'GOEOF'
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

func main() {
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	cap := trust.Capability{
		Convention: "cf-authority-demo",
		OpPattern:  "evaluate",
		Where:      []trust.WhereMatcherCBOR{{Kind: 2, Prefix: "rd-"}},
		Bounds:     map[string]any{},
		Until:      1767225600000000000, // canonical t0
		Nonce:      nonce,
	}

	encoded, err := trust.MarshalCapabilityCBOR(cap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("CBOR bytes (%d bytes): %s\n", len(encoded), hex.EncodeToString(encoded))

	// Decode as raw map to verify keys.
	var raw map[int]any
	if err := trust.UnmarshalCBORRaw(encoded, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "decode error: %v\n", err)
		os.Exit(1)
	}

	// Print all integer keys found (must be 1,2,3,4,5,6).
	fmt.Printf("Field keys present:")
	for k := 1; k <= 6; k++ {
		if _, ok := raw[k]; ok {
			fmt.Printf(" %d", k)
		} else {
			fmt.Fprintf(os.Stderr, "\nFAIL: missing field key %d\n", k)
			os.Exit(1)
		}
	}
	fmt.Printf("\n")

	// Verify re-encode is byte-identical (canonical check).
	decoded, err := trust.UnmarshalCapabilityCBOR(encoded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unmarshal error: %v\n", err)
		os.Exit(1)
	}
	reEncoded, err := trust.MarshalCapabilityCBOR(decoded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "re-encode error: %v\n", err)
		os.Exit(1)
	}
	if hex.EncodeToString(encoded) != hex.EncodeToString(reEncoded) {
		fmt.Fprintf(os.Stderr, "FAIL: re-encode not byte-identical\n")
		fmt.Fprintf(os.Stderr, "  first:  %s\n", hex.EncodeToString(encoded))
		fmt.Fprintf(os.Stderr, "  second: %s\n", hex.EncodeToString(reEncoded))
		os.Exit(1)
	}
	fmt.Println("Re-encode: byte-identical (canonical)")

	fmt.Println("DEMO_OK")
}
GOEOF

cd "$REPO_ROOT"
OUTPUT=$(go run "$TMPDIR_DEMO/main.go" 2>&1) || {
  echo "  $OUTPUT"
  fail "CBOR field key verification program failed"
}

if echo "$OUTPUT" | grep -q "DEMO_OK"; then
  echo "  $OUTPUT" | grep -v "^$"
  ok "CBOR field keys 1-6 present; re-encode byte-identical"
else
  echo "  $OUTPUT"
  fail "CBOR field key verification program did not output DEMO_OK"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "================================================================"
TOTAL=$((PASS + FAIL))
echo "Results: $PASS/$TOTAL checks passed"
if [[ $FAIL -gt 0 ]]; then
  echo "FAILED: $FAIL check(s) failed"
  exit 1
else
  echo "ALL CHECKS PASSED — cf-authority Stage 1 CBOR + predicate demo complete"
  exit 0
fi
