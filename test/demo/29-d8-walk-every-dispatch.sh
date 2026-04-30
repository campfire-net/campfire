#!/usr/bin/env bash
# 29-d8-walk-every-dispatch.sh — D8 walk-every-dispatch deal-breaker demo.
#
# Demonstrates the D8 invariant: the dispatcher re-walks the full delegation
# chain on every dispatch; cached scope claims from a prior dispatch are not
# trusted.
#
# Test sequence (mirrors the integration test in pkg/convention/):
#   1. Build a 3-key chain: anchor → mid → sender
#   2. Resolve (dispatch 1 proxy): sender traces back to anchor → Resolved
#   3. Revoke mid-chain key: anchor posts identity:revoked for mid
#   4. Resolve (dispatch 2 proxy): same sender → chain broken → DeadEnd
#
# The "resolve" command exercises the same delegation.Resolve() path that
# GrantChainResolver calls on every convention server dispatch. This demo
# proves that revocation is visible on re-walk (no caching).
#
# Transport note: delegation.Resolve uses SkipSync:true (reads local SQLite
# only). We issue a `cf read` before each resolve to sync the filesystem
# transport into the anchor's local store — this mirrors how a real convention
# server works (it subscribes to the campfire and has a fully-synced store).
#
# No relay or network required — all messages flow through the shared
# filesystem transport.
#
# Reference: docs/0.30-deal-breakers.md §D8, campfireagent-cee.
source "$(dirname "$0")/lib.sh"

export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"

trap cleanup EXIT

# ---------------------------------------------------------------------------
section "Setup: three-key delegation chain (anchor → mid → sender)"
# ---------------------------------------------------------------------------
ANCHOR_HOME=$(new_identity anchor);   register_cleanup "$ANCHOR_HOME"
MID_HOME=$(new_identity mid);         register_cleanup "$MID_HOME"
SENDER_HOME=$(new_identity sender);   register_cleanup "$SENDER_HOME"

ANCHOR_PUB=$(pubkey_of "$ANCHOR_HOME")
MID_PUB=$(pubkey_of "$MID_HOME")
SENDER_PUB=$(pubkey_of "$SENDER_HOME")

echo "Anchor: $ANCHOR_PUB"
echo "Mid:    $MID_PUB"
echo "Sender: $SENDER_PUB"

# Configure trust anchors on all three identities.
for home in "$ANCHOR_HOME" "$MID_HOME" "$SENDER_HOME"; do
    cat > "$home/config.toml" <<EOF
[identity.trust]
anchors = ["$ANCHOR_PUB"]
EOF
done
echo "Trust anchors configured"

# Create campfire; all three join.
CF_ID=$(create_campfire --cf-home "$ANCHOR_HOME")
echo "Campfire: $CF_ID"

cf join "$CF_ID" --cf-home "$MID_HOME"    2>/dev/null && echo "Mid joined"
cf join "$CF_ID" --cf-home "$SENDER_HOME" 2>/dev/null && echo "Sender joined"

# sync_anchor forces the anchor's local store to sync from the filesystem
# transport. delegation.Resolve uses SkipSync:true (reads local SQLite only);
# a real convention server would already have messages synced via Subscribe.
# This step mimics "server store is up to date before dispatch."
sync_anchor() {
    cf read "$CF_ID" --all --cf-home "$ANCHOR_HOME" >/dev/null 2>&1 || true
}

# ---------------------------------------------------------------------------
section "Step 1: Build the delegation chain"
# anchor grants mid, mid grants sender
# ---------------------------------------------------------------------------
echo "$ cf trust grant CF_ID MID_PUB --ttl 24h --cf-home ANCHOR_HOME"
cf trust grant "$CF_ID" "$MID_PUB" --ttl 24h --cf-home "$ANCHOR_HOME"
echo "anchor → mid: granted"

echo "$ cf trust grant CF_ID SENDER_PUB --ttl 24h --cf-home MID_HOME"
cf trust grant "$CF_ID" "$SENDER_PUB" --ttl 24h --cf-home "$MID_HOME"
echo "mid → sender: granted"

# Sync the anchor store to pick up all posted grants.
sync_anchor

# ---------------------------------------------------------------------------
section "Step 2 (Dispatch 1): resolve sender — expect Resolved"
# ---------------------------------------------------------------------------
echo ""
echo "Resolving sender before revocation (simulates dispatch 1)..."

set +e
RESOLVE_BEFORE_JSON=$(cf trust resolve "$CF_ID" "$SENDER_PUB" --cf-home "$ANCHOR_HOME" --json 2>/dev/null)
RESOLVE_BEFORE_EXIT=$?
set -e

echo "$RESOLVE_BEFORE_JSON"
RESOLVE_BEFORE_STATUS=$(echo "$RESOLVE_BEFORE_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('status','?'))" 2>/dev/null || echo "?")
assert_eq "dispatch 1: chain resolved (status=Resolved)" "Resolved" "$RESOLVE_BEFORE_STATUS"
assert_eq "dispatch 1: exit code 0" "0" "$RESOLVE_BEFORE_EXIT"

