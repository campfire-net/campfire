#!/usr/bin/env bash
# design-docs-0.30-reflection.sh — verify docs/  reflects 0.30 surface.
#
# Checks:
#   PRESENT: key 0.30 terms must appear in the active docs
#   ABSENT:  key 0.19 terms must NOT appear in active docs (outside CHANGELOG/upgrade-guide context)
#
# Exit 0 = all checks pass
# Exit 1 = one or more checks fail

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DOCS_DIR="$REPO_ROOT/docs"

# Active docs — docs that should reflect 0.30 reality.
ACTIVE_DOCS=(
    "$DOCS_DIR/convention-sdk.md"
    "$DOCS_DIR/cli-conventions.md"
    "$DOCS_DIR/architecture-hosted-service.md"
    "$DOCS_DIR/mcp-conventions.md"
    "$DOCS_DIR/cf-authority-spec.md"
    "$DOCS_DIR/cf-discovery-spec.md"
)

# Docs that are intentionally 0.19-era historical records (superseded, not removed).
HISTORICAL_DOCS=(
    "$DOCS_DIR/protocol-spec.md"
    "$DOCS_DIR/specs/v0.19-completion-spec.md"
    "$DOCS_DIR/identity-audit.md"
    "$DOCS_DIR/design-naming-locality.md"
)

FAILURES=0

echo "=== design-docs-0.30-reflection: verifying docs/ reflects 0.30 surface ==="
echo ""

# ---------------------------------------------------------------------------
# Helper: assert term IS present in at least one active doc
# ---------------------------------------------------------------------------
check_present() {
    local term="$1"
    local desc="$2"
    local found=0
    for f in "${ACTIVE_DOCS[@]}"; do
        if [ -f "$f" ] && grep -qi "$term" "$f"; then
            found=1
            break
        fi
    done
    if [ "$found" -eq 1 ]; then
        echo "  OK (present):  $desc"
    else
        echo "  FAIL (missing): $desc"
        echo "    term: '$term' — not found in any active doc"
        FAILURES=$((FAILURES + 1))
    fi
}

# ---------------------------------------------------------------------------
# Helper: assert term is ABSENT from active docs (may appear in historical only)
# ---------------------------------------------------------------------------
check_absent_from_active() {
    local term="$1"
    local desc="$2"
    local violations=()
    for f in "${ACTIVE_DOCS[@]}"; do
        if [ -f "$f" ] && grep -qi "$term" "$f"; then
            violations+=("$f")
        fi
    done
    if [ "${#violations[@]}" -eq 0 ]; then
        echo "  OK (absent):   $desc"
    else
        echo "  FAIL (present in active): $desc"
        echo "    term: '$term' — still in:"
        for v in "${violations[@]}"; do
            echo "      $v"
        done
        FAILURES=$((FAILURES + 1))
    fi
}

# ---------------------------------------------------------------------------
# Helper: assert historical doc is marked as superseded / historical
# ---------------------------------------------------------------------------
check_superseded_marker() {
    local file="$1"
    local desc="$2"
    if [ ! -f "$file" ]; then
        echo "  SKIP (not found): $desc"
        return
    fi
    if grep -qi "superseded\|historical\|0\.19\|removed in 0\.30\|0\.30 cf-protocol" "$file" 2>/dev/null; then
        echo "  OK (marked):   $desc"
    else
        echo "  FAIL (no marker): $desc"
        echo "    file: $file — no superseded/historical marker found"
        FAILURES=$((FAILURES + 1))
    fi
}

# ===========================================================================
# Section 1: 0.30 key terms MUST be present in active docs
# ===========================================================================
echo "--- Section 1: 0.30 terms present in active docs ---"
echo ""

check_present "cf-authority"                    "cf-authority L3 evaluator referenced"
check_present "GateEvaluator"                    "GateEvaluator interface referenced"
check_present "cf-identity"                      "cf-identity L3 package referenced"
check_present "cf-session"                       "cf-session L3 package referenced"
check_present "cf-convention-extension"          "cf-convention-extension package referenced"
check_present "cf-discovery"                     "cf-discovery package referenced"
check_present "cf-protocol"                      "cf-protocol module referenced"
check_present "cf-conventions"                   "cf-conventions module referenced"
check_present "scoped grant"                     "scoped grants model described"
check_present "bounded.transitivity\|bounded transitivity" "bounded transitivity described"
check_present "ephemeral.key\|ephemeral key\|ephemeral Ed25519" "cf-session ephemeral key model described"
check_present "naming:preview\|snippet"          "cf-discovery snippet/preview term referenced"
check_present "delegation:request"               "approval flow delegation:request referenced"
check_present "cf-primitives"                    "cf-primitives binary referenced"
check_present "cf-functions"                     "cf-functions deployment binary referenced"

echo ""

# ===========================================================================
# Section 2: 0.19 terms MUST NOT appear in active docs
# ===========================================================================
echo "--- Section 2: 0.19 removed terms absent from active docs ---"
echo ""

check_absent_from_active "RecenterCanonicalPayload\|func.*Recenter\|maybeRecenter" \
    "recenter.go live function references removed (OK to mention file name in removal notes)"
check_absent_from_active "WithWalkUp.*option\|WalkUpEnabled.*method\|func.*WalkUp\b" \
    "walk_up.go live API declarations removed (OK to list removed symbols in migration notes)"
check_absent_from_active "present_as.*=\|\"present_as\"\|identity\.present_as" \
    "present_as config key removed"
check_absent_from_active "TypeGitHub\|sendGitHub\|--transport github --github-repo\|github:.*transport" \
    "GitHub transport code/usage patterns removed (OK to mention removal)"
check_absent_from_active "cfs1_[A-Za-z0-9]" \
    "cfs1_ shared-key session token format removed (OK to mention in historical context)"
check_absent_from_active "All participants share one ephemeral.*signing key\|all participants share one ephemeral" \
    "old shared-key 'all participants share' session security note removed"
check_absent_from_active "no per-sender attribution inside a session\|no per.sender attribution" \
    "old 'no per-sender attribution inside a session' note removed"
check_absent_from_active "pkg/protocol/join\|pkg/protocol/send\|pkg/protocol/evict\|pkg/convention/identity\.go\|pkg/naming/resolve\|pkg/beacon/campfire\|pkg/trust/pins" \
    "old pkg/ implementation paths removed from active docs (OK to mention in 0.30 migration notes)"

echo ""

# ===========================================================================
# Section 3: Historical docs marked as superseded
# ===========================================================================
echo "--- Section 3: historical docs have superseded markers ---"
echo ""

check_superseded_marker "$DOCS_DIR/protocol-spec.md" \
    "protocol-spec.md marked as 0.19 / superseded by 0.30"

echo ""

# ===========================================================================
# Section 4: New spec docs exist
# ===========================================================================
echo "--- Section 4: 0.30 spec docs exist ---"
echo ""

for f in \
    "$DOCS_DIR/cf-authority-spec.md" \
    "$DOCS_DIR/cf-discovery-spec.md"; do
    if [ -f "$f" ]; then
        echo "  OK (exists):   $(basename "$f")"
    else
        echo "  FAIL (missing): $(basename "$f")"
        FAILURES=$((FAILURES + 1))
    fi
done

echo ""

# ===========================================================================
# Result
# ===========================================================================
if [ "$FAILURES" -eq 0 ]; then
    echo "PASS: docs/ reflect 0.30 surface (design-docs-0.30-reflection)"
    exit 0
else
    echo "FAIL: $FAILURES check(s) failed — docs do not fully reflect 0.30 surface"
    exit 1
fi
