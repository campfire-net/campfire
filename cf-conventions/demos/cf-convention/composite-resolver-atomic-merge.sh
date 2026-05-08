#!/usr/bin/env bash
# cf-conventions/demos/cf-convention/composite-resolver-atomic-merge.sh
#
# Demo: CompositeResolver atomic merge guarantee (v0.19.0, campfireagent-b5e).
#
# The v0.19.0 changelog states:
#   "Chain and Anchor fields are adopted atomically from the first resolver that
#    sets TrustResolved=true, preventing attribution of chain evidence from a
#    non-trust-resolved resolver."
#
# This demo proves the atomic-merge invariant: when resolver A sets Chain but
# leaves TrustResolved=false, and resolver B sets Chain+Anchor+TrustResolved=true,
# the CompositeResolver's merged result carries B's Chain exclusively — never A's
# partial chain evidence, never a mix of A's chain with B's trust flag.
#
# Asserts:
#   1. cf-conventions/cf-convention package compiles cleanly.
#   2. CompositeResolver: Chain comes from TrustResolved setter, not partial setter
#      (TestCompositeResolver_ChainFromTrustResolver).
#   3. CompositeResolver: provenance correctly attributes Chain to TrustResolved
#      setter (Provenance["trust_resolved"] == "fake-b").
#   4. CompositeResolver: when no resolver sets TrustResolved, Chain and Anchor
#      remain nil — no partial adoption (guard against half-adoption regression).
#   5. CompositeResolver: resolver order determines Chain adoption — reversing
#      the resolver list changes which Chain appears in the merged result.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-conventions/demos/cf-convention/composite-resolver-atomic-merge.sh
#
# Requires: Go toolchain on PATH.
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
cd "$REPO_ROOT"

GO="${GO:-}"
if [ -z "$GO" ]; then
    if command -v go &>/dev/null; then
        GO=go
    elif [ -x "/usr/local/go/bin/go" ]; then
        GO=/usr/local/go/bin/go
    else
        echo "ERROR: go not found on PATH and not at /usr/local/go/bin/go" >&2
        exit 1
    fi
fi

PASS=0
FAIL=0

ok()   { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

check() {
    local label="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        ok "$label"
    else
        fail "$label"
    fi
}

echo "=== CompositeResolver atomic merge demo (v0.19.0, campfireagent-b5e) ==="
echo "Repo: $REPO_ROOT"
echo ""

# ── 1. Package compiles ───────────────────────────────────────────────────────
echo "── Compilation ──"
check "cf-conventions/cf-convention builds cleanly" \
    $GO build ./cf-conventions/cf-convention/...
echo ""

# ── 2. Atomic merge: Chain from TrustResolved setter, not partial setter ──────
#
# Setup:
#   fake-a: sets Chain (grant-from-fake-a) + Anchor but TrustResolved=false
#   fake-b: sets Chain (grant-from-fake-b) + Anchor + TrustResolved=true
#   CompositeResolver([fake-a, fake-b]).Resolve(sender) → merged
#
# Atomic-merge invariant: merged.Chain must be fake-b's chain ("grant-from-fake-b"),
# NOT fake-a's chain ("grant-from-fake-a"), because Chain evidence is adopted
# atomically with TrustResolved — only from the resolver that set TrustResolved=true.
echo "── Atomic merge invariant ──"
check "Chain comes from TrustResolved setter (not partial-Chain setter)" \
    $GO test -run TestCompositeResolver_ChainFromTrustResolver \
    ./cf-conventions/cf-convention/...
echo ""

# ── 3. Provenance attribution ─────────────────────────────────────────────────
#
# The same test also verifies that Provenance["trust_resolved"] names "fake-b"
# (the resolver that set TrustResolved=true), confirming the atomic attribution
# is recorded in provenance for auditability.
echo "── Provenance attribution ──"
check "Provenance[trust_resolved] names the TrustResolved setter" \
    $GO test -run TestCompositeResolver_ChainFromTrustResolver \
    ./cf-conventions/cf-convention/...
echo ""

# ── 4. No partial adoption when TrustResolved is never set ───────────────────
#
# When no resolver sets TrustResolved=true, Chain and Anchor must remain nil.
# This guards against a regression where Chain from a non-trust-resolved resolver
# leaks into a merged result that still has TrustResolved=false.
echo "── No partial adoption guard ──"
check "Chain and Anchor remain nil when TrustResolved never set" \
    $GO test -run TestCompositeResolver_TrustResolved_UnknownSender \
    ./cf-conventions/cf-convention/...
echo ""

# ── 5. Resolver order determines Chain adoption ───────────────────────────────
#
# The composite resolver evaluates resolvers in declaration order. The first
# resolver to set TrustResolved=true wins. Reversing the resolver slice must
# change which Chain ends up in the merged result, proving the selection is
# order-sensitive (not arbitrary or last-wins).
echo "── Resolver order sensitivity ──"
check "Resolver order determines Chain adoption (first TrustResolved setter wins)" \
    $GO test -run TestCompositeResolver_ProvenanceFirstSetter_TrustResolved \
    ./cf-conventions/cf-convention/...
echo ""

# ── 6. Full targeted suite for CompositeResolver ─────────────────────────────
echo "── Full CompositeResolver test suite ──"
check "All CompositeResolver unit tests pass" \
    $GO test -run "TestCompositeResolver" \
    ./cf-conventions/cf-convention/...
echo ""

# ── Summary ───────────────────────────────────────────────────────────────────
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    echo ""
    echo "FAIL: $FAIL check(s) failed — see above."
    exit 1
fi
echo ""
echo "Atomic merge guarantee verified: Chain and Anchor are adopted from the"
echo "resolver that sets TrustResolved=true, never from a non-trust-resolved"
echo "resolver, and never as a mix of chain evidence across resolvers."
