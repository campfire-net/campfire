package delegation_test

// Regression tests for campfire-4c7: signature verification on read paths.
//
// Test 13a — buildRevokedSet must reject forged revocations (campfire-c62).
// Test 13b — isAlreadyLinked signature verification is tested in pkg/protocol.

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/delegation"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// TestBuildRevokedSet_RejectsForgedRevocation verifies that buildRevokedSet
// (called inside findValidGrant via Resolve) ignores revocation messages whose
// signatures do not verify — even when msg.Sender claims to be the legitimate
// parent.
//
// Exploit scenario (campfire-c62): on a filesystem transport an attacker can
// write an arbitrary file into the messages/ directory. The crafted message has
// the correct identity:revoked tag and a valid JSON payload naming the child,
// but its Signature field is either missing or signed by a different key. Before
// the fix, buildRevokedSet trusted msg.Sender unconditionally — the forged
// revocation would be treated as authoritative and a valid grant would be
// suppressed, causing Resolve to return DeadEnd instead of Resolved.
func TestBuildRevokedSet_RejectsForgedRevocation(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// Post a legitimate grant: ids[0] → ids[1].
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())

	// Confirm the grant resolves before injecting the forged revocation.
	out := delegation.Resolve(ctx, env.clients[0], env.campfireIDRaw, env.pubkey(1), env.anchors(0))
	if _, ok := out.(delegation.Resolved); !ok {
		t.Fatalf("pre-condition: expected Resolved before forged revocation, got %T", out)
	}

	// Inject a forged revocation: msg.Sender claims to be ids[0] (the parent),
	// but the Signature field is garbage — VerifySignature will reject it.
	// This simulates an attacker writing directly to the filesystem transport.
	childHex := hex.EncodeToString(env.pubkey(1))
	parentHex := hex.EncodeToString(env.pubkey(0))
	forgedRevoke := store.MessageRecord{
		ID:          "forged-revocation-campfire-c62",
		CampfireID:  env.campfireHex,
		Sender:      parentHex,
		Payload:     []byte(`{"child_pubkey":"` + childHex + `"}`),
		Tags:        []string{delegation.RevokedTag},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Signature:   []byte("forgedsignaturevalue"), // invalid — does not verify
		Provenance:  []message.ProvenanceHop{},
	}
	for _, s := range env.stores {
		s.AddMessage(forgedRevoke) //nolint:errcheck
	}

	// Resolve must still return Resolved — the forged revocation must be ignored.
	out = delegation.Resolve(ctx, env.clients[0], env.campfireIDRaw, env.pubkey(1), env.anchors(0))
	if _, ok := out.(delegation.Resolved); !ok {
		t.Errorf("expected Resolved after forged revocation injection, got %T: %+v — forged revocation was not ignored (campfire-c62 regression)", out, out)
	}
}

// TestBuildRevokedSet_AcceptsValidRevocation confirms that a properly signed
// revocation message (posted via the normal Send path) is still honoured after
// the signature verification fix. This is the complementary positive-path test.
func TestBuildRevokedSet_AcceptsValidRevocation(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// Grant and immediately revoke using the normal signed path.
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())
	env.postRevoke(t, 0, env.pubkey(1))

	// Resolve must return DeadEnd — the valid revocation is honoured.
	out := delegation.Resolve(ctx, env.clients[0], env.campfireIDRaw, env.pubkey(1), env.anchors(0))
	if _, ok := out.(delegation.DeadEnd); !ok {
		t.Errorf("expected DeadEnd after valid revocation, got %T: %+v — valid revocation was not applied", out, out)
	}
}
