#!/usr/bin/env bash
# 01-filesystem-basics.sh — Two agents, filesystem transport.
# Create, join, send, read, ls, members. Baseline sanity check.
source "$(dirname "$0")/lib.sh"

trap cleanup EXIT

section "Setup: two identities"
ALICE_HOME=$(new_identity alice); register_cleanup "$ALICE_HOME"
BOB_HOME=$(new_identity bob); register_cleanup "$BOB_HOME"
echo "Alice: $(pubkey_of "$ALICE_HOME")"
echo "Bob:   $(pubkey_of "$BOB_HOME")"

section "Alice creates a filesystem campfire"
CF_ID=$(create_campfire --cf-home "$ALICE_HOME")
echo "Campfire: ${CF_ID:0:12}..."

section "Bob joins via filesystem"
cf join "$CF_ID" --cf-home "$BOB_HOME" 2>/dev/null
echo "Bob joined"

section "Alice sends a message"
cf send "$CF_ID" --cf-home "$ALICE_HOME" --tag greeting "Hello from Alice" 2>/dev/null
echo "Sent"

section "Bob reads messages"
READ_OUT=$(cf read "$CF_ID" --cf-home "$BOB_HOME" --json --all 2>/dev/null)
assert_contains "Bob sees Alice's message" "$READ_OUT" "Hello from Alice"

section "Bob sends a reply"
cf send "$CF_ID" --cf-home "$BOB_HOME" --tag reply "Hello from Bob" 2>/dev/null

section "Alice reads with tag filter"
TAG_OUT=$(cf read "$CF_ID" --cf-home "$ALICE_HOME" --json --all --tag reply 2>/dev/null)
assert_contains "Alice sees reply tag" "$TAG_OUT" "Hello from Bob"
NOTAG_OUT=$(cf read "$CF_ID" --cf-home "$ALICE_HOME" --json --all --tag nonexistent 2>/dev/null)
assert_not_contains "Tag filter excludes non-matching" "$NOTAG_OUT" "Hello"

section "cf ls shows campfire"
LS_OUT=$(cf ls --cf-home "$ALICE_HOME" 2>/dev/null)
echo "$LS_OUT"
assert_contains "ls shows campfire" "$LS_OUT" "${CF_ID:0:12}"
assert_contains "ls shows members" "$LS_OUT" "members"

section "cf members lists agents"
MEMBERS_OUT=$(cf members "$CF_ID" --cf-home "$ALICE_HOME" 2>/dev/null)
echo "$MEMBERS_OUT"
ALICE_PUB=$(pubkey_of "$ALICE_HOME")
BOB_PUB=$(pubkey_of "$BOB_HOME")
# cf members shows short hashes — check first 8 chars
assert_contains "Members shows Alice" "$MEMBERS_OUT" "${ALICE_PUB:0:8}"
assert_contains "Members shows Bob" "$MEMBERS_OUT" "${BOB_PUB:0:8}"

summary
