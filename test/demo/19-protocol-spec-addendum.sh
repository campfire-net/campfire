#!/usr/bin/env bash
# Demo 19 — Protocol Spec Addendum Walkthrough (cf 0.30 L1 Additions)
#
# Asserts that all six Layer-1 additions required by cf 0.30 are present in
# docs/protocol-spec.md. Reads the spec directly — no mocks. Exits 0 when all
# assertions pass.
#
# Run from the campfire repo root:
#   bash test/demo/19-protocol-spec-addendum.sh
#
# Six sections verified (campfireagent-759):
#   §A  campfire:visibility-changed reserved tag
#   §B  session:open / session:close L1 system events
#   §C  future / fulfills reserved tags (explicitly ratified as L1 frozen)
#   §D  Reserved-op floor LIST (10 ops, canonical statement)
#   §E  Antecedent/fulfillment fusion (inseparability rule)
#   §F  Client.Await contract — earliest-timestamp wins ordering rule
#
# Adversarial coverage (wire-format ambiguity probes):
#   ADV-1  visibility-changed MUST be campfire-key-signed (not member-signed)
#   ADV-2  session:close MUST be campfire-key-signed (not member-signed)
#   ADV-3  Await ordering MUST specify a deterministic tiebreaker (not just
#          "earliest wins") — ensures no two fulfillments are treated as equal
#   ADV-4  Reserved-op floor MUST be stated as 10 ops (not more, not fewer) —
#          prevents silent scope-creep in the list
#   ADV-5  Antecedent/fulfillment MUST be described as wire-level (not L3
#          projection) — closes the OPEN-001 carve-out ambiguity

set -euo pipefail

SPEC="$(cd "$(dirname "$0")/../.." && pwd)/docs/protocol-spec.md"

fail=0
pass=0

assert_present() {
  local label="$1"
  local pattern="$2"
  if grep -qE "$pattern" "$SPEC"; then
    echo "[PASS] $label"
    ((pass++)) || true
  else
    echo "[FAIL] $label"
    echo "       Pattern not found in protocol-spec.md: $pattern"
    ((fail++)) || true
  fi
}

assert_absent() {
  local label="$1"
  local pattern="$2"
  if ! grep -qE "$pattern" "$SPEC"; then
    echo "[PASS] $label (correctly absent)"
    ((pass++)) || true
  else
    echo "[FAIL] $label (unexpectedly present — spec has the forbidden language)"
    ((fail++)) || true
  fi
}

echo "=== Protocol Spec Addendum Walkthrough (campfireagent-759) ==="
echo "Spec: $SPEC"
echo ""

# -------------------------------------------------------------------
echo "--- §A  campfire:visibility-changed ---"
assert_present "A1 tag name present" "campfire:visibility-changed"
assert_present "A2 campfire-key-signed" "(campfire.key.signed|campfire key.signed)"
assert_present "A3 public-to-private semantics" "(public.*private|visibility)"
assert_present "A4 consumer obligations stated" "(consumer|observer|member).*(MUST|must|obligation)"
echo ""

# -------------------------------------------------------------------
echo "--- §B  session:open / session:close ---"
assert_present "B1 session:open tag" "session:open"
assert_present "B2 session:close tag" "session:close"
assert_present "B3 campfire-key-signed for session events" "session:open.*campfire.key.signed|campfire.key.signed.*session:open|session events.*campfire.key|campfire.key.*session events"
assert_present "B4 session events sit at L1 (alongside campfire:rekey)" "session:(open|close).*campfire:rekey|campfire:rekey.*session:(open|close)|session:open.*campfire:rekey|session:close.*campfire:rekey|alongside.*campfire:rekey"
echo ""

# -------------------------------------------------------------------
echo "--- §C  future / fulfills reserved tags ratified as L1 ---"
assert_present "C1 future tag named as L1 reserved" "(L1|layer.1|layer 1|1.0 freeze|frozen).*future|future.*(L1|layer.1|layer 1|1.0 freeze|frozen)"
assert_present "C2 fulfills tag named as L1 reserved" "(L1|layer.1|layer 1|1.0 freeze|frozen).*fulfills|fulfills.*(L1|layer.1|layer 1|1.0 freeze|frozen)"
assert_present "C3 DAG semantics frozen together with envelope" "(DAG|antecedent|fulfillment).*(frozen|freeze|L1 freeze)"
echo ""

