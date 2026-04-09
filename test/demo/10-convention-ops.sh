#!/usr/bin/env bash
# 10-convention-ops.sh — Typed convention operations on a campfire.
# Declare convention, send typed operations, read with convention tags.
source "$(dirname "$0")/lib.sh"

trap cleanup EXIT

section "Setup"
ALICE_HOME=$(new_identity alice); register_cleanup "$ALICE_HOME"
BOB_HOME=$(new_identity bob); register_cleanup "$BOB_HOME"

section "Alice creates a filesystem campfire"
CF_ID=$(create_campfire --cf-home "$ALICE_HOME")
echo "Campfire: ${CF_ID:0:12}..."

section "Bob joins"
cf join "$CF_ID" --cf-home "$BOB_HOME" 2>/dev/null

section "Alice sends tagged messages"
cf send "$CF_ID" --cf-home "$ALICE_HOME" --tag "proposal" --tag "design" "Proposal: use REST" 2>/dev/null
cf send "$CF_ID" --cf-home "$ALICE_HOME" --tag "status" "System nominal" 2>/dev/null

section "Bob reads only proposals"
PROP_OUT=$(cf read "$CF_ID" --cf-home "$BOB_HOME" --json --all --tag "proposal" 2>/dev/null)
assert_contains "Proposal tag filter works" "$PROP_OUT" "Proposal: use REST"
assert_not_contains "Non-proposal excluded" "$PROP_OUT" "System nominal"

section "Bob reads only status"
STATUS_OUT=$(cf read "$CF_ID" --cf-home "$BOB_HOME" --json --all --tag "status" 2>/dev/null)
assert_contains "Status tag filter works" "$STATUS_OUT" "System nominal"
assert_not_contains "Proposal excluded" "$STATUS_OUT" "Proposal"

section "Multiple tag filtering"
cf send "$CF_ID" --cf-home "$ALICE_HOME" --tag "priority" --tag "urgent" "Urgent priority item" 2>/dev/null
cf send "$CF_ID" --cf-home "$ALICE_HOME" --tag "priority" "Normal priority item" 2>/dev/null
URGENT=$(cf read "$CF_ID" --cf-home "$BOB_HOME" --json --all --tag "urgent" 2>/dev/null)
assert_contains "Urgent filter matches" "$URGENT" "Urgent priority item"
assert_not_contains "Normal excluded from urgent" "$URGENT" "Normal priority item"

summary
