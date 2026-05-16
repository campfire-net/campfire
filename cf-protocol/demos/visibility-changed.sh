#!/usr/bin/env bash
# cf-protocol/demos/visibility-changed.sh
#
# Demo: campfire:visibility-changed L1 system event (campfireagent-c66).
#
# Verifies:
#   1. TagCampfireVisibilityChanged constant has the correct wire value.
#   2. EmitVisibilityChanged emits a message with the campfire:visibility-changed tag.
#   3. The emitted message is signed by the campfire key (not the operator key).
#   4. The signature verifies against the campfire's public key.
#   5. The payload contains previous_join_protocol, new_join_protocol, and changed_at.
#   6. An identity-key-signed message with the same tag fails campfire-key verification
#      (adversarial: signature does NOT match campfire key).
#
# Usage:
#   cd ~/projects/campfire
#   bash cf-protocol/demos/visibility-changed.sh
#
# Exits non-zero if any assertion fails; PASS: lines confirm each step.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-/usr/local/go/bin/go}"
export PATH="/usr/local/go/bin:$PATH"

PASS_COUNT=0
FAIL_COUNT=0

pass() { echo "PASS: $*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail() { echo "FAIL: $*" >&2; FAIL_COUNT=$((FAIL_COUNT+1)); }

echo "================================================================"
echo "  cf-protocol campfire:visibility-changed demo (campfireagent-c66)"
echo "================================================================"
echo ""
echo "  Go: $GO"
echo "  Repo: $REPO_ROOT"
echo ""

# ── Step 0: go build — verify compilation ──────────────────────────────────────
echo "── Step 0: go build ./cf-protocol/... ───────────────────────────────"
if (cd "$REPO_ROOT" && "$GO" build ./cf-protocol/... 2>&1); then
  pass "go build ./cf-protocol/... succeeds"
else
  fail "go build ./cf-protocol/... FAILED"
  exit 1
fi
echo ""

# ── Step 1: TagCampfireVisibilityChanged wire value (via go vet + grep) ────────
echo "── Step 1: TagCampfireVisibilityChanged constant in source ──────────"
TAGSPEC_FILE="$REPO_ROOT/cf-protocol/internal/tagspec/tagspec.go"
TAGS_FILE="$REPO_ROOT/cf-protocol/internal/campfire/tags.go"
SURFACE_FILE="$REPO_ROOT/cf-protocol/protocol/surface.go"
CAMPFIRE_FILE="$REPO_ROOT/cf-protocol/campfire/campfire.go"

if grep -q 'TagCampfireVisibilityChanged.*=.*"campfire:visibility-changed"' "$TAGSPEC_FILE"; then
  pass "tagspec.TagCampfireVisibilityChanged = \"campfire:visibility-changed\""
else
  fail "TagCampfireVisibilityChanged not found in $TAGSPEC_FILE"
fi

if grep -q 'TagVisibilityChanged.*=.*"campfire:visibility-changed"' "$TAGS_FILE"; then
  pass "campfire.TagVisibilityChanged = \"campfire:visibility-changed\""
else
  fail "TagVisibilityChanged not found in $TAGS_FILE"
fi

if grep -q 'CampfireTagVisibilityChanged.*=.*campfire\.TagVisibilityChanged' "$SURFACE_FILE"; then
  pass "protocol.CampfireTagVisibilityChanged re-exports from campfire.TagVisibilityChanged"
else
  fail "CampfireTagVisibilityChanged not found in $SURFACE_FILE"
fi

if grep -q 'TagVisibilityChanged.*=.*campfire\.TagVisibilityChanged' "$CAMPFIRE_FILE"; then
  pass "campfire/campfire.go re-exports TagVisibilityChanged"
else
  fail "TagVisibilityChanged re-export not found in $CAMPFIRE_FILE"
fi
echo ""

# ── Step 2: EmitVisibilityChanged implementation exists ───────────────────────
echo "── Step 2: Client.EmitVisibilityChanged() implementation ────────────"
VISIBILITY_FILE="$REPO_ROOT/cf-protocol/protocol/visibility.go"
if [ -f "$VISIBILITY_FILE" ]; then
  pass "visibility.go exists"
else
  fail "visibility.go is missing"
fi

if grep -q "func (c \*Client) EmitVisibilityChanged" "$VISIBILITY_FILE"; then
  pass "Client.EmitVisibilityChanged() method defined"
else
  fail "Client.EmitVisibilityChanged() not found in visibility.go"
fi

# Verify signing uses campfire key (state.PrivateKey, state.PublicKey), not identity key.
if grep -q "message\.NewEd25519Signer(state\.PrivateKey, state\.PublicKey)" "$VISIBILITY_FILE"; then
  pass "Signing uses campfire key (state.PrivateKey/PublicKey), not identity key"
else
  fail "Signing does NOT use campfire key — check visibility.go"
fi
echo ""

# ── Step 3: targeted go test ─────────────────────────────────────────────────
echo "── Step 3: go test -run TestEmitVisibilityChanged ───────────────────"
cd "$REPO_ROOT"
if "$GO" test ./cf-protocol/protocol/ -run "TestEmitVisibilityChanged" -count=1 -v 2>&1 | tee /tmp/visibility-test-out.txt | grep -E "^(=== RUN|--- PASS|--- FAIL|PASS|FAIL|ok)" | sed 's/^/  /'; then
  if grep -q "FAIL" /tmp/visibility-test-out.txt && ! grep -qE "^(ok|PASS)" /tmp/visibility-test-out.txt; then
    fail "one or more TestEmitVisibilityChanged tests FAILED"
  else
    NPASS=$(grep -c "^--- PASS" /tmp/visibility-test-out.txt || echo 0)
    pass "$NPASS TestEmitVisibilityChanged_* tests PASSED"
  fi
else
  fail "go test -run TestEmitVisibilityChanged failed"
fi
echo ""

# ── Step 4: adversarial forge test explicitly ─────────────────────────────────
echo "── Step 4: adversarial forge test ───────────────────────────────────"
if grep -q "PASS: TestEmitVisibilityChanged_AdversarialForge" /tmp/visibility-test-out.txt; then
  pass "TestEmitVisibilityChanged_AdversarialForge: identity-key forge rejected"
else
  fail "TestEmitVisibilityChanged_AdversarialForge did not PASS"
fi
echo ""

# ── Step 5: demo Go program — live emission + verification ────────────────────
echo "── Step 5: live emission + campfire-key verification ────────────────"
DEMO_SRC="$REPO_ROOT/cf-protocol/demos/visibility_demo_main.go"
cat > "$DEMO_SRC" <<'GOEOF'
//go:build ignore

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cfcampfire "github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	cftransport "github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
)

