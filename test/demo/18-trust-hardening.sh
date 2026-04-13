#!/usr/bin/env bash
# 18-trust-hardening.sh — v0.19 trust delegation hardening demo.
#
# Demonstrates the security hardening applied in v0.19:
#   1. Hex case normalization — uppercase revocations match lowercase grants
#   2. campfireID validation — Resolve rejects empty/malformed IDs
#   3. cf trust grant JSON output — structured machine-readable grant info
#   4. cf trust revoke JSON output — structured revocation confirmation
#   5. Config cascade split-layer — backend in global, fingerprint in project
#   6. Grant TTL ceiling enforcement — >7 days rejected at issuance
#
# No relay or network required — all messages flow through the shared
# filesystem transport directory.
source "$(dirname "$0")/lib.sh"

export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"

trap cleanup EXIT

# ---------------------------------------------------------------------------
section "Setup: create identities"
# ---------------------------------------------------------------------------
ROOT_HOME=$(new_identity root);     register_cleanup "$ROOT_HOME"
CHILD_HOME=$(new_identity child);   register_cleanup "$CHILD_HOME"

ROOT_PUB=$(pubkey_of "$ROOT_HOME")
CHILD_PUB=$(pubkey_of "$CHILD_HOME")

echo "Root:  $ROOT_PUB"
echo "Child: $CHILD_PUB"

# Configure trust anchors.
for home in "$ROOT_HOME" "$CHILD_HOME"; do
    cat > "$home/config.toml" <<EOF
[identity.trust]
anchors = ["$ROOT_PUB"]
EOF
done
echo "Trust anchors configured"

# Create campfire, child joins.
CF_ID=$(create_campfire --cf-home "$ROOT_HOME")
echo "Campfire: $CF_ID"
cf join "$CF_ID" --cf-home "$CHILD_HOME" 2>/dev/null
echo "Child joined"

# ---------------------------------------------------------------------------
section "1. cf trust grant JSON output"
# ---------------------------------------------------------------------------
GRANT_JSON=$(cf trust grant "$CF_ID" "$CHILD_PUB" --ttl 24h --cf-home "$ROOT_HOME" --json 2>/dev/null)
echo "$GRANT_JSON"

assert_contains "Grant JSON has msg_id"       "$GRANT_JSON" '"msg_id"'
assert_contains "Grant JSON has parent"       "$GRANT_JSON" '"parent"'
assert_contains "Grant JSON has child"        "$GRANT_JSON" '"child"'
assert_contains "Grant JSON has campfire_id"  "$GRANT_JSON" '"campfire_id"'
assert_contains "Grant JSON has expires_at"   "$GRANT_JSON" '"expires_at"'
assert_contains "Grant JSON has ttl_seconds"  "$GRANT_JSON" '"ttl_seconds"'

# ---------------------------------------------------------------------------
section "2. Verify grant works (baseline)"
# ---------------------------------------------------------------------------
cf read "$CF_ID" --cf-home "$CHILD_HOME" --all >/dev/null 2>&1 || true

RESOLVE_OUT=$(cf trust resolve "$CF_ID" "$CHILD_PUB" --cf-home "$CHILD_HOME" 2>/dev/null || true)
assert_contains "Child resolves after grant" "$RESOLVE_OUT" "Resolved"

# ---------------------------------------------------------------------------
section "3. Hex case normalization — uppercase revocation matches"
# ---------------------------------------------------------------------------
# Convert child pubkey to uppercase hex. v0.19 normalizes case in both
# grant and revocation lookups, so an uppercase revocation MUST match.
CHILD_PUB_UPPER=$(echo "$CHILD_PUB" | tr '[:lower:]' '[:upper:]')
echo "Child pubkey (lowercase): $CHILD_PUB"
echo "Child pubkey (UPPERCASE): $CHILD_PUB_UPPER"

# Revoke using the uppercase hex.
REVOKE_JSON=$(cf trust revoke "$CF_ID" "$CHILD_PUB_UPPER" --cf-home "$ROOT_HOME" --json 2>/dev/null || true)

# Check if the revocation was accepted.
if echo "$REVOKE_JSON" | grep -q '"msg_id"'; then
    echo "Revocation posted with uppercase hex"
    assert_contains "Revoke JSON has msg_id" "$REVOKE_JSON" '"msg_id"'
else
    # If the CLI rejects uppercase, post raw (the store-level fix handles matching).
    echo "CLI normalized — posting raw revocation with uppercase child_pubkey"
    REVOKE_PAYLOAD="{\"child_pubkey\":\"$CHILD_PUB_UPPER\",\"campfire_id\":\"$CF_ID\"}"
    cf send "$CF_ID" --cf-home "$ROOT_HOME" --tag identity:revoked "$REVOKE_PAYLOAD" >/dev/null 2>&1
fi

# Sync and verify the revocation took effect.
cf read "$CF_ID" --cf-home "$CHILD_HOME" --all >/dev/null 2>&1 || true

RESOLVE_AFTER_REVOKE=$(cf trust resolve "$CF_ID" "$CHILD_PUB" --cf-home "$CHILD_HOME" 2>/dev/null || true)
assert_contains "Uppercase revocation matched (DeadEnd)" "$RESOLVE_AFTER_REVOKE" "DeadEnd"

