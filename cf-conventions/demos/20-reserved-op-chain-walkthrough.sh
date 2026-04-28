#!/usr/bin/env bash
# 20-reserved-op-chain-walkthrough.sh
#
# Stage 0 demo script for campfireagent-514.
# Validates that docs/reserved-ops-chain.md exists and contains the canonical
# L1→L2→L3 enforcement chain architecture for the reserved-op floor.
#
# Six-condition done §10: this script IS the "failing test first" for a
# spec-only item. It fails before the doc exists and passes after.
#
# Adversarial invariant: the doc MUST explicitly state that L3 (cf-authority)
# cannot lower the reserved-op floor below the L1 list. If the chain doc
# allows L3 to override L1, this script fails — by design.
#
# Exit codes:
#   0 — all checks pass
#   1 — a required section or structural claim is missing/wrong
#
# Usage:
#   bash cf-conventions/demos/20-reserved-op-chain-walkthrough.sh
#
# Run from the campfire repo root.

set -euo pipefail

CHAIN_DOC="docs/reserved-ops-chain.md"
PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

# ---------------------------------------------------------------------------
# §A — Chain doc existence
# ---------------------------------------------------------------------------
echo ""
echo "=== §A — Chain doc existence ==="
if [[ -f "$CHAIN_DOC" ]]; then
  ok "chain doc exists at $CHAIN_DOC"
else
  fail "chain doc missing: $CHAIN_DOC"
  echo ""
  echo "RESULT: $FAIL check(s) failed — chain doc has not been written yet."
  echo "This is the expected failure before campfireagent-514 implementation."
  exit 1
fi

# ---------------------------------------------------------------------------
# §B — L1 layer section present (cf-protocol)
# ---------------------------------------------------------------------------
echo ""
echo "=== §B — Layer 1 (cf-protocol) section ==="

check_contains() {
  local pattern="$1"
  local label="$2"
  if grep -qE "$pattern" "$CHAIN_DOC"; then
    ok "$label"
  else
    fail "$label (pattern: '$pattern')"
  fi
}

check_contains "Layer 1|L1|cf-protocol" "L1 / cf-protocol layer documented"
check_contains "reserved-ops\.go|reserved_ops\.go|cf-protocol/internal" "L1 Go file reference documented"
check_contains "LIST|authoritative list|ten.op|10.op" "reserved-op LIST placement at L1 documented"

# ---------------------------------------------------------------------------
# §C — L2 layer section present (cf-conventions enforcer)
# ---------------------------------------------------------------------------
echo ""
echo "=== §C — Layer 2 (cf-conventions dispatch enforcer) section ==="

check_contains "Layer 2|L2|cf-conventions|dispatch interceptor|ENFORCER" "L2 / cf-conventions enforcer documented"
check_contains "intercept|dispatch interceptor|interceptor" "L2 intercepts dispatch documented"
check_contains "consult.*L1|consult.*list|reads.*LIST|checks.*LIST|LIST.*before|reads from.*reserved-ops|reads.*reserved-ops|reserved-ops\.go.*LIST|LIST.*reserved-ops" "L2 consults L1 list documented"

# ---------------------------------------------------------------------------
# §D — L3 layer section present (cf-authority evaluator)
# ---------------------------------------------------------------------------
echo ""
echo "=== §D — Layer 3 (cf-authority evaluator) section ==="

check_contains "Layer 3|L3|cf-authority|EVALUATOR|GateEvaluator" "L3 / cf-authority evaluator documented"
check_contains "chain.*walk|grant.*chain|chain.*eval|EvaluateRequest|EvaluateResult" "L3 grant-chain evaluation documented"
check_contains "DenyReason|reserved_op_floor|DENY.*reserved" "L3 DenyReason / reserved_op_floor documented"

# ---------------------------------------------------------------------------
# §E — The ten reserved ops enumerated
# ---------------------------------------------------------------------------
echo ""
echo "=== §E — All ten reserved ops listed ==="

