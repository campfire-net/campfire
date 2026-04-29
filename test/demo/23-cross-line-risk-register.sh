#!/usr/bin/env bash
# 23-cross-line-risk-register.sh — Verify the 0.30 cross-line critical-fix risk register.
#
# This demo script is the "test of last resort" for OPEN-030 (T10 cross-line critical-fix
# risk register). It reads docs/0.30-critical-fix-risk-register.md and asserts:
#   1. The register exists.
#   2. All five named fixes are enumerated (FIX-1 through FIX-5).
#   3. Each fix has a rollback plan.
#   4. The summary table is present.
#   5. Each audit ID is represented (MB1, MB2, T5, T6, I1/I9/I13).
#
# The script intentionally does NOT require a running campfire or network access.
# It exercises the spec artifact only — this is a spec-gap item (OPEN-030 Queue B),
# not a feature item.
#
# Reference: docs/0.30-critical-fix-risk-register.md
# Design: docs/design/0.30-design.md §7; OPEN-ITEMS.md OPEN-030

source "$(dirname "$0")/lib.sh"

REGISTER="$(cd "$(dirname "$0")/../.." && pwd)/docs/0.30-critical-fix-risk-register.md"

section "1. Register existence"
if [ -f "$REGISTER" ]; then
    echo -e "  ${GREEN}PASS${RESET}: register exists at docs/0.30-critical-fix-risk-register.md"
    PASS_COUNT=$((PASS_COUNT + 1))
else
    echo -e "  ${RED}FAIL${RESET}: register not found at $REGISTER"
    FAIL_COUNT=$((FAIL_COUNT + 1))
fi

CONTENT=""
if [ -f "$REGISTER" ]; then
    CONTENT=$(cat "$REGISTER")
fi

section "2. All five fixes present"
for fix in "FIX-1" "FIX-2" "FIX-3" "FIX-4" "FIX-5"; do
    assert_contains "fix $fix present" "$CONTENT" "$fix"
done

section "3. Rollback plan present for each fix"
# Count rollback plan sections — one per fix
ROLLBACK_COUNT=$(echo "$CONTENT" | grep -c "^**Rollback plan" || true)
if [ "$ROLLBACK_COUNT" -ge 5 ]; then
    echo -e "  ${GREEN}PASS${RESET}: $ROLLBACK_COUNT rollback plans found (need ≥ 5)"
    PASS_COUNT=$((PASS_COUNT + 1))
else
    echo -e "  ${RED}FAIL${RESET}: only $ROLLBACK_COUNT rollback plan(s) found, need ≥ 5"
    FAIL_COUNT=$((FAIL_COUNT + 1))
fi

section "4. Summary table present"
assert_contains "summary table header" "$CONTENT" "| Fix ID | Audit ID | Severity |"
assert_contains "summary table has 5 rows" "$CONTENT" "| FIX-5 |"

section "5. All audit IDs represented"
assert_contains "MB1 present" "$CONTENT" "MB1"
assert_contains "MB2 present" "$CONTENT" "MB2"
assert_contains "T5 present" "$CONTENT" "T5"
assert_contains "T6 present" "$CONTENT" "T6"
assert_contains "I1 present" "$CONTENT" "I1"
assert_contains "I9 present" "$CONTENT" "I9"
assert_contains "I13 present" "$CONTENT" "I13"

section "6. Forward-port protocol present"
assert_contains "forward-port protocol" "$CONTENT" "Forward-Port Protocol"

section "7. Adversarial case: register must enumerate the identity revocation triangle constraint"
# The spec requires that the register call out that I1/I9/I13 must not be partially fixed.
# This is the adversarial case — the register is wrong if it treats identity as a simple 'fix'.
assert_contains "I1/I9/I13 triangle constraint" "$CONTENT" "revocation triangle"
assert_contains "no partial changes constraint" "$CONTENT" "partial"

summary
