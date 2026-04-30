#!/usr/bin/env bash
# cf-protocol/demos/wire-freeze-verify.sh
#
# Demo: Phase 8 Gate 4 — wire-format freeze snapshot verifier (campfireagent-3a8).
#
# This script runs the wire-format freeze snapshot verifier and confirms that
# every claim in docs/0.30-wire-format-freeze.md matches the actual code at HEAD.
#
# The verifier (cf-protocol/internal/wireverify/) walks these S0q snapshot claims:
#   §3.1  Message envelope CBOR field positions (antecedents at field 5)
#   §3.1  MessageSignInput covers id+payload+tags+antecedents+timestamp only
#   §3.2  ProvenanceHop CBOR field positions (Role at field 8, omitempty)
#   §3.2  HopSignInput covers all hop fields including Role
#   §1.4  Session + visibility-changed tag constants (session:open, session:close,
#          campfire:visibility-changed) correct in tagspec.go and protocol re-exports
#   §1.4  Pre-0.30 campfire: system event tags registered as constants
#   §1.5  Reserved-op floor: exactly the canonical 10 ops
#   §1.6  future / fulfills reserved tags frozen at cf-protocol 1.0
#   §1.8  Await ordering: earliest-timestamp-wins + lex-ID tiebreaker tags correct
#   §3.3  Capability CBOR field IDs 1–6 match cf-authority trust/grant.go
#   §3.4  GrantPayload CBOR field IDs 1–4 match cf-authority trust/grant.go
#   §3.5  WhereMatcher kind values (1=CampfireByID, 2=NamePrefix, 3=TagMatcher)
#   §2.2  PredicateAST leaf kind strings (6 leaf kinds)
#   §2.2  Absence of not: predicate (parse-time rejection)
#   §2.4  DenyReason enum: all 10 stable wire codes
#
# Adversarial:
#   TestWireVerifyMutationDetection — three mutation scenarios confirm verifier catches drift.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/wire-freeze-verify.sh
#
# Exits 0 when all claims match code. Exits 1 on any drift.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO=${GO:-/usr/local/go/bin/go}
PASS_COUNT=0
FAIL_COUNT=0

pass() { echo "PASS: $*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail() { echo "FAIL: $*" >&2; FAIL_COUNT=$((FAIL_COUNT+1)); }

echo "=== cf 0.30 Wire-Format Freeze Snapshot Verifier (campfireagent-3a8) ==="
echo "Repo:     $REPO_ROOT"
echo "Verifier: cf-protocol/internal/wireverify/"
echo ""

# ── Step 1: Snapshot file exists ──────────────────────────────────────────────
echo "--- Step 1: S0q snapshot file"

SNAPSHOT="${REPO_ROOT}/docs/0.30-wire-format-freeze.md"
if [[ -f "$SNAPSHOT" ]]; then
  pass "docs/0.30-wire-format-freeze.md present"
else
  fail "docs/0.30-wire-format-freeze.md MISSING — cannot verify"
  echo "DEMO EXIT 1 — snapshot file missing"
  exit 1
fi

# ── Step 2: Build verifier package ────────────────────────────────────────────
echo ""
echo "--- Step 2: Build cf-protocol/internal/wireverify"

if (cd "$REPO_ROOT" && $GO build ./cf-protocol/internal/... 2>&1); then
  pass "cf-protocol/internal/... builds (wireverify included)"
else
  fail "cf-protocol/internal/... build FAILED"
  FAIL_COUNT=$((FAIL_COUNT+1))
fi

# ── Step 3: Run verifier tests ────────────────────────────────────────────────
echo ""
echo "--- Step 3: Wire-format snapshot verifier (Go test)"

VERIFIER_OUT=$(cd "$REPO_ROOT" && $GO test ./cf-protocol/internal/wireverify/ \
  -v -count=1 2>&1)
VERIFIER_EXIT=$?

echo "$VERIFIER_OUT" | grep -E "^(=== RUN|--- PASS|--- FAIL|PASS|FAIL)" | \
  sed 's/^=== RUN   /  testing: /' | \
  sed 's/^--- /  /'

if [[ $VERIFIER_EXIT -eq 0 ]]; then
  PASSED=$(echo "$VERIFIER_OUT" | grep -c "^--- PASS" || true)
  pass "all $PASSED verifier assertions passed (spec === code at HEAD)"
else
  FAILED=$(echo "$VERIFIER_OUT" | grep -c "^--- FAIL" || true)
  fail "$FAILED verifier assertions FAILED — snapshot drift detected"
fi

# ── Step 4: Snapshot digest (change detection) ────────────────────────────────
echo ""
echo "--- Step 4: Snapshot digest (for change detection)"

if command -v sha256sum &>/dev/null; then
  sha256sum "$SNAPSHOT"
elif command -v shasum &>/dev/null; then
  shasum -a 256 "$SNAPSHOT"
else
  echo "[WARN] sha256sum/shasum not available"
fi

# ── Summary ────────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: ${PASS_COUNT} passed, ${FAIL_COUNT} failed ==="
echo ""

if [[ $FAIL_COUNT -eq 0 ]]; then
  echo "DEMO EXIT 0 — Phase 8 Gate 4 clear: wire-format snapshot matches code at HEAD."
  echo ""
  echo "Verifier: cf-protocol/internal/wireverify/wireverify_test.go"
  echo "Snapshot: docs/0.30-wire-format-freeze.md"
  echo "CI step:  .github/workflows/ci.yml (Wire-format freeze snapshot verifier)"
  exit 0
else
  echo "DEMO EXIT 1 — snapshot drift detected. Review test output above."
  exit 1
fi
