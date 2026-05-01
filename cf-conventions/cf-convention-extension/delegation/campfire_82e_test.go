package delegation_test

// campfire_82e_test.go — Regression tests for campfire-82e code quality fixes.
//
// Covered:
//   10a — protoMsgToRaw skips grants with invalid (non-hex) Sender
//   10c — ErrStoreRead inner error is inspectable via errors.Is

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extension/delegation"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// ---- 10a: protoMsgToRaw skips grants with invalid (non-hex) Sender ----

// TestProtoMsgToRaw_InvalidSenderHex verifies that a grant message record with a
// non-hex Sender field is skipped by findValidGrant (via Resolve) and does not
// cause a panic or a spurious error outcome. The invalid sender is an antipattern
// — this test is the regression for campfire-0c4.
func TestProtoMsgToRaw_InvalidSenderHex(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	childHex := hex.EncodeToString(env.pubkey(1))

	// Inject a grant message record with a non-hex Sender directly into the store.
	// This bypasses the normal Send path (which would reject the malformed sender).
	badSenderRec := store.MessageRecord{
		ID:          "bad-sender-hex-grant",
		CampfireID:  env.campfireHex,
		Sender:      "NOT-VALID-HEX!!!",
		Payload:     []byte(`{"child_pubkey":"` + childHex + `","campfire_id":"` + env.campfireHex + `","expires_at":9999999999}`),
		Tags:        []string{delegation.GrantedTag},
		Antecedents: []string{},
		Timestamp:   time.Now().UnixNano(),
		Signature:   []byte("irrelevant"),
		Provenance:  []message.ProvenanceHop{},
	}
	for _, s := range env.stores {
		s.AddMessage(badSenderRec) //nolint:errcheck
	}

	// Resolve should skip the bad-sender grant and return DeadEnd (no valid grant),
	// not panic or return an unexpected error type.
	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	_, ok := out.(delegation.DeadEnd)
	if !ok {
		t.Fatalf("expected DeadEnd (bad-sender grant skipped), got %T: %+v", out, out)
	}
}

// ---- 10c: ErrStoreRead inner error is inspectable via errors.Is ----

// TestResolve_StoreReadError_InnerErrorInspectable verifies that when the store
// returns an error, Resolve surfaces a ReadError whose Err field wraps both
// ErrStoreRead and the original store error, making both inspectable via errors.Is.
// This is the regression for campfire-b5a.
func TestResolve_StoreReadError_InnerErrorInspectable(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	// A sentinel error that the injected store will return.
	innerErr := errors.New("inner store read error sentinel")

	// Inject store error on first read via listErrAfterNStore.
	// listErrAfterNStore is defined in trust_test.go (same package).
	errStore := &listErrAfterNStore{
		Store: env.stores[1],
		n:     1,
		err:   innerErr,
	}
	faultyClient := protocol.New(errStore, env.identities[1])

	out := delegation.Resolve(ctx, faultyClient, env.campfireIDRaw, env.pubkey(1), env.anchors(0))

	re, ok := out.(delegation.ReadError)
	if !ok {
		t.Fatalf("expected ReadError, got %T: %+v", out, out)
	}

	// ErrStoreRead must be inspectable.
	if !errors.Is(re.Err, delegation.ErrStoreRead) {
		t.Errorf("errors.Is(re.Err, ErrStoreRead) = false; want true")
	}
	// The original inner error must also be inspectable via errors.Is
	// (requires %w wrapping — regression for campfire-b5a).
	if !errors.Is(re.Err, innerErr) {
		t.Errorf("errors.Is(re.Err, innerErr) = false; want true (inner error not wrapped with %%w)")
	}
}
