#!/usr/bin/env bash
# conventions-idioms-walkthrough.sh
#
# Stage 0 demo script for campfireagent-965.
# Validates that cf-conventions/CONVENTIONS.md exists and contains the
# canonical 3+1 idioms and RFC promotion list required by the 0.30 design.
#
# Six-condition done §10: this script IS the "failing test first" for a
# spec-only item. It fails before the doc exists and passes after.
#
# Adversarial invariant: the doc MUST enumerate exactly four idioms — the
# three primary idioms (§5.2) plus the lazy-mint per-worker grant (+1 from
# §2.9). Not more, not fewer. If any idiom is missing or an extra idiom
# appears, this script fails — by design.
#
# Exit codes:
#   0 — all checks pass
#   1 — a required section, idiom, or RFC entry is missing/wrong
#
# Usage:
#   bash cf-conventions/demos/conventions-idioms-walkthrough.sh
#
# Run from the campfire repo root.

set -euo pipefail

CONVENTIONS_DOC="cf-conventions/CONVENTIONS.md"
PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

check_contains() {
  local pattern="$1"
  local label="$2"
  if grep -qE "$pattern" "$CONVENTIONS_DOC"; then
    ok "$label"
  else
    fail "$label (pattern: '$pattern')"
  fi
}

# ---------------------------------------------------------------------------
# §A — Document existence
# ---------------------------------------------------------------------------
echo ""
echo "=== §A — Document existence ==="

if [[ -f "$CONVENTIONS_DOC" ]]; then
  ok "CONVENTIONS.md exists at $CONVENTIONS_DOC"
else
  fail "CONVENTIONS.md missing: $CONVENTIONS_DOC"
  echo ""
  echo "RESULT: $FAIL check(s) failed — CONVENTIONS.md has not been written yet."
  echo "This is the expected failure before campfireagent-965 implementation."
  exit 1
fi

# ---------------------------------------------------------------------------
# §B — Top-level structure: two parts present
# ---------------------------------------------------------------------------
echo ""
echo "=== §B — Top-level structure ==="

check_contains "Part 1|3\+1|Canonical Convention Idioms" "Part 1 (3+1 idioms) section present"
check_contains "Part 2|RFC Promotion|RFC promotion" "Part 2 (RFC promotion list) section present"

# ---------------------------------------------------------------------------
# §C — Exactly four idiom subsections
# ---------------------------------------------------------------------------
echo ""
echo "=== §C — Exactly four idiom subsections ==="
#
# The 0.30 design specifies three primary idioms (§5.2) plus one special-case
# idiom (+1 from §2.9). Not more, not fewer.

# Count idiom subsections by the "### Idiom" heading pattern
IDIOM_COUNT=$(grep -cE "^### Idiom [0-9]" "$CONVENTIONS_DOC" || true)

if [[ "$IDIOM_COUNT" -eq 4 ]]; then
  ok "Exactly 4 idiom subsections found (3 primary + 1 special-case)"
elif [[ "$IDIOM_COUNT" -lt 4 ]]; then
  fail "Too few idiom subsections: found $IDIOM_COUNT, expected 4"
else
  fail "Too many idiom subsections: found $IDIOM_COUNT, expected 4 (adversarial: doc should not invent extra idioms)"
fi

# Verify each idiom has a name and one-line summary
check_contains "Convention-op-wraps-primitive|server-side Create" "Idiom 1 (convention-op-wraps-primitive / server-side Create) named"
check_contains "Hybrid wrapper dispatch|dontguess pattern" "Idiom 2 (hybrid wrapper dispatch / dontguess pattern) named"
check_contains "Service-side depth-1 admit grant|depth-1 admit" "Idiom 3 (service-side depth-1 admit grant) named"
check_contains "Lazy-mint per-worker grant|lazy-mint" "Idiom 4/+1 (lazy-mint per-worker grant) named"

# ---------------------------------------------------------------------------
# §D — Idiom 1: convention-op-wraps-primitive
# ---------------------------------------------------------------------------
echo ""
echo "=== §D — Idiom 1: convention-op-wraps-primitive ==="

check_contains "social.*dm|dm.*social|social <user> dm" "Idiom 1 references social dm as worked example"
check_contains "Create.*primitive|Create primitive|client\.Create" "Idiom 1 mentions Create primitive call"
check_contains "Admit|admit" "Idiom 1 mentions Admit step"
check_contains "Leave|leave" "Idiom 1 mentions Leave step (service exits; campfire self-owned)"
check_contains "Anti-pattern|anti-pattern" "Idiom 1 has anti-patterns section"
# Adversarial: doc must state service must Leave (self-owned campfire invariant)
check_contains "self.owned|participants.*own|no.*service.*member|service.*leaves|service cannot read" \
  "Idiom 1 documents self-ownership invariant (service must leave)"