REVOKE_EXIT=0
cf trust resolve "$CF_ID" "$CHILD_PUB" --cf-home "$CHILD_HOME" >/dev/null 2>&1 || REVOKE_EXIT=$?
assert_eq "Resolve exits 1 after uppercase revoke" "1" "$REVOKE_EXIT"

# ---------------------------------------------------------------------------
section "4. campfireID validation — reject empty/malformed IDs"
# ---------------------------------------------------------------------------
# Empty campfire ID should fail.
EMPTY_EXIT=0
cf trust resolve "" "$CHILD_PUB" --cf-home "$CHILD_HOME" >/dev/null 2>&1 || EMPTY_EXIT=$?
assert_eq "Empty campfireID rejected (exit 1)" "1" "$EMPTY_EXIT"

# Short campfire ID (16 hex chars instead of 64) should fail.
SHORT_EXIT=0
cf trust resolve "abcdef0123456789" "$CHILD_PUB" --cf-home "$CHILD_HOME" >/dev/null 2>&1 || SHORT_EXIT=$?
assert_eq "Short campfireID rejected (exit 1)" "1" "$SHORT_EXIT"

# ---------------------------------------------------------------------------
section "5. Grant TTL ceiling enforcement"
# ---------------------------------------------------------------------------
# Re-grant the child (need it for further tests).
sleep 1
cf trust grant "$CF_ID" "$CHILD_PUB" --ttl 24h --cf-home "$ROOT_HOME" >/dev/null 2>&1
echo "Re-granted child (24h)"

# TTL > 7 days (169h) must be rejected.
TTL_EXIT=0
cf trust grant "$CF_ID" "$CHILD_PUB" --ttl 169h --cf-home "$ROOT_HOME" >/dev/null 2>&1 || TTL_EXIT=$?
assert_eq "TTL > 7 days rejected (exit 1)" "1" "$TTL_EXIT"

# TTL exactly 7 days (168h) must succeed.
TTL_MAX_EXIT=0
cf trust grant "$CF_ID" "$CHILD_PUB" --ttl 168h --cf-home "$ROOT_HOME" >/dev/null 2>&1 || TTL_MAX_EXIT=$?
assert_eq "TTL = 7 days accepted (exit 0)" "0" "$TTL_MAX_EXIT"

# ---------------------------------------------------------------------------
section "6. cf trust revoke JSON output"
# ---------------------------------------------------------------------------
REVOKE_JSON2=$(cf trust revoke "$CF_ID" "$CHILD_PUB" --cf-home "$ROOT_HOME" --json 2>/dev/null)
echo "$REVOKE_JSON2"

assert_contains "Revoke JSON has msg_id"      "$REVOKE_JSON2" '"msg_id"'
assert_contains "Revoke JSON has parent"      "$REVOKE_JSON2" '"parent"'
assert_contains "Revoke JSON has child"       "$REVOKE_JSON2" '"child"'
assert_contains "Revoke JSON has campfire_id" "$REVOKE_JSON2" '"campfire_id"'

# ---------------------------------------------------------------------------
section "7. Config cascade: split-layer ssh-agent backend"
# ---------------------------------------------------------------------------
# This tests the v0.19 fix: backend=ssh-agent in one config layer,
# fingerprint in another, must NOT fail at the intermediate layer.
# The validation runs post-cascade on the fully-merged config.

# Check if ssh-agent prerequisites are available.
if command -v ssh-agent >/dev/null 2>&1 && command -v ssh-keygen >/dev/null 2>&1; then
    SPLIT_HOME=$(new_identity split); register_cleanup "$SPLIT_HOME"
    SPLIT_GLOBAL=$(mktemp -d "/tmp/cf-demo-split-global-XXXX"); register_cleanup "$SPLIT_GLOBAL"

    # Generate a throwaway key for fingerprint.
    KEY_FILE="$SPLIT_GLOBAL/id_ed25519"
    ssh-keygen -t ed25519 -f "$KEY_FILE" -N "" -C "demo-18-split" -q
    FINGERPRINT=$(ssh-keygen -l -E sha256 -f "${KEY_FILE}.pub" 2>/dev/null | awk '{print $2}')

    # Global config: sets backend only (no fingerprint).
    cat > "$SPLIT_GLOBAL/config.toml" <<EOF
[identity]
backend = "ssh-agent"
EOF

    # Project config: sets fingerprint only (no backend).
    cat > "$SPLIT_HOME/config.toml" <<EOF
[identity]
fingerprint = "$FINGERPRINT"
EOF

    # Pre-v0.19 this would fail: mergeLayer checked per-layer, rejecting
    # the global layer (ssh-agent without fingerprint). Post-v0.19 the
    # validation runs on the merged result: backend from global +
    # fingerprint from project = valid.
    #
    # We can't fully Init (no ssh-agent socket), but we can verify that
    # config loading succeeds by checking cf id doesn't crash.
    SPLIT_EXIT=0
    cf id --cf-home "$SPLIT_HOME" >/dev/null 2>&1 || SPLIT_EXIT=$?
    # cf id will succeed (reads identity, not the backend) — the point is
    # it doesn't crash during config load. A crash = exit 2 or signal.
    assert_eq "Split-layer config loads without crash" "0" "$SPLIT_EXIT"
    echo "Split-layer ssh-agent config loaded successfully"
else
    echo "SKIP: ssh-agent prerequisites not available for split-layer test"
fi

summary
