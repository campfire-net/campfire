#!/usr/bin/env bash
# 31-unjoin-declaration-canonical-field-order.sh — §11.5 canonical field order spec demo.
#
# This demo is a SPEC CONFORMANCE CHECK, not a network exercise.
# It verifies that cf-discovery-spec.md §11.5 explicitly defines:
#
#   1. The canonical field order is defined (alphabetical or declaration order,
#      explicitly stated — not just referenced).
#   2. A concrete worked example of the canonical JSON payload is present.
#   3. JSON serialization rules are specified (no whitespace, UTF-8, RFC 8259,
#      no trailing newline).
#   4. The prefix bytes and separator are specified exactly
#      (discovery:unjoin-declaration\n, \n = 0x0A LF, no CR).
#   5. The worked example is valid JSON (parseable).
#   6. The worked example contains all five required fields:
#      campfire_id, reason, observed_inconsistency, probe_msg_id, joiner_pubkey.
#   7. The fields in the worked example appear in the declared canonical order
#      (alphabetical).
#
# Rationale: campfireagent-14d veracity finding — §11.5 said "canonical field
# order" without defining what canonical means, making signed payloads
# non-reproducible across implementations.
#
# No relay or network required — reads spec file only.
source "$(dirname "$0")/lib.sh"

SPEC="$(dirname "$0")/../../docs/cf-discovery-spec.md"

if [ ! -f "$SPEC" ]; then
    echo "FATAL: cf-discovery-spec.md not found at $SPEC"
    exit 2
fi

SPEC_CONTENT="$(cat "$SPEC")"

# Extract §11.5 section text (from ### 11.5 up to ### 11.6)
SECTION_115="$(awk '/^### 11\.5 /,/^### 11\.6/' "$SPEC")"

# ---------------------------------------------------------------------------
section "1. Canonical field order is explicitly defined"
# ---------------------------------------------------------------------------
assert_contains "spec §11.5 names alphabetical canonical order" \
    "$SECTION_115" \
    "alphabetical"

assert_contains "spec §11.5 references RFC 8259 or RFC 7159 for field ordering context" \
    "$SPEC_CONTENT" \
    "RFC 8259"

# ---------------------------------------------------------------------------
section "2. Worked example is present"
# ---------------------------------------------------------------------------
assert_contains "spec §11.5 contains a worked example header or label" \
    "$SECTION_115" \
    "Example"

assert_contains "spec §11.5 worked example contains campfire_id field" \
    "$SECTION_115" \
    "campfire_id"

assert_contains "spec §11.5 worked example contains reason field" \
    "$SECTION_115" \
    "probe-verification-failed"

assert_contains "spec §11.5 worked example contains observed_inconsistency field" \
    "$SECTION_115" \
    "observed_inconsistency"

assert_contains "spec §11.5 worked example contains probe_msg_id field" \
    "$SECTION_115" \
    "probe_msg_id"

assert_contains "spec §11.5 worked example contains joiner_pubkey field" \
    "$SECTION_115" \
    "joiner_pubkey"

# ---------------------------------------------------------------------------
section "3. JSON serialization rules are specified"
# ---------------------------------------------------------------------------
assert_contains "spec §11.5 specifies no-whitespace between separators" \
    "$SECTION_115" \
    "No whitespace"

assert_contains "spec §11.5 specifies UTF-8 encoding" \
    "$SECTION_115" \
    "UTF-8"

assert_contains "spec §11.5 specifies no trailing newline after JSON object" \
    "$SECTION_115" \
    "no trailing newline"

# ---------------------------------------------------------------------------
section "4. Prefix bytes and separator are specified exactly"
# ---------------------------------------------------------------------------
assert_contains "spec §11.5 specifies LF separator (0x0A)" \
    "$SECTION_115" \
    "0x0A"

assert_contains "spec §11.5 specifies no CR in separator" \
    "$SECTION_115" \
    "No CR"

assert_contains "spec §11.5 specifies discovery:unjoin-declaration prefix" \
    "$SECTION_115" \
    "discovery:unjoin-declaration"

# ---------------------------------------------------------------------------
section "5. Worked example is valid JSON"
# ---------------------------------------------------------------------------
# Extract the JSON object from the worked example code block in §11.5.
# We look for a line starting with { in the section and try to parse it.
EXAMPLE_JSON="$(echo "$SECTION_115" | grep -E '^\{"campfire_id"' | head -1)"

if [ -z "$EXAMPLE_JSON" ]; then
    echo -e "  ${RED}FAIL${RESET}: could not extract worked example JSON line from §11.5"
    FAIL_COUNT=$((FAIL_COUNT + 1))
else
    PARSE_RESULT="$(echo "$EXAMPLE_JSON" | python3 -c "
import sys, json
try:
    obj = json.loads(sys.stdin.read().strip())
    print('valid')
except Exception as e:
    print('invalid: ' + str(e))
")"
    assert_eq "worked example parses as valid JSON" "valid" "$PARSE_RESULT"
fi

# ---------------------------------------------------------------------------
section "6. Worked example contains all five required fields"
# ---------------------------------------------------------------------------
# (Already covered by field-presence checks in section 2 above — this section
# consolidates the structural check via JSON parse.)
if [ -n "$EXAMPLE_JSON" ]; then
    for FIELD in campfire_id reason observed_inconsistency probe_msg_id joiner_pubkey; do
        HAS_FIELD="$(echo "$EXAMPLE_JSON" | python3 -c "
import sys, json
obj = json.loads(sys.stdin.read().strip())
print('yes' if '$FIELD' in obj else 'no')
" 2>/dev/null || echo 'no')"
        assert_eq "worked example has field: $FIELD" "yes" "$HAS_FIELD"
    done
fi

# ---------------------------------------------------------------------------
section "7. Fields in worked example appear in alphabetical canonical order"
# ---------------------------------------------------------------------------
# The alphabetical order of the five fields is:
#   campfire_id, joiner_pubkey, observed_inconsistency, probe_msg_id, reason
if [ -n "$EXAMPLE_JSON" ]; then
    FIELD_ORDER="$(echo "$EXAMPLE_JSON" | python3 -c "
import sys, json, re
# Preserve insertion order by parsing key positions directly
text = sys.stdin.read().strip()
# Extract keys in order of appearance
keys = re.findall(r'\"([^\"]+)\"\\s*:', text)
# Filter to only the five required fields
required = {'campfire_id','joiner_pubkey','observed_inconsistency','probe_msg_id','reason'}
ordered = [k for k in keys if k in required]
print(','.join(ordered))
")"
    EXPECTED_ORDER="campfire_id,joiner_pubkey,observed_inconsistency,probe_msg_id,reason"
    assert_eq "worked example fields are in alphabetical order" \
        "$EXPECTED_ORDER" "$FIELD_ORDER"
fi

summary