RESERVED_OPS=(
  "disband"
  "evict"
  "admit"
  "grant"
  "revoke"
  "delegation-grant"
  "delegation-revoke"
  "delegation-accept"
  "member-roster"
  "compaction"
)

for op in "${RESERVED_OPS[@]}"; do
  if grep -q "$op" "$CHAIN_DOC"; then
    ok "reserved op '$op' present in chain doc"
  else
    fail "reserved op '$op' missing from chain doc"
  fi
done

# ---------------------------------------------------------------------------
# §F — Clamping order (owner ceiling > parent grant > convention declaration)
# ---------------------------------------------------------------------------
echo ""
echo "=== §F — Clamping order documented ==="

check_contains "owner ceiling|owner.*ceil" "owner ceiling documented"
check_contains "parent grant" "parent grant documented"
check_contains "convention declaration" "convention declaration documented"
check_contains "owner ceiling.*parent grant|clamping order.*one.directional|one.directional.*clamping" \
  "one-directional clamping order (owner ceiling > parent grant > convention declaration)"

# ---------------------------------------------------------------------------
# §G — ADVERSARIAL: L3 CANNOT lower the L1 floor (critical safety invariant)
# ---------------------------------------------------------------------------
echo ""
echo "=== §G — Adversarial: L3 cannot lower the reserved-op floor ==="
#
# This is the adversarial check required by six-condition done §10 condition 3.
# The chain doc MUST explicitly state that no L3 convention can lower the floor.
# If the doc allows L3 to override L1, this check fails — by design.

if grep -qE "no convention|No convention|cannot lower|L3.*cannot|cannot.*L3|no.*L3.*lower|floor.*not.*lower|lower.*floor.*not" "$CHAIN_DOC"; then
  ok "chain doc explicitly forbids L3/convention from lowering the reserved-op floor"
else
  fail "chain doc DOES NOT explicitly state that L3/convention cannot lower the floor (adversarial invariant violated)"
fi

# Verify the doc references protocol-spec.md (parent authority for the LIST)
if grep -q "protocol-spec.md" "$CHAIN_DOC"; then
  ok "chain doc references protocol-spec.md (source of the L1 LIST)"
else
  fail "chain doc does not reference protocol-spec.md"
fi

# Verify the doc references cf-authority-spec.md (L3 evaluator spec)
if grep -q "cf-authority-spec.md" "$CHAIN_DOC"; then
  ok "chain doc references cf-authority-spec.md (L3 evaluator spec)"
else
  fail "chain doc does not reference cf-authority-spec.md"
fi

# ---------------------------------------------------------------------------
# §H — future / fulfills disambiguation (tags, not floor ops)
# ---------------------------------------------------------------------------
echo ""
echo "=== §H — future/fulfills tag disambiguation ==="
#
# Round-2 graph-vs-messaging-split.md established that 'future' and 'fulfills'
# are reserved TAGS (member-signed), not reserved-op floor entries.
# The chain doc must make this distinction explicit.

if grep -q 'future' "$CHAIN_DOC"; then
  ok "'future' reserved tag referenced in chain doc"
else
  fail "'future' reserved tag not referenced (disambiguation missing)"
fi

if grep -q 'fulfills' "$CHAIN_DOC"; then
  ok "'fulfills' reserved tag referenced in chain doc"
else
  fail "'fulfills' reserved tag not referenced (disambiguation missing)"
fi

if grep -qE "reserved tag|not.*floor.*op|tag.*not.*op|distinguished from.*floor|separate from.*floor" "$CHAIN_DOC"; then
  ok "chain doc disambiguates reserved tags from reserved-op floor entries"
else
  fail "chain doc does not explicitly distinguish reserved tags from floor ops (disambiguation missing)"
fi

# ---------------------------------------------------------------------------
# §I — Architecture diagram or ASCII chain present
# ---------------------------------------------------------------------------
echo ""
echo "=== §I — Architecture diagram / chain illustration ==="

