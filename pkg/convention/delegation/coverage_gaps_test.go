package delegation_test

// coverage_gaps_test.go — Pure test additions for six uncovered paths (campfire-f22).
//
// Covered:
//   5a — findValidGrant skips malformed-payload grant and uses valid candidate (1ca)
//   5b — ValidateGrant returns "grant payload" error on non-JSON payload (b3a)
//   5c — isRevoked skips malformed-payload revoke and applies valid one (fd3)

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention/delegation"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// ---- 5a: findValidGrant skips grants with malformed payloads ----

// TestFindValidGrant_MalformedPayload verifies that findValidGrant (exercised
// via Resolve) skips an identity:granted message whose payload is not valid JSON,
// and continues to a second valid grant from the same parent.
//
// This proves the json.Unmarshal error path (line "continue" in findValidGrant) —
// a malformed payload is silently skipped and does not produce an error outcome.
func TestFindValidGrant_MalformedPayload(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	childHex := hex.EncodeToString(env.pubkey(1))
	parentHex := hex.EncodeToString(env.pubkey(0))

	// Inject a grant record with non-JSON payload into all stores.
	// Timestamp is older so the valid grant (posted via postGrant below) sorts first.
	malformedRec := store.MessageRecord{
		ID:          "malformed-payload-grant",
		CampfireID:  env.campfireHex,
		Sender:      parentHex,
		Payload:     []byte(`not-json-at-all`),
		Tags:        []string{delegation.GrantedTag},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano() - int64(time.Second),
		Signature:   []byte("irrelevant-sig"),
		Provenance:  []message.ProvenanceHop{},
	}
	for _, s := range env.stores {
		s.AddMessage(malformedRec) //nolint:errcheck
	}

	// Post a valid grant so there is a good candidate alongside the malformed one.
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())

	// Resolve should succeed by skipping the malformed grant and using the valid one.
	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved (malformed grant skipped), got %T: %+v", out, out)
	}
	if !r.Anchor.Equal(env.pubkey(0)) {
		t.Errorf("expected anchor to be ids[0]")
	}
	_ = childHex
}

// ---- 5b: ValidateGrant returns error on non-JSON payload ----

// TestValidateGrant_PayloadParseError verifies that ValidateGrant returns an error
// containing "grant payload" when the message is correctly signed but the payload
// bytes are not valid JSON.
//
// Rule 1 (signature) passes because the message is properly signed.
// The error is surfaced from ParseGrantPayload: "grant payload: <json err>".
func TestValidateGrant_PayloadParseError(t *testing.T) {
	pub, priv := generateKey(t)

	// non-JSON payload bytes, signed correctly so Rule 1 passes.
	nonJSONPayload := []byte(`not-valid-json!!!`)
	grant := newSignedGrant(t, priv, pub, nonJSONPayload)

	// Use a fixed campfire hex and a far-future reference time so rules 3/4 would
	// not fire before the parse error — but the parse error fires first anyway.
	testNow := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	err := delegation.ValidateGrant(grant, campfireHex, testNow)
	if err == nil {
		t.Fatal("expected error for non-JSON payload, got nil")
	}
	if !strings.Contains(err.Error(), "grant payload") {
		t.Errorf("expected error to contain \"grant payload\", got: %v", err)
	}
}

// ---- 5c: isRevoked skips malformed-payload revocations ----

// TestIsRevoked_MalformedRevokePayload verifies that isRevoked (exercised via Resolve)
// skips an identity:revoked message whose payload is not valid JSON, while still
// applying a valid revocation posted alongside it.
//
// Both revocations target the same child. The malformed one is skipped; the valid
// one takes effect, causing the grant to be treated as revoked → DeadEnd.
func TestIsRevoked_MalformedRevokePayload(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// Post a valid grant so there is something to revoke.
	env.postGrant(t, 0, env.pubkey(1), futureExpiry())

	childHex := hex.EncodeToString(env.pubkey(1))
	parentHex := hex.EncodeToString(env.pubkey(0))

	// Inject a revoke message with non-JSON payload (should be skipped by isRevoked).
	malformedRevoke := store.MessageRecord{
		ID:          "malformed-revoke-payload",
		CampfireID:  env.campfireHex,
		Sender:      parentHex,
		Payload:     []byte(`not-json-payload`),
		Tags:        []string{delegation.RevokedTag},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano() + int64(time.Millisecond),
		Signature:   []byte("irrelevant"),
		Provenance:  []message.ProvenanceHop{},
	}
	for _, s := range env.stores {
		s.AddMessage(malformedRevoke) //nolint:errcheck
	}

	// Resolve with only the malformed revoke present: the grant should NOT be revoked
	// because the malformed revoke is skipped. Result should be Resolved.
	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	_, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved (malformed revoke skipped), got %T: %+v", out, out)
	}

	// Now also post a valid revocation and sync. The valid one should take effect.
	env.postRevoke(t, 0, env.pubkey(1))

	out = delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	_, ok = out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd (valid revoke applied), got %T: %+v", out, out)
	}
	_ = childHex
}