# ---------------------------------------------------------------------------
# §E — Idiom 2: hybrid wrapper dispatch
# ---------------------------------------------------------------------------
echo ""
echo "=== §E — Idiom 2: hybrid wrapper dispatch ==="

check_contains "dontguess" "Idiom 2 references dontguess as worked example"
check_contains "cf-primitives|cf_primitives" "Idiom 2 mentions cf-primitives routing branch"
check_contains "convention dispatch|executor|executor\.Execute" "Idiom 2 mentions convention dispatch branch"
check_contains "F3|MCP.*CLI|CLI.*MCP|parity" "Idiom 2 references F3 MCP/CLI parity invariant"

# ---------------------------------------------------------------------------
# §F — Idiom 3: service-side depth-1 admit grant
# ---------------------------------------------------------------------------
echo ""
echo "=== §F — Idiom 3: service-side depth-1 admit grant ==="

check_contains "reach|matchmaking" "Idiom 3 references the reach / matchmaking as worked example"
check_contains "depth.*1|depth-1|depth ≤ 1|reserved.op floor" "Idiom 3 references depth-1 reserved-op floor constraint"
check_contains "single.use|single use" "Idiom 3 specifies single-use grants"
check_contains "where:|where:.*campfire|scoped.*campfire" "Idiom 3 specifies grant must be scoped to specific campfire"

# ---------------------------------------------------------------------------
# §G — Idiom 4 (+1): lazy-mint per-worker grant
# ---------------------------------------------------------------------------
echo ""
echo "=== §G — Idiom 4 (+1): lazy-mint per-worker grant ==="

check_contains "identity:introduce" "Idiom 4 references identity:introduce message"
check_contains "delegation:grant" "Idiom 4 references delegation:grant reply"
check_contains "session:open|session campfire" "Idiom 4 references session campfire / session:open"
check_contains "cfs2_|cfs1.*removed|no.*shared.*key|never.*share.*private" \
  "Idiom 4 documents no-shared-key invariant (cfs2_ or explicit statement)"
check_contains "swarm.coordination|legion" "Idiom 4 names swarm-coordination and/or legion as consumers"
check_contains "blast radius|compromised.*worker|revocation.*granular" \
  "Idiom 4 documents per-worker revocability (blast radius)"
# Adversarial: the +1 idiom must not claim general applicability — it is
# explicitly a special case for ephemeral fleets
check_contains "special.case|ephemeral.*fleet|worker.*fleet|parallel dispatch|swarm" \
  "Idiom 4 is correctly scoped as special-case / fleet pattern"

# ---------------------------------------------------------------------------
# §H — RFC promotion list: structure
# ---------------------------------------------------------------------------
echo ""
echo "=== §H — RFC promotion list: structure ==="

check_contains "RFC|rfc" "RFC promotion section present"
check_contains "ratif|Ratif" "Ratification criteria or 'ratified' status present"
check_contains "two named.*consumer|two.*consumer|named.*portfolio.*consumer|promotion.*two" \
  "Promotion policy requires two named portfolio consumers"
check_contains "frozen|Frozen" "Frozen status documented"

# Verify promotion process steps are present
check_contains "RFC.*ratif|RFC.*review|review.*ratif" "RFC promotion process stages present"

# ---------------------------------------------------------------------------
# §I — RFC table: at least one entry with all required fields
# ---------------------------------------------------------------------------
echo ""
echo "=== §I — RFC promotion table entries ==="
#
# Each entry must have: convention name, current status, sponsor/package,
# ratification criteria, target version.

# Count table rows (lines that start with | and contain RFC or ratified/frozen)
TABLE_ENTRY_COUNT=$(grep -cE "^\| .*\| (RFC|ratified|frozen|Ratified|Frozen|Parked)" "$CONVENTIONS_DOC" || true)

if [[ "$TABLE_ENTRY_COUNT" -ge 1 ]]; then
  ok "At least one RFC table entry found ($TABLE_ENTRY_COUNT entries)"
else
  fail "No RFC table entries found (expected at least one)"
fi

# Verify table has required column headers
check_contains "Convention.*Status|Convention.*Package" "RFC table has Convention/Status column headers"
check_contains "Named Consumer|Consumer" "RFC table has Named Consumers column"
check_contains "Ratification Criteria|ratification" "RFC table has Ratification Criteria column"
check_contains "Target Version|Target" "RFC table has Target Version column"

