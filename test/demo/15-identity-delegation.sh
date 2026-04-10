#!/usr/bin/env bash
# 15-identity-delegation.sh — Identity delegation lifecycle demo.
#
# Demonstrates: grant → resolve → revoke → re-grant for a chain of
# 3 identities (root → delegate → leaf) using filesystem transport.
#
# No relay or network required — all messages flow through the shared
# filesystem transport directory.
source "$(dirname "$0")/lib.sh"

export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"

trap cleanup EXIT

# ---------------------------------------------------------------------------
section "Setup: create 3 identities (root, delegate, leaf)"
# ---------------------------------------------------------------------------
ROOT_HOME=$(new_identity root);     register_cleanup "$ROOT_HOME"
DELEGATE_HOME=$(new_identity delegate); register_cleanup "$DELEGATE_HOME"
LEAF_HOME=$(new_identity leaf);     register_cleanup "$LEAF_HOME"

ROOT_PUB=$(pubkey_of "$ROOT_HOME")
DELEGATE_PUB=$(pubkey_of "$DELEGATE_HOME")
LEAF_PUB=$(pubkey_of "$LEAF_HOME")

echo "Root:     $ROOT_PUB"
echo "Delegate: $DELEGATE_PUB"
echo "Leaf:     $LEAF_PUB"

# Configure root's pubkey as trust anchor for delegate and leaf.
# Each config lives in <home>/.cf/config.toml (but the home IS the .cf dir,
# so we write directly into $DELEGATE_HOME/config.toml).
cat > "$DELEGATE_HOME/config.toml" <<EOF
[identity.trust]
anchors = ["$ROOT_PUB"]
EOF

cat > "$LEAF_HOME/config.toml" <<EOF
[identity.trust]
anchors = ["$ROOT_PUB"]
EOF

# Also configure root to have itself as anchor (for self-resolve test).
cat > "$ROOT_HOME/config.toml" <<EOF
[identity.trust]
anchors = ["$ROOT_PUB"]
EOF

echo "Trust anchors configured (root is anchor for all three)"

# ---------------------------------------------------------------------------
section "Root creates campfire and admits delegate + leaf"
# ---------------------------------------------------------------------------
CF_ID=$(create_campfire --cf-home "$ROOT_HOME")
echo "Campfire: $CF_ID"

cf join "$CF_ID" --cf-home "$DELEGATE_HOME" 2>/dev/null
echo "Delegate joined"

cf join "$CF_ID" --cf-home "$LEAF_HOME" 2>/dev/null
echo "Leaf joined"

# ---------------------------------------------------------------------------
section "Verify root itself resolves (is a trust anchor)"
# ---------------------------------------------------------------------------
# Sync root's store
cf read "$CF_ID" --cf-home "$ROOT_HOME" --all >/dev/null 2>&1 || true

RESOLVE_OUT=$(cf trust resolve "$CF_ID" "$ROOT_PUB" --cf-home "$ROOT_HOME" 2>/dev/null || true)
assert_contains "Root resolves as trust anchor" "$RESOLVE_OUT" "Resolved"

# ---------------------------------------------------------------------------
section "Root grants delegate"
# ---------------------------------------------------------------------------
# expires_at = now + 7 days (in seconds)
EXPIRES_AT=$(( $(date +%s) + 7 * 86400 ))
GRANT_PAYLOAD="{\"child_pubkey\":\"$DELEGATE_PUB\",\"campfire_id\":\"$CF_ID\",\"expires_at\":$EXPIRES_AT}"

cf send "$CF_ID" --cf-home "$ROOT_HOME" --tag identity:granted "$GRANT_PAYLOAD" >/dev/null 2>&1
echo "Root granted delegate"

# ---------------------------------------------------------------------------
section "Verify delegate is trusted"
# ---------------------------------------------------------------------------
# Sync delegate's store so the grant message is visible locally.
cf read "$CF_ID" --cf-home "$DELEGATE_HOME" --all >/dev/null 2>&1 || true

