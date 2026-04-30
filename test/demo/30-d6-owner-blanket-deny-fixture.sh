#!/usr/bin/env bash
# Demo 30 — D6 Owner-Ceiling: BlanketDeny CBOR Conformance Fixtures (campfireagent-af2)
#
# Verifies that the two D6 conformance fixtures are well-formed CBOR:
#
#   testdata/chains/13-owner-blanket-deny.cbor       — case 13a (glob "ready:*")
#   testdata/chains/13b-owner-blanket-deny-exact.cbor — case 13b (exact "ready:claim")
#
# Each fixture encodes a GrantPayload with:
#   - convention="ready", op_pattern="claim"
#   - ChildPubkey: 32-byte Ed25519 public key (deterministic seed)
#   - Until: canonicalT0 + 24h (not expired at test time)
#   - Depth: 0 (owner-root grant)
#   - ParentGrantID: nil
#
# The fixture-validation tests (conformance/fixture_test.go) assert:
#   1. Integer field keys 1-4 present in GrantPayload (spec §1.2)
#   2. Integer field keys 1-6 present in Capability (spec §1.1)
#   3. Convention == "ready"
#   4. OpPattern == "claim"
#   5. Until >= canonical t0 (not pre-expired)
#   6. Nonce is 16 bytes
#   7. Depth == 0, ParentGrantID == nil
#   8. CBOR re-encode is byte-identical (§4.4 canonicalization)
#   9. Both fixtures decode to identical GrantPayloads (same grant chain)
#
# NO evaluator is called. This is a structural fixture check only.
# Stage 3 will add evaluator tests that USE these fixtures.
#
# Run from the campfire repo root:
#   bash test/demo/30-d6-owner-blanket-deny-fixture.sh
#
# Exit 0 on success, non-zero on failure.

set -euo pipefail

export PATH=/usr/local/go/bin:$PATH

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
FIXTURE_DIR="$REPO_ROOT/cf-conventions/cf-authority/trust/conformance/testdata/chains"
CONFORMANCE_PKG="github.com/campfire-net/campfire/cf-conventions/cf-authority/trust/conformance"

fail=0
pass=0

banner() {
  echo ""
  echo "=== $*"
}

check_pass() {
  echo "PASS: $1"
  ((pass++)) || true
}

check_fail() {
  echo "FAIL: $1"
  ((fail++)) || true
}

# ---------------------------------------------------------------------------
# 1. Fixture files exist and are non-empty
# ---------------------------------------------------------------------------

banner "Step 1: fixture files exist"

for fixture in "13-owner-blanket-deny.cbor" "13b-owner-blanket-deny-exact.cbor"; do
  path="$FIXTURE_DIR/$fixture"
  if [[ -f "$path" && -s "$path" ]]; then
    size=$(wc -c < "$path")
    echo "  $fixture — ${size} bytes"
    check_pass "fixture exists: $fixture"
  else
    echo "  MISSING or empty: $path"
    check_fail "fixture exists: $fixture"
  fi
done

# ---------------------------------------------------------------------------
# 2. Run fixture-validation Go tests (structural CBOR checks)
# ---------------------------------------------------------------------------

banner "Step 2: fixture-validation tests (no evaluator)"

cd "$REPO_ROOT"
if go test "$CONFORMANCE_PKG" \
    -run "TestD6_Fixture13a_GlobPattern_Wellformed|TestD6_Fixture13b_ExactMatch_Wellformed|TestD6_Fixtures13a_13b_SameGrantChain" \
    -count=1 \
    -timeout 60s \
    -v 2>&1 | grep -E "^(=== RUN|--- (PASS|FAIL)|PASS|FAIL|ok)"; then
  check_pass "fixture-validation tests"
else
  check_fail "fixture-validation tests"
fi

# ---------------------------------------------------------------------------
# 3. Grep-check: CBOR field structure (decode via go tool)
# ---------------------------------------------------------------------------

banner "Step 3: field structure spot-check (generator round-trip)"

# Re-run the generator and verify fixtures are still byte-identical.
# This confirms the fixture contents are deterministic.
TMPDIR_FIXTURES="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_FIXTURES"' EXIT

# Copy existing fixtures for comparison.
cp "$FIXTURE_DIR/13-owner-blanket-deny.cbor" "$TMPDIR_FIXTURES/13-orig.cbor"
cp "$FIXTURE_DIR/13b-owner-blanket-deny-exact.cbor" "$TMPDIR_FIXTURES/13b-orig.cbor"

# Re-generate fixtures.
(cd "$REPO_ROOT/cf-conventions/cf-authority/trust/conformance" && \
  go run ./gen/) 2>&1 | grep -E "^(wrote|done)"

# Compare byte-for-byte.
if diff -q "$FIXTURE_DIR/13-owner-blanket-deny.cbor" "$TMPDIR_FIXTURES/13-orig.cbor" > /dev/null 2>&1; then
  echo "  13-owner-blanket-deny.cbor: byte-identical on re-generation"
  check_pass "13a fixture deterministic"
else
  echo "  13-owner-blanket-deny.cbor: NOT byte-identical on re-generation (CBOR drift)"
  check_fail "13a fixture deterministic"
fi

if diff -q "$FIXTURE_DIR/13b-owner-blanket-deny-exact.cbor" "$TMPDIR_FIXTURES/13b-orig.cbor" > /dev/null 2>&1; then
  echo "  13b-owner-blanket-deny-exact.cbor: byte-identical on re-generation"
  check_pass "13b fixture deterministic"
else
  echo "  13b-owner-blanket-deny-exact.cbor: NOT byte-identical on re-generation (CBOR drift)"
  check_fail "13b fixture deterministic"
fi

# ---------------------------------------------------------------------------
# 4. Full cf-authority test suite (regression guard)
# ---------------------------------------------------------------------------

banner "Step 4: full cf-authority regression suite"

if go test ./cf-conventions/cf-authority/... -count=1 -timeout 120s 2>&1 | \
    grep -E "^(ok|FAIL|---)" | head -20; then
  check_pass "cf-authority regression suite"
else
  check_fail "cf-authority regression suite"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

banner "Summary"
echo "  PASS: $pass"
echo "  FAIL: $fail"
echo ""

if [[ $fail -gt 0 ]]; then
  echo "DEMO FAILED ($fail failure(s))"
  exit 1
fi

echo "DEMO PASSED — D6 BlanketDeny CBOR fixtures are well-formed and spec-compliant."
exit 0
