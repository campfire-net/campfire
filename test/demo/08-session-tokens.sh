#!/usr/bin/env bash
# 08-session-tokens.sh — cfs2_ session token lifecycle (cf 0.30+).
#
# cf 0.30 migration: cfs1_ tokens (shared ephemeral key) are replaced by
# cfs2_ tokens (campfire address only, no shared private key). Workers
# participate via per-worker Ed25519 keys with lazy-mint grants.
#
# This demo tests: create → cfs2_ prefix → send → read → end.
source "$(dirname "$0")/lib.sh"

trap cleanup EXIT

section "Create session token (cfs2_)"
TOKEN=$(cf session create --ttl 1h 2>/dev/null)
echo "Token: ${TOKEN:0:35}..."
assert_contains "Token starts with cfs2_" "$TOKEN" "cfs2_"

section "Send messages via session (using cfs2_ token)"
cf session send "$TOKEN" "First session message" 2>/dev/null
echo "Sent message 1"
cf session send "$TOKEN" "Second session message" 2>/dev/null
echo "Sent message 2"

section "Read messages via session (using cfs2_ token)"
READ_OUT=$(cf session read "$TOKEN" 2>/dev/null)
assert_contains "Session read sees message 1" "$READ_OUT" "First session message"
assert_contains "Session read sees message 2" "$READ_OUT" "Second session message"

section "End session (using cfs2_ token)"
cf session end "$TOKEN" 2>/dev/null
echo "Session ended"

section "Read after end (should fail or return empty)"
POST_END=$(cf session read "$TOKEN" 2>&1 || true)
# After end, the session campfire is disbanded — reads should fail
echo "Post-end read: ${POST_END:0:80}"

section "Legacy cfs1_ token rejected with migration error"
FAKE_V1="cfs1_dGVzdA"
V1_ERR=$(cf session send "$FAKE_V1" "should not send" 2>&1 || true)
assert_contains "cfs1_ token rejected with migration message" "$V1_ERR" "cfs1_"

summary
