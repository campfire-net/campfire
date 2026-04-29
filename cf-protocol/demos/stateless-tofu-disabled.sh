#!/usr/bin/env bash
# cf-protocol/demos/stateless-tofu-disabled.sh
#
# Demo: stateless-deployment TOFU disable section (campfireagent-f9c).
#
# This demo is a SPEC CONFORMANCE CHECK, not a network exercise.
# It verifies that cf-protocol/README.md contains the required section
# documenting TOFU disable semantics for stateless-deployment scenarios
# (e.g., Docker without volume, Lambda, Azure Functions / cf-functions,
# browser/Wasm). Reference: OPEN-026, design v2 §6 P5, T11.
#
# Six assertions:
#   1. Section "Stateless Deployment" exists in cf-protocol/README.md.
#   2. TOFU enabled posture is documented (trust-on-first-encounter semantics).
#   3. TOFU disabled posture is documented (explicit pinning required; no
#      implicit trust).
#   4. The detection mechanism (ephemeral filesystem detection) is documented.
#   5. The disable mechanism (how to disable TOFU) is documented.
#   6. The alternatives (explicit pinning) are documented.
#
# This script FAILS before the README section is written and PASSES after.
# It is the "failing test first" artifact required by §10 TDD discipline.
#
# Usage (from repo root):
#   bash cf-protocol/demos/stateless-tofu-disabled.sh
#
# No relay, network, or cf binary required.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
README="$REPO_ROOT/cf-protocol/README.md"

# ── Minimal inline harness (avoids test/demo/lib.sh path dependency) ───────

RED='\033[0;31m'
GREEN='\033[0;32m'
BOLD='\033[1m'
RESET='\033[0m'

PASS_COUNT=0
FAIL_COUNT=0

assert_contains() {
    local label="$1" haystack="$2" needle="$3"
    if echo "$haystack" | grep -qF "$needle"; then
        echo -e "  ${GREEN}PASS${RESET}: $label"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${RESET}: $label"
        echo "         expected to find: $needle"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

section() {
    echo ""
    echo -e "${BOLD}=== $1 ===${RESET}"
}

echo "================================================================"
echo "  Stateless-Deployment TOFU Disable — spec conformance demo"
echo "  campfireagent-f9c / OPEN-026"
echo "================================================================"

if [ ! -f "$README" ]; then
    echo -e "${RED}FATAL${RESET}: cf-protocol/README.md not found at $README"
    echo "  Create the README with the stateless-deployment section first."
    exit 2
fi

README_CONTENT="$(cat "$README")"

# ── Assertion 1: Section exists ─────────────────────────────────────────────
section "1. Stateless-Deployment section exists"
assert_contains "README has stateless-deployment section heading" \
    "$README_CONTENT" \
    "Stateless Deployment"

# ── Assertion 2: TOFU enabled posture documented ────────────────────────────
section "2. TOFU enabled = trust-on-first-encounter"
assert_contains "README documents TOFU-enabled posture (trust-on-first-encounter)" \
    "$README_CONTENT" \
    "trust-on-first-encounter"

assert_contains "README states TOFU pins first observation for future comparison" \
    "$README_CONTENT" \
    "pins"

# ── Assertion 3: TOFU disabled posture documented ───────────────────────────
section "3. TOFU disabled = explicit pinning required, no implicit trust"
assert_contains "README documents TOFU-disabled posture (explicit pinning required)" \
    "$README_CONTENT" \
    "explicit pinning"

assert_contains "README states no implicit trust when TOFU is disabled" \
    "$README_CONTENT" \
    "no implicit trust"

# ── Assertion 4: Detection mechanism documented ──────────────────────────────
section "4. Ephemeral filesystem detection mechanism documented"
assert_contains "README documents ephemeral filesystem detection" \
    "$README_CONTENT" \
    "ephemeral filesystem"

assert_contains "README names stateless-deployment categories (Lambda/Wasm/Docker)" \
    "$README_CONTENT" \
    "Lambda"

# ── Assertion 5: Disable mechanism documented as PLANNED (NOT IMPLEMENTED) ──
# CF_NO_PINS is forward-spec only in 0.30.0. The README must:
#   a. Still document CF_NO_PINS so operators know the planned interface.
#   b. Include a prominent warning that it is NOT IMPLEMENTED in 0.30.0.
#   c. State it is PLANNED for 0.30.x.
section "5. TOFU disable mechanism documented as PLANNED / NOT IMPLEMENTED"
assert_contains "README documents CF_NO_PINS environment variable (forward-spec)" \
    "$README_CONTENT" \
    "CF_NO_PINS"

assert_contains "README warns that CF_NO_PINS is NOT IMPLEMENTED in 0.30.0" \
    "$README_CONTENT" \
    "NOT IMPLEMENTED in 0.30.0"

assert_contains "README states CF_NO_PINS is PLANNED for 0.30.x" \
    "$README_CONTENT" \
    "PLANNED for 0.30.x"

# ── Assertion 6: Alternatives documented as CURRENTLY IMPLEMENTED ────────────
# Operator alternatives (cf trust pin pre-seeding, [trust] config.toml) are
# the ONLY working mechanisms in 0.30.0 and must be clearly documented.
section "6. Operator alternatives documented (implemented today)"
assert_contains "README documents explicit key pinning as alternative" \
    "$README_CONTENT" \
    "cf trust pin"

assert_contains "README documents operator-supplied trust as alternative" \
    "$README_CONTENT" \
    "operator"

assert_contains "README directs readers to Operator Alternatives for 0.30.0" \
    "$README_CONTENT" \
    "operator alternatives"

# ── Adversarial: security posture difference explicitly stated ───────────────
section "Adversarial: security posture difference explicitly stated"
assert_contains "README explains security posture of TOFU-enabled state" \
    "$README_CONTENT" \
    "TOFU enabled"

assert_contains "README explains security posture of TOFU-disabled state" \
    "$README_CONTENT" \
    "TOFU disabled"

assert_contains "README warns that stateless TOFU is a security hole" \
    "$README_CONTENT" \
    "security hole"

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "================================================================"
echo -e "Results: ${GREEN}${PASS_COUNT} passed${RESET}, ${RED}${FAIL_COUNT} failed${RESET}"
echo "================================================================"
if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
echo ""
echo "  Artifacts:"
echo "    cf-protocol/README.md   — TOFU disable spec section"
echo "    cf-protocol/demos/stateless-tofu-disabled.sh — this demo"
echo ""
