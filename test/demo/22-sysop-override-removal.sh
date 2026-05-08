#!/usr/bin/env bash
# 22-sysop-override-removal.sh — OPEN-012: sysop_override removal spec conformance demo.
#
# This demo is a SPEC CONFORMANCE CHECK, not a network exercise.
# It verifies that sysop-delegation.md (in agentic-internet) has been
# updated for cf-delegation 1.0 to:
#
#   1. NOT present sysop_override: true as a valid/legitimate behavior
#      anywhere in the spec corpus.
#   2. Include a deprecation/removal note in §5.2.
#   3. Include the constraint text in §8.4: owner-of-record scope.
#   4. Include rejection language in §13.3 signing rules.
#   5. Have no lingering "valid" sysop_override references in
#      ~/projects/campfire/docs/ (the public spec corpus).
#
# Decision (campfireagent-0b3): CONSTRAIN TO OWNER-OF-RECORD.
#   Rationale: sysop_override: true was P3-leaking — any Level 2+ sysop
#   could claim global override authority across all campfires. The
#   cf-delegation 1.0 constraint narrows this to within-campfire authority
#   held by the campfire's owner-of-record. This preserves the emergency
#   break capability while eliminating the cross-campfire authority leak.
#
# Adversarial probe: grep campfire/docs/ for sysop_override to confirm
# no lingering "valid" presentation exists in the public spec corpus.
#
# Run from the campfire repo root:
#   bash test/demo/22-sysop-override-removal.sh

source "$(dirname "$0")/lib.sh"

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CAMPFIRE_DOCS="${REPO_ROOT}/docs"

# Prefer vendored copy (works in CI without external checkout).
# Fall back to the live agentic-internet checkout for local dev.
VENDORED_SPEC="${REPO_ROOT}/test/demo/fixtures/agentic-internet/docs/conventions/sysop-delegation.md"
LIVE_SPEC="${HOME}/projects/agentic-internet/docs/conventions/sysop-delegation.md"

if [ -f "$VENDORED_SPEC" ]; then
    AI_SPEC="$VENDORED_SPEC"
elif [ -f "$LIVE_SPEC" ]; then
    AI_SPEC="$LIVE_SPEC"
else
    echo "FATAL: sysop-delegation.md not found at vendored path ($VENDORED_SPEC) or live path ($LIVE_SPEC)"
    exit 2
fi