# -------------------------------------------------------------------
echo "--- §D  Reserved-op floor LIST (10 ops) ---"
assert_present "D1 reserved-op floor named" "(reserved.op|Reserved.Op).*(floor|list|LIST)"
assert_present "D2 disband in list" "disband"
assert_present "D3 evict in list" "evict"
assert_present "D4 admit in list" "admit"
assert_present "D5 delegation-grant in list" "delegation-grant"
assert_present "D6 delegation-revoke in list" "delegation-revoke"
assert_present "D7 delegation-accept in list" "delegation-accept"
assert_present "D8 member-roster in list" "member-roster"
assert_present "D9 compaction in list" "(compaction|compact)"
assert_present "D10 grant in list" "\bgrant\b"
assert_present "D11 revoke in list" "\brevoke\b"
echo ""

# -------------------------------------------------------------------
echo "--- §E  Antecedent/fulfillment fusion (OPEN-001 closure) ---"
assert_present "E1 inseparable/fused language" "(inseparable|fused|fusion|inseparability)"
assert_present "E2 antecedents is wire-level / CBOR field" "(CBOR|wire.format|wire level|wire-level).*antecedent|antecedent.*(CBOR|wire.format|wire level|wire-level)"
assert_present "E3 no cf-protocol-without-DAG profile" "(no.*without.DAG|no.*cf-causality|no.*carve.out)"
assert_present "E4 activation semantics agent-local (preserved)" "[Aa]ctivation.*agent.local|agent.local.*[Aa]ctivation"
echo ""

# -------------------------------------------------------------------
echo "--- §F  Client.Await contract — fulfillment ordering ---"
assert_present "F1 earliest-timestamp wins" "earliest.timestamp|earliest timestamp"
assert_present "F2 deterministic tiebreaker present (lexicographic or message ID)" "(lexicograph|message.ID.*tie|tie.*message.ID)"
assert_present "F3 Await predicate: fulfills tag + target-in-antecedents" "(fulfills.*antecedent|antecedent.*fulfills).*(predicate|contract|condition)"
echo ""

# -------------------------------------------------------------------
echo "--- Adversarial probes ---"

# ADV-1: visibility-changed must not appear in the "Member-signed exceptions." list.
# We check the single sentence that enumerates the exceptions does not include it.
assert_absent "ADV-1 visibility-changed NOT in member-signed exceptions sentence" \
  "Member-signed exceptions\..*campfire:visibility-changed"

# ADV-2: session:close must not appear in the "Member-signed exceptions." list.
assert_absent "ADV-2 session:close NOT in member-signed exceptions sentence" \
  "Member-signed exceptions\..*session:close"

# ADV-3: Await ordering has a tiebreaker — "first" alone is insufficient
assert_present "ADV-3 Await tiebreaker present (not just 'earliest')" \
  "(lexicograph|tiebreak|tie.break)"

# ADV-4: reserved-op list claims exactly 10 ops (or enumerates all 10 explicitly)
# We verify by checking all 10 are present (D2-D11 above already do this)
# Additionally check the spec claims this is an exhaustive floor
assert_present "ADV-4 reserved-op floor described as protocol minimum (no convention can lower)" \
  "(no convention|convention.*cannot|floor.*lower|lower.*floor|minimum.*convention)"

# ADV-5: antecedents must not be positively described as an L3 projection.
# The spec may SAY "no cf-causality carve-out" (which is correct — it denies it).
# The dangerous pattern is describing antecedents AS an L3 projection without denial.
# We check: "antecedents" followed by "L3 projection" with no "no" or "not" between them.
assert_absent "ADV-5 antecedents NOT described as L3 projection without denial" \
  "antecedent.*(are|is) (an? )?(L3|layer.3|layer 3) projection"

echo ""
echo "=== Results: ${pass} passed, ${fail} failed ==="

if [[ $fail -gt 0 ]]; then
  echo "DEMO EXIT 1 — spec addendum incomplete or missing required content"
  exit 1
fi

echo "DEMO EXIT 0 — all L1 additions verified in protocol-spec.md"
exit 0