# ---------------------------------------------------------------------------
section "Step 3: Revoke mid-chain key (anchor revokes mid)"
# ---------------------------------------------------------------------------
echo ""
echo "Anchor posts identity:revoked for mid key..."
echo "$ cf trust revoke CF_ID MID_PUB --cf-home ANCHOR_HOME"
cf trust revoke "$CF_ID" "$MID_PUB" --cf-home "$ANCHOR_HOME"
echo "Revocation posted — mid key is now invalid"

# Sync the anchor store to pick up the revocation.
sync_anchor

# ---------------------------------------------------------------------------
section "Step 4 (Dispatch 2): resolve same sender — expect DeadEnd (not trusted)"
# ---------------------------------------------------------------------------
# The walk re-reads the store. The revocation is now visible.
# A caching implementation would incorrectly return Resolved here.
echo ""
echo "Resolving sender AFTER mid-chain revocation (simulates dispatch 2)..."

set +e
RESOLVE_AFTER_JSON=$(cf trust resolve "$CF_ID" "$SENDER_PUB" --cf-home "$ANCHOR_HOME" --json 2>/dev/null)
RESOLVE_AFTER_EXIT=$?
set -e

echo "$RESOLVE_AFTER_JSON"
RESOLVE_AFTER_STATUS=$(echo "$RESOLVE_AFTER_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('status','?'))" 2>/dev/null || echo "?")
assert_eq "dispatch 2: chain broken (status=DeadEnd)" "DeadEnd" "$RESOLVE_AFTER_STATUS"
assert_eq "dispatch 2: exit code 1 (not trusted)" "1" "$RESOLVE_AFTER_EXIT"

# ---------------------------------------------------------------------------
section "Adversarial case: revoke deepest key (mid revokes sender)"
# ---------------------------------------------------------------------------
ANCHOR3_HOME=$(new_identity anchor3);   register_cleanup "$ANCHOR3_HOME"
MID3_HOME=$(new_identity mid3);         register_cleanup "$MID3_HOME"
SENDER3_HOME=$(new_identity sender3);   register_cleanup "$SENDER3_HOME"

ANCHOR3_PUB=$(pubkey_of "$ANCHOR3_HOME")
MID3_PUB=$(pubkey_of "$MID3_HOME")
SENDER3_PUB=$(pubkey_of "$SENDER3_HOME")

for home in "$ANCHOR3_HOME" "$MID3_HOME" "$SENDER3_HOME"; do
    cat > "$home/config.toml" <<EOF
[identity.trust]
anchors = ["$ANCHOR3_PUB"]
EOF
done

CF3_ID=$(create_campfire --cf-home "$ANCHOR3_HOME")
cf join "$CF3_ID" --cf-home "$MID3_HOME"    2>/dev/null
cf join "$CF3_ID" --cf-home "$SENDER3_HOME" 2>/dev/null

cf trust grant "$CF3_ID" "$MID3_PUB"    --ttl 24h --cf-home "$ANCHOR3_HOME"
cf trust grant "$CF3_ID" "$SENDER3_PUB" --ttl 24h --cf-home "$MID3_HOME"

cf read "$CF3_ID" --all --cf-home "$ANCHOR3_HOME" >/dev/null 2>&1 || true

# Verify chain valid before revoke.
set +e
PRE3=$(cf trust resolve "$CF3_ID" "$SENDER3_PUB" --cf-home "$ANCHOR3_HOME" --json 2>/dev/null)
PRE3_EXIT=$?
set -e
PRE3_STATUS=$(echo "$PRE3" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('status','?'))" 2>/dev/null || echo "?")
assert_eq "adversarial setup: chain valid before deepest revoke" "Resolved" "$PRE3_STATUS"

# Revoke the deepest key: mid revokes sender (just-in-time deny).
echo "$ cf trust revoke CF3_ID SENDER3_PUB --cf-home MID3_HOME"
cf trust revoke "$CF3_ID" "$SENDER3_PUB" --cf-home "$MID3_HOME"
echo "Deepest key revocation posted"

cf read "$CF3_ID" --all --cf-home "$ANCHOR3_HOME" >/dev/null 2>&1 || true

set +e
POST3=$(cf trust resolve "$CF3_ID" "$SENDER3_PUB" --cf-home "$ANCHOR3_HOME" --json 2>/dev/null)
POST3_EXIT=$?
set -e
POST3_STATUS=$(echo "$POST3" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('status','?'))" 2>/dev/null || echo "?")
echo "$POST3"
assert_eq "adversarial: deepest key revoked (status=DeadEnd)" "DeadEnd" "$POST3_STATUS"
assert_eq "adversarial: exit code 1 (not trusted)" "1" "$POST3_EXIT"

# ---------------------------------------------------------------------------
summary