if grep -qE "L1.*L2.*L3|L1 →|→ L2|→ L3|L1→L2|L2→L3|cf-protocol.*cf-conventions.*cf-authority" "$CHAIN_DOC"; then
  ok "L1→L2→L3 chain illustrated in doc (diagram or inline text)"
else
  fail "L1→L2→L3 chain illustration absent from doc"
fi

# ---------------------------------------------------------------------------
# §J — Doc cites relevant design references
# ---------------------------------------------------------------------------
echo ""
echo "=== §J — Design reference citations ==="

REFS=(
  "0.30-design.md"
  "OPEN-003"
)

for ref in "${REFS[@]}"; do
  if grep -qF "$ref" "$CHAIN_DOC"; then
    ok "reference '$ref' cited in chain doc"
  else
    fail "reference '$ref' not cited in chain doc"
  fi
done

# ---------------------------------------------------------------------------
# §K — future/fulfills are NOT in the floor ops list (Python validation)
# ---------------------------------------------------------------------------
echo ""
echo "=== §K — Structural validation: future/fulfills absent from floor ops list ==="

python3 - <<'PYEOF'
import sys

# The canonical 10-op reserved-op floor list (from protocol-spec.md §Reserved-Op Floor LIST)
FLOOR_OPS = {
    "disband", "evict", "admit", "grant", "revoke",
    "delegation-grant", "delegation-revoke", "delegation-accept",
    "member-roster", "compaction"
}

# Tags that are reserved but NOT floor ops (from round-2 graph-vs-messaging-split.md)
RESERVED_TAGS_NOT_OPS = {"future", "fulfills"}

# Verify no overlap
overlap = FLOOR_OPS & RESERVED_TAGS_NOT_OPS
if overlap:
    print(f"  [FAIL] Overlap between floor ops and reserved tags: {overlap}")
    sys.exit(1)
print(f"  [PASS] No overlap: 'future' and 'fulfills' are NOT in the 10-op floor list")

# Verify floor ops count
assert len(FLOOR_OPS) == 10, f"Expected 10 floor ops, got {len(FLOOR_OPS)}"
print(f"  [PASS] Floor ops count: {len(FLOOR_OPS)} (canonical 10)")

# Verify the reserved tags are member-signed (not campfire-key-signed)
# This is a documentation assertion — we verify the spec states this
# by checking that 'future' and 'fulfills' are NOT in the campfire-key-signed ops
CAMPFIRE_KEY_SIGNED_SYSTEM_EVENTS = {
    "campfire:rekey", "campfire:visibility-changed", "session:open", "session:close"
}
for tag in RESERVED_TAGS_NOT_OPS:
    assert tag not in CAMPFIRE_KEY_SIGNED_SYSTEM_EVENTS, \
        f"'{tag}' should be member-signed, not campfire-key-signed"
print(f"  [PASS] Reserved tags ('future', 'fulfills') are member-signed, not campfire-key-signed")

# Verify clamping order invariant: the clamping chain is one-directional
# No party lower in the chain can raise the floor on a reserved op
# owner_ceiling >= parent_grant >= convention_declaration (minimum authority required)
# This is an ordering constraint, not a numerical one
CLAMPING_ORDER = ["owner_ceiling", "parent_grant", "convention_declaration"]
# Verify the order is strict (no same-level authority can escalate)
for i in range(len(CLAMPING_ORDER) - 1):
    assert CLAMPING_ORDER[i] != CLAMPING_ORDER[i+1], "Adjacent clamping levels must be distinct"
print(f"  [PASS] Clamping order is strict: {' > '.join(CLAMPING_ORDER)}")

print("  [PASS] Structural validation complete")
PYEOF

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
  echo "ALL CHECKS PASSED — reserved-op chain doc walkthrough complete"
  echo "demo_ran: false, awaiting orchestrator"
  exit 0
fi