# ---------------------------------------------------------------------------
# §J — Specific named consumers documented in table
# ---------------------------------------------------------------------------
echo ""
echo "=== §J — Named consumers in RFC table ==="
#
# Per §4.3 T8, the initial table must mark each L3 package against named
# portfolio consumers. Verify key consumers appear.

CONSUMERS=("rd" "dontguess" "swarm-coordination" "social" "legion")
for consumer in "${CONSUMERS[@]}"; do
  if grep -qi "$consumer" "$CONVENTIONS_DOC"; then
    ok "Named consumer '$consumer' referenced in CONVENTIONS.md"
  else
    fail "Named consumer '$consumer' missing from CONVENTIONS.md"
  fi
done

# ---------------------------------------------------------------------------
# §K — Design reference citations present
# ---------------------------------------------------------------------------
echo ""
echo "=== §K — Design reference citations ==="

check_contains "0\.30-design\.md" "Design reference '0.30-design.md' cited"
check_contains "OPEN-019" "Design reference 'OPEN-019' cited"
check_contains "OPEN-029" "Design reference 'OPEN-029' cited"
check_contains "§5\.2|5\.2" "Design reference '§5.2' (idioms section) cited"

# ---------------------------------------------------------------------------
# §L — Structural validation via Python
# ---------------------------------------------------------------------------
echo ""
echo "=== §L — Structural validation ==="

python3 - <<'PYEOF'
import sys
import re

doc_path = "cf-conventions/CONVENTIONS.md"
with open(doc_path) as f:
    content = f.read()

errors = []

# 1. Count idiom headings — must be exactly 4
idiom_headings = re.findall(r'^### Idiom \d+', content, re.MULTILINE)
if len(idiom_headings) != 4:
    errors.append(f"Expected exactly 4 idiom headings (### Idiom N), found {len(idiom_headings)}: {idiom_headings}")
else:
    print(f"  [PASS] Exactly 4 idiom headings found")

# 2. Each idiom must have a "When to use" subsection
when_to_use_count = len(re.findall(r'#### When to use', content))
if when_to_use_count < 4:
    errors.append(f"Expected at least 4 'When to use' subsections, found {when_to_use_count}")
else:
    print(f"  [PASS] 'When to use' subsections: {when_to_use_count}")

# 3. Each idiom must have an "Anti-patterns" subsection
anti_pattern_count = len(re.findall(r'#### Anti-patterns?', content))
if anti_pattern_count < 4:
    errors.append(f"Expected at least 4 'Anti-patterns' subsections, found {anti_pattern_count}")
else:
    print(f"  [PASS] Anti-patterns subsections: {anti_pattern_count}")

# 4. RFC table must have at least one entry (line starting with | containing RFC)
rfc_table_rows = re.findall(r'^\| .+ \| RFC .+', content, re.MULTILINE)
if len(rfc_table_rows) < 1:
    errors.append(f"RFC table must have at least one RFC entry, found {len(rfc_table_rows)}")
else:
    print(f"  [PASS] RFC table rows: {len(rfc_table_rows)}")

# 5. Each RFC table row must have required fields (6 columns = 7 pipe chars)
for row in rfc_table_rows:
    pipe_count = row.count('|')
    if pipe_count < 6:
        errors.append(f"RFC table row has {pipe_count} columns (expected ≥6): {row[:80]}")
    else:
        print(f"  [PASS] RFC table row has sufficient columns: {row[:60]}...")

# 6. Idiom 4 must be marked as special case (+1), not just "Idiom 4"
idiom4_section = re.search(r'### Idiom 4.*?(?=### Idiom|\Z)', content, re.DOTALL)
if idiom4_section:
    text = idiom4_section.group(0)
    if not re.search(r'\+1|special.case|fleet|ephemeral', text, re.IGNORECASE):
        errors.append("Idiom 4 section must mark it as special-case / +1 / fleet pattern")
    else:
        print("  [PASS] Idiom 4 is correctly marked as special-case (+1)")
else:
    errors.append("Could not locate Idiom 4 section for validation")

if errors:
    for e in errors:
        print(f"  [FAIL] {e}")
    sys.exit(1)

print("  [PASS] All structural validations passed")
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
  echo "ALL CHECKS PASSED — CONVENTIONS.md idioms + RFC promotion list verified"
  echo "demo_ran: true, ready for orchestrator review"
  exit 0
fi