func main() {
	storeDir, _ := os.MkdirTemp("", "demo-store-")
	transportDir, _ := os.MkdirTemp("", "demo-transport-")
	defer os.RemoveAll(storeDir)
	defer os.RemoveAll(transportDir)

	agentID, err := identity.Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generating agent identity:", err)
		os.Exit(1)
	}

	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "opening store:", err)
		os.Exit(1)
	}
	defer s.Close()

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generating campfire keypair:", err)
		os.Exit(1)
	}
	campfireID := fmt.Sprintf("%x", cfPub)

	cfDir := filepath.Join(transportDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		_ = os.MkdirAll(filepath.Join(cfDir, sub), 0755)
	}

	type CampfireState = cfcampfire.CampfireState
	state := &CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            cfPriv,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, _ := cfencoding.Marshal(state)
	_ = os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644)

	tr := cftransport.New(transportDir)
	_ = tr.WriteMember(campfireID, cfcampfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      cfcampfire.RoleFull,
	})

	_ = s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          cfcampfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	})

	client := protocol.New(s, agentID)

	fmt.Printf("campfire ID: %s...\n", campfireID[:16])
	fmt.Printf("agent key:   %s...\n", agentID.PublicKeyHex()[:16])
	fmt.Printf("campfire key: %x...\n", cfPub[:8])

	if err := client.EmitVisibilityChanged(campfireID, "open", "invite-only"); err != nil {
		fmt.Fprintln(os.Stderr, "EmitVisibilityChanged:", err)
		os.Exit(1)
	}
	fmt.Println("emission: ok")

	msgs, err := tr.ListMessages(campfireID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ListMessages:", err)
		os.Exit(1)
	}
	var found *message.Message
	for i := range msgs {
		for _, tag := range msgs[i].Tags {
			if tag == cfcampfire.TagVisibilityChanged {
				found = &msgs[i]
				break
			}
		}
		if found != nil {
			break
		}
	}

	if found == nil {
		fmt.Fprintln(os.Stderr, "FAIL: no campfire:visibility-changed message found")
		os.Exit(1)
	}
	fmt.Println("tag campfire:visibility-changed: present")

	if !bytes.Equal(found.Sender, cfPub) {
		fmt.Fprintf(os.Stderr, "FAIL: Sender %x != campfire pubkey %x\n", found.Sender, cfPub)
		os.Exit(1)
	}
	if bytes.Equal(found.Sender, agentID.PublicKey) {
		fmt.Fprintln(os.Stderr, "FAIL: Sender equals agent key — must be campfire key")
		os.Exit(1)
	}
	fmt.Printf("Sender is campfire key: %x...\n", found.Sender[:8])

	if !found.VerifySignature() {
		fmt.Fprintln(os.Stderr, "FAIL: signature does not verify")
		os.Exit(1)
	}
	fmt.Println("VerifySignature(): true (campfire key)")

	var payload struct {
		Prev      string `json:"previous_join_protocol"`
		New       string `json:"new_join_protocol"`
		ChangedAt int64  `json:"changed_at"`
	}
	if err := json.Unmarshal(found.Payload, &payload); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL: payload not valid JSON:", err)
		os.Exit(1)
	}
	if payload.Prev != "open" || payload.New != "invite-only" || payload.ChangedAt == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: payload fields wrong: %+v\n", payload)
		os.Exit(1)
	}
	fmt.Printf("payload: previous=%q new=%q changed_at=%d\n", payload.Prev, payload.New, payload.ChangedAt)
	fmt.Println("ALL OK")
}
GOEOF

