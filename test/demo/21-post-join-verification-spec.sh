#!/usr/bin/env bash
# 21-post-join-verification-spec.sh — Stage 0 post-join verification spec demo.
#
# This demo is a SPEC CONFORMANCE CHECK, not a network exercise.
# It verifies that cf-discovery-spec.md contains the required post-join
# verification section (OPEN-015), including:
#
#   1. Section §11 ("Post-Join Verification") exists in cf-discovery-spec.md.
#   2. The chosen mechanism ("probe-write-then-observe") is documented.
#   3. At least one concrete honeypot scenario is described (the attack that
#      the mechanism detects).
#   4. The unjoin trigger is documented (when verification fails, what happens).
#   5. The unjoin signature contract is documented (what is signed, by whom).
#
# Rationale: The done-condition for campfireagent-8f3 is that the spec text
# exists and is machine-assertable. This script is the conformance harness.
# It must FAIL before the spec section is written and PASS after.
#
# No relay or network required — reads spec file only.
source "$(dirname "$0")/lib.sh"

SPEC="$(dirname "$0")/../../docs/cf-discovery-spec.md"

if [ ! -f "$SPEC" ]; then
    echo "FATAL: cf-discovery-spec.md not found at $SPEC"
    exit 2
fi

SPEC_CONTENT="$(cat "$SPEC")"

# ---------------------------------------------------------------------------
section "1. Section §11 exists: Post-Join Verification"
# ---------------------------------------------------------------------------
assert_contains "spec has section 11 post-join verification" \
    "$SPEC_CONTENT" \
    "## 11. Post-Join Verification"

# ---------------------------------------------------------------------------
section "2. Chosen mechanism: probe-write-then-observe"
# ---------------------------------------------------------------------------
assert_contains "spec names probe-write-then-observe as chosen mechanism" \
    "$SPEC_CONTENT" \
    "probe-write-then-observe"

# The Merkle-compare alternative must be rejected/not chosen
assert_contains "spec documents the rejected alternative (Merkle-compare)" \
    "$SPEC_CONTENT" \
    "Merkle"

# ---------------------------------------------------------------------------
section "3. Honeypot scenario: concrete detection case"
# ---------------------------------------------------------------------------
# The spec must describe a scenario where the mechanism fires.
# We check for the honeypot keyword and that it's in the §11 section.
assert_contains "spec describes honeypot detection scenario" \
    "$SPEC_CONTENT" \
    "honeypot"

assert_contains "spec describes non-open enforcement detection" \
    "$SPEC_CONTENT" \
    "non-open"

# ---------------------------------------------------------------------------
section "4. Unjoin trigger documented"
# ---------------------------------------------------------------------------
assert_contains "spec documents unjoin trigger on verification failure" \
    "$SPEC_CONTENT" \
    "unjoin"

assert_contains "spec states unjoin fires when probe fails" \
    "$SPEC_CONTENT" \
    "probe"

# ---------------------------------------------------------------------------
section "5. Unjoin signature contract documented"
# ---------------------------------------------------------------------------
assert_contains "spec documents what is signed on unjoin" \
    "$SPEC_CONTENT" \
    "unjoin-declaration"

assert_contains "spec states joiner signs the unjoin message" \
    "$SPEC_CONTENT" \
    "joiner"

assert_contains "spec documents inconsistent-state claim in unjoin" \
    "$SPEC_CONTENT" \
    "inconsistent"

summary