RESOLVE_OUT=$(cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" 2>/dev/null || true)
assert_contains "Delegate resolves after grant" "$RESOLVE_OUT" "Resolved"

RESOLVE_EXIT=0
cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" >/dev/null 2>&1 || RESOLVE_EXIT=$?
assert_eq "cf trust resolve exits 0 for delegate" "0" "$RESOLVE_EXIT"

# ---------------------------------------------------------------------------
section "Delegate grants leaf (2-hop chain)"
# ---------------------------------------------------------------------------
LEAF_EXPIRES_AT=$(( $(date +%s) + 6 * 86400 ))
LEAF_GRANT_PAYLOAD="{\"child_pubkey\":\"$LEAF_PUB\",\"campfire_id\":\"$CF_ID\",\"expires_at\":$LEAF_EXPIRES_AT}"

cf send "$CF_ID" --cf-home "$DELEGATE_HOME" --tag identity:granted "$LEAF_GRANT_PAYLOAD" >/dev/null 2>&1
echo "Delegate granted leaf"

# Sync leaf's store so both grants (root→delegate, delegate→leaf) are visible.
cf read "$CF_ID" --cf-home "$LEAF_HOME" --all >/dev/null 2>&1 || true

RESOLVE_OUT=$(cf trust resolve "$CF_ID" "$LEAF_PUB" --cf-home "$LEAF_HOME" 2>/dev/null || true)
assert_contains "Leaf resolves via 2-hop chain" "$RESOLVE_OUT" "Resolved"

LEAF_RESOLVE_EXIT=0
cf trust resolve "$CF_ID" "$LEAF_PUB" --cf-home "$LEAF_HOME" >/dev/null 2>&1 || LEAF_RESOLVE_EXIT=$?
assert_eq "cf trust resolve exits 0 for leaf (2-hop)" "0" "$LEAF_RESOLVE_EXIT"

# ---------------------------------------------------------------------------
section "Root revokes delegate"
# ---------------------------------------------------------------------------
REVOKE_PAYLOAD="{\"child_pubkey\":\"$DELEGATE_PUB\",\"campfire_id\":\"$CF_ID\"}"

cf send "$CF_ID" --cf-home "$ROOT_HOME" --tag identity:revoked "$REVOKE_PAYLOAD" >/dev/null 2>&1
echo "Root revoked delegate"

# All agents sync to pick up the revocation.
cf read "$CF_ID" --cf-home "$DELEGATE_HOME" --all >/dev/null 2>&1 || true
cf read "$CF_ID" --cf-home "$LEAF_HOME" --all >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
section "Verify revocation cascades"
# ---------------------------------------------------------------------------
DELEGATE_EXIT=0
cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" >/dev/null 2>&1 || DELEGATE_EXIT=$?
assert_eq "Delegate NOT trusted after revocation (exit 1)" "1" "$DELEGATE_EXIT"

DELEGATE_OUT=$(cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" 2>/dev/null || true)
assert_contains "Delegate shows DeadEnd after revocation" "$DELEGATE_OUT" "DeadEnd"

LEAF_EXIT=0
cf trust resolve "$CF_ID" "$LEAF_PUB" --cf-home "$LEAF_HOME" >/dev/null 2>&1 || LEAF_EXIT=$?
assert_eq "Leaf NOT trusted after delegate revocation (exit 1)" "1" "$LEAF_EXIT"

LEAF_OUT=$(cf trust resolve "$CF_ID" "$LEAF_PUB" --cf-home "$LEAF_HOME" 2>/dev/null || true)
assert_contains "Leaf shows DeadEnd (cascade from delegate revocation)" "$LEAF_OUT" "DeadEnd"

# ---------------------------------------------------------------------------
section "Root re-grants delegate"
# ---------------------------------------------------------------------------
# Small sleep to ensure the re-grant timestamp is strictly after the revoke.
sleep 1

REGRANT_EXPIRES_AT=$(( $(date +%s) + 7 * 86400 ))
REGRANT_PAYLOAD="{\"child_pubkey\":\"$DELEGATE_PUB\",\"campfire_id\":\"$CF_ID\",\"expires_at\":$REGRANT_EXPIRES_AT}"

cf send "$CF_ID" --cf-home "$ROOT_HOME" --tag identity:granted "$REGRANT_PAYLOAD" >/dev/null 2>&1
echo "Root re-granted delegate"

# All agents sync to pick up the new grant.
cf read "$CF_ID" --cf-home "$DELEGATE_HOME" --all >/dev/null 2>&1 || true
cf read "$CF_ID" --cf-home "$LEAF_HOME" --all >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
section "Verify re-grant restores trust"
# ---------------------------------------------------------------------------
DELEGATE_REGRANT_EXIT=0
cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" >/dev/null 2>&1 || DELEGATE_REGRANT_EXIT=$?
assert_eq "Delegate trusted again after re-grant (exit 0)" "0" "$DELEGATE_REGRANT_EXIT"

DELEGATE_REGRANT_OUT=$(cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" 2>/dev/null || true)
assert_contains "Delegate shows Resolved after re-grant" "$DELEGATE_REGRANT_OUT" "Resolved"

LEAF_REGRANT_EXIT=0
cf trust resolve "$CF_ID" "$LEAF_PUB" --cf-home "$LEAF_HOME" >/dev/null 2>&1 || LEAF_REGRANT_EXIT=$?
assert_eq "Leaf trusted again after delegate re-grant (exit 0)" "0" "$LEAF_REGRANT_EXIT"

LEAF_REGRANT_OUT=$(cf trust resolve "$CF_ID" "$LEAF_PUB" --cf-home "$LEAF_HOME" 2>/dev/null || true)
assert_contains "Leaf shows Resolved after re-grant (cascade restored)" "$LEAF_REGRANT_OUT" "Resolved"

# ---------------------------------------------------------------------------
section "JSON output mode"
# ---------------------------------------------------------------------------
JSON_OUT=$(cf trust resolve "$CF_ID" "$DELEGATE_PUB" --cf-home "$DELEGATE_HOME" --json 2>/dev/null || true)
assert_contains "JSON output contains status" "$JSON_OUT" '"status"'
assert_contains "JSON output shows Resolved" "$JSON_OUT" '"Resolved"'
assert_contains "JSON output contains anchor" "$JSON_OUT" '"anchor"'

summary
