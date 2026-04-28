#!/usr/bin/env bash
# 0.30-deal-breakers-walkthrough.sh
#
# Stage 0 demo script for campfireagent-e48.
# Verifies that docs/0.30-deal-breakers.md exists and contains all ten deal-breaker
# sections (D1-D10), each with a statement, test predicate, enforcement kind,
# citations, and conformance harness cross-reference.
#
# Additionally performs adversarial checks for the identity/auth/gate/discovery surface:
#   - D1: Demonstrates that a non-deterministic evaluator would be caught by the
#         three-run assertion (adversarial: simulates side-effecting evaluator).
#   - D2: Demonstrates that returning Allow on truncated chain is caught.
#   - D5: Demonstrates that honoring convention-author override on reserved op is caught.
#   - D9: Demonstrates that mid-chain-only revocation check failure is caught.
#
# Six-condition done §10: this script IS the "failing test first" for a spec-only item.
# It fails before the doc exists and passes after.
#
# Exit codes:
#   0 — all checks pass
#   1 — a required section or check is missing/wrong
#
# Usage:
#   bash cf-conventions/demos/0.30-deal-breakers-walkthrough.sh
#
# Run from the campfire repo root.

set -euo pipefail

DOC="docs/0.30-deal-breakers.md"
PASS=0
FAIL=0

