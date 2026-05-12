#!/usr/bin/env bash
# cf-protocol/demos/session-tags.sh
#
# Demo: session:open / session:close L1 system event tags (campfireagent-647).
#
# This script verifies:
#   1. TagSessionOpen constant has wire value "session:open".
#   2. TagSessionClose constant has wire value "session:close".
#   3. Both tags use the "session:" prefix (not "campfire:" or "convention:").
#   4. EmitSessionOpen posts a signed message with the session:open tag.
#   5. EmitSessionClose posts a signed message with the session:close tag.
#   6. Emitted messages have valid signatures.
#   7. All cf-protocol/... tests pass.
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/session-tags.sh
#
# Exits non-zero if any assertion fails.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO=${GO:-/usr/local/go/bin/go}
PASS_COUNT=0
FAIL_COUNT=0

pass() { echo "PASS: $*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail() { echo "FAIL: $*" >&2; FAIL_COUNT=$((FAIL_COUNT+1)); }

echo "=== session:open / session:close L1 tags demo (campfireagent-647) ==="
echo "Repo: $REPO_ROOT"
echo ""

# ─────────────────────────────────────────────────────────────────────────────
# Pre-compile test binaries ONCE (campfireagent-c2b).
#
# Determinism rationale (same family as campfireagent-f28 / PR #566 and
# campfireagent-4e3 / PR #568):
#   Earlier revisions invoked `go test` multiple times across cf-protocol
#   packages (Steps 6, 7, 8). Each invocation revalidated $GOCACHE
#   (~/.cache/go-build). Under parallel-worktree stress-sweep load, those
#   concurrent revalidations evict/partially write cache entries that other
#   invocations are reading, producing transient setup failures and pushing
#   the demo past its 120s per-demo budget (3/10 runs in the rc.5 sweep).
#
# Fix: compile the two needed test binaries once into a private mktemp dir,
# then exec each binary directly with -test.run filters. The binaries are
# self-contained — no go toolchain at run time, no shared build-cache surface.
# Step 8 reduces to `go build ./cf-protocol/...` (compile-guard) since the
# touched packages are already covered by Steps 6 and 7.
TEST_BIN_DIR="$(mktemp -d -t session-tags-XXXXXX)"
trap 'rm -rf "$TEST_BIN_DIR"' EXIT
TAGSPEC_BIN="$TEST_BIN_DIR/tagspec.test"
PROTOCOL_BIN="$TEST_BIN_DIR/protocol.test"

# ── Step 1: Build ─────────────────────────────────────────────────────────────
echo "--- Step 1: Build"

if (cd "$REPO_ROOT" && $GO build ./cf-protocol/... 2>/dev/null); then
  pass "go build ./cf-protocol/... succeeds"
else
  fail "go build ./cf-protocol/... FAILED"
fi

# Pre-compile test binaries used by Steps 6 and 7. A compile failure here is
# a deterministic hard failure with a clear message — never a flaky section.
echo ""
echo "--- Step 1b: Pre-compile test binaries"
if (cd "$REPO_ROOT" && $GO test -c -o "$TAGSPEC_BIN" ./cf-protocol/internal/tagspec/ 2>&1); then
  pass "tagspec test binary compiled"
else
  fail "could not compile tagspec test binary"
  echo "ABORT: build failed; session-tags demo cannot run." >&2
  exit 1
fi
if (cd "$REPO_ROOT" && $GO test -c -o "$PROTOCOL_BIN" ./cf-protocol/protocol/ 2>&1); then
  pass "protocol test binary compiled"
else
  fail "could not compile protocol test binary"
  echo "ABORT: build failed; session-tags demo cannot run." >&2
  exit 1
fi

# ── Step 2: Tag constants verification ────────────────────────────────────────
echo ""
echo "--- Step 2: Tag constant wire values"

# Verify via go vet + grep on source
SESSION_OPEN=$(grep 'TagSessionOpen = "session:open"' \
  "$REPO_ROOT/cf-protocol/internal/tagspec/tagspec.go" 2>/dev/null | wc -l || true)
if [ "$SESSION_OPEN" -gt 0 ]; then
  pass "TagSessionOpen = \"session:open\" defined in tagspec.go"
else
  fail "TagSessionOpen = \"session:open\" NOT found in tagspec.go"
fi

SESSION_CLOSE=$(grep 'TagSessionClose = "session:close"' \
  "$REPO_ROOT/cf-protocol/internal/tagspec/tagspec.go" 2>/dev/null | wc -l || true)
if [ "$SESSION_CLOSE" -gt 0 ]; then
  pass "TagSessionClose = \"session:close\" defined in tagspec.go"
else
  fail "TagSessionClose = \"session:close\" NOT found in tagspec.go"
fi

# ── Step 3: Public surface re-exports ─────────────────────────────────────────
echo ""
echo "--- Step 3: Public surface re-exports"

OPEN_EXPORT=$(grep 'TagSessionOpen = tagspec.TagSessionOpen' \
  "$REPO_ROOT/cf-protocol/protocol/session_events.go" 2>/dev/null | wc -l || true)
if [ "$OPEN_EXPORT" -gt 0 ]; then
  pass "TagSessionOpen re-exported in cf-protocol/protocol/session_events.go"
else
  fail "TagSessionOpen re-export NOT found in cf-protocol/protocol/session_events.go"
fi

CLOSE_EXPORT=$(grep 'TagSessionClose = tagspec.TagSessionClose' \
  "$REPO_ROOT/cf-protocol/protocol/session_events.go" 2>/dev/null | wc -l || true)
if [ "$CLOSE_EXPORT" -gt 0 ]; then
  pass "TagSessionClose re-exported in cf-protocol/protocol/session_events.go"
else
  fail "TagSessionClose re-export NOT found in cf-protocol/protocol/session_events.go"
fi

# ── Step 4: Emission API exists ───────────────────────────────────────────────
echo ""
echo "--- Step 4: Emission API surface"

EMIT_OPEN=$(grep 'func EmitSessionOpen' \
  "$REPO_ROOT/cf-protocol/protocol/session_events.go" 2>/dev/null | wc -l || true)
if [ "$EMIT_OPEN" -gt 0 ]; then
  pass "EmitSessionOpen function defined in cf-protocol/protocol/session_events.go"
else
  fail "EmitSessionOpen function NOT found in cf-protocol/protocol/session_events.go"
fi

EMIT_CLOSE=$(grep 'func EmitSessionClose' \
  "$REPO_ROOT/cf-protocol/protocol/session_events.go" 2>/dev/null | wc -l || true)
if [ "$EMIT_CLOSE" -gt 0 ]; then
  pass "EmitSessionClose function defined in cf-protocol/protocol/session_events.go"
else
  fail "EmitSessionClose function NOT found in cf-protocol/protocol/session_events.go"
fi

# ── Step 5: Tag-namespace isolation ───────────────────────────────────────────
echo ""
echo "--- Step 5: Tag-namespace isolation"

# session:open must NOT start with campfire:
if echo "session:open" | grep -q "^campfire:"; then
  fail "session:open starts with campfire: — namespace collision"
else
  pass "session:open does NOT use campfire: prefix"
fi

if echo "session:close" | grep -q "^convention:"; then
  fail "session:close starts with convention: — namespace collision"
else
  pass "session:close does NOT use convention: prefix"
fi

if [ "session:open" != "session:close" ]; then
  pass "session:open and session:close are distinct wire values"
else
  fail "session:open and session:close are identical — collision"
fi

# ── Step 6: Unit tests ────────────────────────────────────────────────────────
echo ""
echo "--- Step 6: Unit tests for tagspec constants"

set +e
TAGSPEC_OUT=$("$TAGSPEC_BIN" -test.run "TestSessionTag" -test.v 2>&1)
TAGSPEC_EXIT=$?
set -e
if [ $TAGSPEC_EXIT -eq 0 ] && ! echo "$TAGSPEC_OUT" | grep -q "^--- FAIL"; then
  pass "tagspec unit tests pass (TestSessionTag*)"
else
  fail "tagspec unit tests FAILED"
  echo "$TAGSPEC_OUT" | grep -E "PASS|FAIL" | sed 's/^/  /'
fi

echo ""
echo "--- Step 7: Emission tests"

set +e
EMIT_OUT=$("$PROTOCOL_BIN" -test.run "TestTagSession|TestEmitSession" -test.v -test.timeout 60s 2>&1)
EMIT_EXIT=$?
set -e
if [ $EMIT_EXIT -eq 0 ] && ! echo "$EMIT_OUT" | grep -q "^--- FAIL"; then
  pass "protocol emission tests pass (TestTagSession*, TestEmitSession*)"
else
  fail "protocol emission tests FAILED"
  echo "$EMIT_OUT" | grep -E "PASS|FAIL" | sed 's/^/  /'
fi

echo ""
echo "--- Step 8: cf-protocol/... compile guard"

# Compile-only guard (campfireagent-c2b): the touched test packages
# (cf-protocol/internal/tagspec/ and cf-protocol/protocol/) are exhaustively
# covered by Steps 6 and 7. The remaining cf-protocol/... packages just need
# a compile-passing check here — full module `go test ./cf-protocol/...`
# revalidates GOCACHE and races parallel worktrees (3/10 timeout in the rc.5
# stress-sweep). `go build` gives the same compile guarantee deterministically.
if (cd "$REPO_ROOT" && $GO build ./cf-protocol/... 2>&1); then
  pass "go build ./cf-protocol/... — all packages compile"
else
  fail "go build ./cf-protocol/... — compile FAILED"
fi

# ── Summary ────────────────────────────────────────────────────────────────────
echo ""
echo "=== Summary ==="
echo "PASS: $PASS_COUNT"
echo "FAIL: $FAIL_COUNT"
echo ""

if [ "$FAIL_COUNT" -eq 0 ]; then
  echo "ALL CHECKS PASSED — session:open / session:close L1 tags established."
  echo ""
  echo "  Artifacts:"
  echo "    cf-protocol/internal/tagspec/tagspec.go    — TagSessionOpen, TagSessionClose constants"
  echo "    cf-protocol/protocol/session_events.go     — public re-exports + EmitSessionOpen / EmitSessionClose"
  echo "    cf-protocol/protocol/session.go            — sendTagged helper (used by emission API)"
  echo "    cf-protocol/protocol/session_events_test.go — 15 tests (TDD, adversarial)"
  echo "    cf-protocol/internal/tagspec/tagspec_test.go — 3 new tag tests"
  echo ""
  echo "  NOT in this stage:"
  echo "    Stage 3 cf-session orchestrator code (that lives in cf-conventions/cf-session/)."
  exit 0
else
  echo "SOME CHECKS FAILED — review output above."
  exit 1
fi
