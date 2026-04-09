#!/usr/bin/env bash
# 08-session-tokens.sh — Zero-ceremony session campfire.
# Create token, send, read, end. The swarm coordination path.
source "$(dirname "$0")/lib.sh"

trap cleanup EXIT

section "Create session token"
TOKEN=$(cf session create --ttl 1h 2>/dev/null)
echo "Token: ${TOKEN:0:30}..."
assert_contains "Token starts with cfs1_" "$TOKEN" "cfs1_"

section "Send messages via session"
cf session send "$TOKEN" "First session message" 2>/dev/null
echo "Sent message 1"
cf session send "$TOKEN" "Second session message" 2>/dev/null
echo "Sent message 2"

section "Read messages via session"
READ_OUT=$(cf session read "$TOKEN" 2>/dev/null)
assert_contains "Session read sees message 1" "$READ_OUT" "First session message"
assert_contains "Session read sees message 2" "$READ_OUT" "Second session message"

section "End session"
cf session end "$TOKEN" 2>/dev/null
echo "Session ended"

section "Read after end (should fail or return empty)"
POST_END=$(cf session read "$TOKEN" 2>&1 || true)
# After end, the session campfire is disbanded — reads should fail
echo "Post-end read: ${POST_END:0:80}"

summary