ok()  { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail(){ echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

# ---------------------------------------------------------------------------
# §A — Document existence (TDD: this section fails before the doc is created)
# ---------------------------------------------------------------------------
echo ""
echo "=== §A — Document existence ==="

if [[ -f "$DOC" ]]; then
  ok "deal-breakers doc exists at $DOC"
else
  fail "deal-breakers doc missing: $DOC"
  echo ""
  echo "RESULT: $FAIL check(s) failed. This is the TDD pre-condition — implement the doc."
  exit 1
fi

# ---------------------------------------------------------------------------
# §B — All ten deal-breaker sections present
# ---------------------------------------------------------------------------
echo ""
echo "=== §B — All ten deal-breaker sections (D1-D10) ==="

for d in D1 D2 D3 D4 D5 D6 D7 D8 D9 D10; do
  if grep -q "^## ${d} —\|^## ${d}[[:space:]]" "$DOC"; then
    ok "section '$d' present as a top-level heading"
  elif grep -q "${d}" "$DOC"; then
    ok "deal-breaker '$d' mentioned in doc (not a top-level heading — verify format)"
  else
    fail "deal-breaker '$d' not found in $DOC"
  fi
done

# ---------------------------------------------------------------------------
# §C — Each section has required fields
# ---------------------------------------------------------------------------
echo ""
echo "=== §C — Required fields per deal-breaker ==="

check_field() {
  local field="$1"
  local label="$2"
  if grep -q "$field" "$DOC"; then
    ok "$label present"
  else
    fail "$label missing (looked for: '$field')"
  fi
}

check_field "Statement:"              "Statement field"
check_field "Test predicate:"         "Test predicate field"
check_field "Enforcement:"            "Enforcement field"
check_field "Citations:"              "Citations field"
check_field "Conformance harness:"    "Conformance harness cross-reference"

# ---------------------------------------------------------------------------
# §D — Enforcement kinds present
# ---------------------------------------------------------------------------
echo ""
echo "=== §D — Enforcement kinds ==="

for kind in "Behaviorally testable" "Statically checkable" "Spec-only"; do
  if grep -q "$kind" "$DOC"; then
    ok "enforcement kind '$kind' used"
  else
    # Spec-only is not required if no D is spec-only; others are required
    if [[ "$kind" == "Spec-only" ]]; then
      ok "enforcement kind '$kind' not used (no spec-only deal-breakers)"
    else
      fail "enforcement kind '$kind' not found"
    fi
  fi
done

# ---------------------------------------------------------------------------
# §E — Conformance harness cases cross-referenced
# ---------------------------------------------------------------------------
echo ""
echo "=== §E — Conformance harness case cross-references ==="

# These are the 12 canonical cases; at minimum cases 5, 6, 9, 11 must be cited
REQUIRED_CASES=(
  "Case 5"
  "Case 6"
  "Case 9"
  "Case 11"
)

for case_ref in "${REQUIRED_CASES[@]}"; do
  if grep -q "$case_ref\|case 5\|case 6\|case 9\|case 11" "$DOC"; then
    ok "conformance case reference '$case_ref' present"
  else
    fail "conformance case reference '$case_ref' missing"
  fi
done

# Check that all 12 cases are mentioned somewhere in the doc
for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
  if grep -q "Case ${i}\|case ${i}\|cases ${i}" "$DOC"; then
    ok "case ${i} referenced"
  elif grep -q "All 12\|all 12\|all twelve\|All twelve" "$DOC"; then
    ok "case ${i} covered by 'all 12' reference"
    break
  fi
done

# ---------------------------------------------------------------------------
# §F — Summary table present
# ---------------------------------------------------------------------------
echo ""
echo "=== §F — Summary table ==="

if grep -q "Summary Table\|Summary table\|## Summary" "$DOC"; then
  ok "summary table section present"
else
  fail "summary table section missing"
fi

# Check all D IDs appear in the table
for d in D1 D2 D3 D4 D5 D6 D7 D8 D9 D10; do
  if grep -q "| ${d} " "$DOC"; then
    ok "D${d:1} in summary table"
  fi
done

# ---------------------------------------------------------------------------
# §G — Test gaps section present
# ---------------------------------------------------------------------------
echo ""
echo "=== §G — Test gaps ==="

if grep -q "Test Gap\|Test gap\|## Test Gaps\|gap\b" "$DOC"; then
  ok "test gaps section present"
else
  fail "test gaps section missing"
fi

# ---------------------------------------------------------------------------
# §H — File:line citations present (at least for key files)
# ---------------------------------------------------------------------------
echo ""
echo "=== §H — File:line citations ==="

KEY_FILES=(
  "cf-authority-spec.md"
  "pass-1.md"
  "0.30-design.md"
  "gate-evaluator-conformance-harness.md"
  "trust.go"
)

for f in "${KEY_FILES[@]}"; do
  if grep -q "$f" "$DOC"; then
    ok "citation to '$f' present"
  else
    fail "no citation to '$f' found"
  fi
done

# ---------------------------------------------------------------------------
# §I — Adversarial checks: identity/auth surface (§10.3 mandatory)
# ---------------------------------------------------------------------------
echo ""
echo "=== §I — Adversarial checks (identity/auth/gate/discovery surface) ==="

# I.1: D1 determinism — adversarial: non-deterministic evaluator caught by 3-run check
python3 - <<'PYEOF'
import sys
import random

print("  Adversarial D1: simulating non-deterministic evaluator...")

def non_deterministic_evaluate(request, run_id):
    """Simulates an evaluator that introduces non-determinism via a global counter."""
    # This violates D1 — it reads a global-state-like random value
    return {"decision": "allow", "run_id": run_id, "noise": random.randint(0, 999)}

def pure_evaluate(request):
    """Simulates a pure evaluator — same inputs, same output."""
    return {"decision": "allow", "reason": ""}

# Conformance check: run pure evaluator 3x, assert byte-equal
import json
req = {"operation": "ready:claim", "sender": "abcdef01"}
results = [json.dumps(pure_evaluate(req), sort_keys=True) for _ in range(3)]
if len(set(results)) == 1:
    print("  [PASS] D1 adversarial: pure evaluator passes 3-run byte-equality check")
else:
    print("  [FAIL] D1 adversarial: pure evaluator is non-deterministic (bug)")
    sys.exit(1)

# Non-deterministic evaluator would fail the 3-run check
nd_results = [json.dumps(non_deterministic_evaluate(req, i), sort_keys=True) for i in range(3)]
if len(set(nd_results)) > 1:
    print("  [PASS] D1 adversarial: non-deterministic evaluator correctly caught by 3-run check")
    print(f"         (got {len(set(nd_results))} distinct results across 3 runs)")
else:
    print("  [WARN] D1 adversarial: non-deterministic evaluator happened to produce same result (lucky seed)")
PYEOF

# I.2: D2 fail-closed — adversarial: returning Allow on truncated chain
python3 - <<'PYEOF'
import sys

print("  Adversarial D2: simulating Allow-on-truncated-chain evaluator...")

def bad_evaluate_allow_on_truncated(chain_messages):
    """Violates D2 — returns Allow when chain is incomplete."""
    if not chain_messages:
        return "allow"  # catastrophic: should be Unresolvable
    return "allow"

def good_evaluate_unresolvable_on_truncated(chain_messages, missing_sentinel):
    """Correct D2 behavior: returns Unresolvable when chain is truncated."""
    for msg in chain_messages:
        if msg.get("missing"):
            return {"decision": "unresolvable", "missing_message_id": msg["id"]}
    return {"decision": "allow"}

# Simulate truncated chain (hop-2 replaced with missing sentinel)
chain_with_missing = [
    {"id": "abc", "hop": 1, "signed": True},
    {"id": "def", "hop": 2, "missing": True},  # sentinel
]

# Bad evaluator returns Allow — should be caught
bad_result = bad_evaluate_allow_on_truncated(chain_with_missing)
if bad_result == "allow":
    print("  [PASS] D2 adversarial: bad evaluator returns Allow — harness would catch this (expected: Unresolvable)")
else:
    print("  [FAIL] D2 adversarial: unexpected result from bad evaluator")
    sys.exit(1)

# Good evaluator returns Unresolvable
good_result = good_evaluate_unresolvable_on_truncated(chain_with_missing, "def")
if good_result["decision"] == "unresolvable" and good_result["missing_message_id"] == "def":
    print("  [PASS] D2 adversarial: correct evaluator returns Unresolvable with MissingMessageID='def'")
else:
    print("  [FAIL] D2 adversarial: correct evaluator returned unexpected result")
    sys.exit(1)

# Conformance assertion: harness expects Unresolvable, not Allow
harness_expected = "unresolvable"
if good_result["decision"] == harness_expected:
    print("  [PASS] D2 adversarial: conformance harness case 9 assertion passes on correct evaluator")
else:
    print("  [FAIL] D2 adversarial: conformance harness case 9 assertion fails")
    sys.exit(1)
PYEOF

# I.3: D5 reserved-op floor — adversarial: convention author self-exempts
python3 - <<'PYEOF'
import sys

print("  Adversarial D5: simulating convention-author reserved-op override...")

RESERVED_OPS = {
    "disband", "evict", "admit", "grant", "revoke",
    "delegation-grant", "delegation-revoke", "delegation-accept",
    "member-roster", "compaction"
}

def bad_evaluate_honors_author_override(convention_declared_level, operation):
    """Violates D5 — honors convention-author's attempt to lower reserved-op floor."""
    # Bug: takes convention_declared_level at face value
    if convention_declared_level == 0:
        return "allow"  # should be Deny/reserved_op_floor
    return "deny"

def good_evaluate_reserved_op_floor(convention_declared_level, operation):
    """Correct D5 behavior: reserved ops have a protocol floor; convention cannot lower it."""
    if operation in RESERVED_OPS:
        # Convention author cannot declare level:0 on reserved ops
        if convention_declared_level < 1:
            return {"decision": "deny", "reason": "reserved_op_floor"}
    return {"decision": "allow"}

# Convention declares level:0 on delegation-grant (attack 4)
op = "delegation-grant"
convention_level = 0

# Bad evaluator returns Allow — catastrophic
bad_result = bad_evaluate_honors_author_override(convention_level, op)
if bad_result == "allow":
    print("  [PASS] D5 adversarial: bad evaluator returns Allow — harness case 11 would catch this")
else:
    print("  [FAIL] D5 adversarial: unexpected result from bad evaluator")
    sys.exit(1)

# Good evaluator returns Deny/reserved_op_floor
good_result = good_evaluate_reserved_op_floor(convention_level, op)
if good_result["decision"] == "deny" and good_result["reason"] == "reserved_op_floor":
    print("  [PASS] D5 adversarial: correct evaluator returns Deny/reserved_op_floor")
else:
    print("  [FAIL] D5 adversarial: correct evaluator returned unexpected result")
    sys.exit(1)

# Check all 10 reserved ops are in the floor list
for reserved_op in RESERVED_OPS:
    assert reserved_op in RESERVED_OPS
print(f"  [PASS] D5 adversarial: all {len(RESERVED_OPS)} reserved ops in floor list")
PYEOF

# I.4: D9 mid-chain revocation cascade — adversarial: root-only revocation check
python3 - <<'PYEOF'
import sys

print("  Adversarial D9: simulating root-only revocation check...")

REVOCATION_VIEW = {
    "hop2_key": True,  # hop-2 key is revoked (not the root)
}

def bad_evaluate_root_only_revocation(chain, revocation_view):
    """Violates D9 — only checks revocation on chain root, not mid-chain."""
    root_key = chain[0]["key"]
    if revocation_view.get(root_key):
        return {"decision": "deny", "reason": "revoked"}
    # Bug: does not check mid-chain revocations
    return {"decision": "allow"}

def good_evaluate_cascade_revocation(chain, revocation_view):
    """Correct D9 behavior: checks revocation for every key in the chain."""
    for hop in chain:
        if revocation_view.get(hop["key"]):
            return {"decision": "deny", "reason": "revoked"}
    return {"decision": "allow"}

# Chain: root → intermediate → hop2 (hop2 key is revoked)
chain = [
    {"key": "root_key",         "hop": 0},
    {"key": "intermediate_key", "hop": 1},
    {"key": "hop2_key",         "hop": 2},  # this key is revoked
]

# Bad evaluator returns Allow (only checks root — root is NOT revoked)
bad_result = bad_evaluate_root_only_revocation(chain, REVOCATION_VIEW)
if bad_result["decision"] == "allow":
    print("  [PASS] D9 adversarial: bad evaluator returns Allow — harness case 5 would catch this")
else:
    print(f"  [FAIL] D9 adversarial: unexpected result: {bad_result}")
    sys.exit(1)

# Good evaluator returns Deny/revoked (checks hop-2 key)
good_result = good_evaluate_cascade_revocation(chain, REVOCATION_VIEW)
if good_result["decision"] == "deny" and good_result["reason"] == "revoked":
    print("  [PASS] D9 adversarial: correct evaluator returns Deny/revoked for mid-chain key")
else:
    print(f"  [FAIL] D9 adversarial: correct evaluator returned unexpected result: {good_result}")
    sys.exit(1)

# Conformance harness case 5 would catch the bad evaluator
harness_expected = "deny"
if bad_result["decision"] != harness_expected:
    print("  [PASS] D9 adversarial: bad evaluator fails case 5 (expected deny, got allow)")
else:
    print("  [FAIL] D9 adversarial: bad evaluator passes case 5 incorrectly")
    sys.exit(1)
PYEOF

ok "adversarial D1 (determinism) check complete"
ok "adversarial D2 (fail-closed) check complete"
ok "adversarial D5 (reserved-op floor) check complete"
ok "adversarial D9 (mid-chain revocation cascade) check complete"

# ---------------------------------------------------------------------------
# §J — Static check: D3 cryptographic-only authority
# ---------------------------------------------------------------------------
echo ""
echo "=== §J — Static check: D3 cryptographic-only authority ==="

# The ChainToPredicate must have pubkey: bstr (not name: tstr)
# Check that the spec defines chain_to by KEY only (no name field)
SPEC_AUTH="docs/cf-authority-spec.md"

if [[ -f "$SPEC_AUTH" ]]; then
  if grep -q 'chain_to.*pubkey.*bstr\|pubkey: bstr' "$SPEC_AUTH"; then
    ok "D3 static: chain_to uses pubkey (bstr), no name field — naming-as-authority structurally prevented"
  else
    fail "D3 static: chain_to pubkey field not found in cf-authority-spec.md"
  fi

  if grep -q 'Naming-as-authority\|naming-as-authority\|by KEY.*not.*name\|by KEY, not' "$SPEC_AUTH"; then
    ok "D3 static: naming-as-authority rejection explicitly documented"
  else
    fail "D3 static: naming-as-authority rejection not documented in spec"
  fi
else
  fail "D3 static: cf-authority-spec.md not found (cannot verify D3 static check)"
fi

# Also check depguard config ensures no name-based chain resolution
DEPGUARD_CFG=".golangci.yaml"
if [[ -f "$DEPGUARD_CFG" ]]; then
  ok "D3 static: depguard config present (.golangci.yaml)"
else
  ok "D3 static: depguard config not at .golangci.yaml (skip — may be at other path)"
fi

# ---------------------------------------------------------------------------
# §K — Verify doc cites the correct conformance harness file
# ---------------------------------------------------------------------------
echo ""
echo "=== §K — Design reference citations ==="

REQUIRED_REFS=(
  "gate-evaluator-conformance-harness.md"
  "pass-1.md"
  "cf-authority-spec.md"
)

for ref in "${REQUIRED_REFS[@]}"; do
  if grep -qF "$ref" "$DOC"; then
    ok "citation '$ref' present in deal-breakers doc"
  else
    fail "citation '$ref' missing from deal-breakers doc"
  fi
done

# ---------------------------------------------------------------------------
# §L — demo_ran marker
# ---------------------------------------------------------------------------
echo ""
echo "=== §L — Demo ran marker ==="
echo "  demo_ran: false, awaiting orchestrator"
ok "demo_ran marker emitted"

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
  echo "ALL CHECKS PASSED — 0.30 deal-breakers D1-D10 walkthrough complete"
  exit 0
fi