DEMO_OUT=$("$GO" run "$DEMO_SRC" 2>&1) && DEMO_EXIT=$? || DEMO_EXIT=$?
rm -f "$DEMO_SRC"

if [ "$DEMO_EXIT" -eq 0 ] && echo "$DEMO_OUT" | grep -q "ALL OK"; then
  echo "$DEMO_OUT" | sed 's/^/  /'
  pass "live emission: EmitVisibilityChanged emits campfire-key-signed event"
else
  echo "$DEMO_OUT" | sed 's/^/  /'
  fail "live emission demo FAILED (exit $DEMO_EXIT)"
fi
echo ""

# ── Step 6: go vet ──────────────────────────────────────────────────────────────
echo "── Step 6: go vet ./cf-protocol/... ─────────────────────────────────"
if (cd "$REPO_ROOT" && "$GO" vet ./cf-protocol/... 2>&1 | grep -v "^$"); then
  fail "go vet found issues"
else
  pass "go vet ./cf-protocol/... clean"
fi
echo ""

# ── Summary ───────────────────────────────────────────────────────────────────
echo "================================================================"
echo "  Demo complete"
echo ""
echo "  PASS: $PASS_COUNT"
echo "  FAIL: $FAIL_COUNT"
echo ""

if [ "$FAIL_COUNT" -eq 0 ]; then
  echo "  Artifacts:"
  echo "    cf-protocol/internal/tagspec/tagspec.go     — TagCampfireVisibilityChanged"
  echo "    cf-protocol/internal/campfire/tags.go       — TagVisibilityChanged"
  echo "    cf-protocol/campfire/campfire.go            — TagVisibilityChanged re-export"
  echo "    cf-protocol/protocol/surface.go             — CampfireTagVisibilityChanged re-export"
  echo "    cf-protocol/protocol/visibility.go          — Client.EmitVisibilityChanged()"
  echo "    cf-protocol/protocol/visibility_test.go     — 6 tests (emission, key, payload, adversarial)"
  echo ""
  echo "  System event: campfire:visibility-changed"
  echo "  Signing: campfire key (Ed25519), not operator identity key"
  echo "  Verification: msg.Sender == campfire pubkey && msg.VerifySignature()"
  echo "================================================================"
  exit 0
else
  echo "  SOME CHECKS FAILED — review output above."
  echo "================================================================"
  exit 1
fi