# Use direct grep on file for reliable matching (avoids bash variable size limits
# and echo-escaping issues with backticks and special chars in large files).
spec_contains() {
    local label="$1"
    local needle="$2"
    if grep -qF "$needle" "$AI_SPEC" 2>/dev/null; then
        echo -e "  ${GREEN}PASS${RESET}: $label"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${RESET}: $label"
        echo "         expected to find: $needle"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

spec_not_contains_valid() {
    local label="$1"
    local needle="$2"
    # Check if needle appears WITHOUT a removal/deprecation keyword on the same line
    if grep -F "$needle" "$AI_SPEC" 2>/dev/null | grep -qvE "removed|deprecated|MUST be rejected|MUST NOT|not valid|NOT a valid|not recognized|Removed:|is removed|P3-leaking|old.*mechanism"; then
        echo -e "  ${RED}FAIL${RESET}: $label"
        echo "         Found valid presentation:"
        grep -F "$needle" "$AI_SPEC" 2>/dev/null | grep -vE "removed|deprecated|MUST be rejected|MUST NOT|not valid|NOT a valid|not recognized|Removed:|is removed|P3-leaking|old.*mechanism"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    else
        echo -e "  ${GREEN}PASS${RESET}: $label"
        PASS_COUNT=$((PASS_COUNT + 1))
    fi
}

# ---------------------------------------------------------------------------
section "1. Amendment header: cf-delegation 1.0 amendment declared"
# ---------------------------------------------------------------------------
spec_contains "spec declares cf-delegation 1.0 amendment at top" \
    "cf-delegation 1.0 amendment (OPEN-012)"

spec_contains "spec names P3-leaking as the failure mode" \
    "P3-leaking"

# ---------------------------------------------------------------------------
section "2. Deprecation note: sysop_override is explicitly removed in §5.2"
# ---------------------------------------------------------------------------
spec_contains "spec has deprecation note for sysop_override" \
    "Deprecation note:"

spec_contains "deprecation note says field is removed in cf-delegation 1.0" \
    "is removed in cf-delegation 1.0"

spec_contains "deprecation note says messages MUST be rejected" \
    "MUST be rejected"

# ---------------------------------------------------------------------------
section "3. Constraint text: §8.4 uses 'owner-of-record' scope"
# ---------------------------------------------------------------------------
spec_contains "spec §8.4 constrains override to campfire owner-of-record" \
    "campfire's owner-of-record MAY issue an override revocation"

spec_contains "spec §8.4 has explicit scope constraint (campfire-local only)" \
    "Scope constraint:"

spec_contains "spec §8.4 says override is NOT cross-campfire" \
    "CANNOT override-revoke delegations in campfire B"

spec_contains "spec §8.4 explicitly labels sysop_override as removed" \
    "Removed:"

spec_contains "spec §8.4 says implementations MUST NOT accept sysop_override" \
    "Runtime implementations MUST NOT accept messages with"

# ---------------------------------------------------------------------------
section "4. §13.3 signing rule: sysop_override not valid"
# ---------------------------------------------------------------------------
spec_contains "§13.3 signing rule uses owner-of-record, not Level 2+ sysop" \
    "campfire's owner-of-record key (owner override"

spec_contains "§13.3 explicitly states sysop_override is NOT a valid signing authority" \
    "NOT a valid signing authority in cf-delegation 1.0"

spec_contains "§13.3 says messages presenting sysop_override MUST be rejected" \
    "messages presenting it MUST be rejected"

# ---------------------------------------------------------------------------
section "5. §16.5 references the new §8.4 (owner-of-record)"
# ---------------------------------------------------------------------------
spec_contains "§16.5 references campfire owner override, not generic sysop override" \
    "campfire owner override revocation mechanism"

spec_contains "§16.5 notes the campfire-scoped constraint" \
    "the owner cannot override-revoke in other campfires"

# ---------------------------------------------------------------------------
section "6. Adversarial: no sysop_override presented as valid in campfire/docs/"
# ---------------------------------------------------------------------------
# Public spec corpus (campfire/docs/) has no sysop_override references at all.
if grep -r "sysop_override" "$CAMPFIRE_DOCS" 2>/dev/null | grep -q .; then
    echo -e "  ${RED}FAIL${RESET}: Found sysop_override in campfire/docs/"
    grep -r "sysop_override" "$CAMPFIRE_DOCS" 2>/dev/null
    FAIL_COUNT=$((FAIL_COUNT + 1))
else
    echo -e "  ${GREEN}PASS${RESET}: No sysop_override in campfire/docs/ (correct — field is deprecated)"
    PASS_COUNT=$((PASS_COUNT + 1))
fi

# ---------------------------------------------------------------------------
section "6b. Adversarial: sysop_override in spec is ONLY in removal/deprecation context"
# ---------------------------------------------------------------------------
echo "All sysop_override occurrences in spec:"
grep -n "sysop_override" "$AI_SPEC" || true
echo ""
spec_not_contains_valid \
    "every sysop_override occurrence is in removed/deprecated/rejected context" \
    "sysop_override"

# ---------------------------------------------------------------------------
section "7. Decision documentation: constraint rationale present"
# ---------------------------------------------------------------------------
spec_contains "spec documents the emergency-break capability is preserved (not removed)" \
    "narrowing, not a removal of the emergency break capability"

spec_contains "spec documents the P3-leaking risk" \
    "global revocation through claimed root authority"

summary
