package protocol

// forward_security_internal_test.go — internal-package adversarial test for
// forwardMessage's ingress validation (campfireagent-3d0 defense-in-depth).
//
// fs.WriteMessage validates msg.ID at the transport layer; forwardMessage
// validates again at the protocol ingress so a malformed ID never reaches
// membership lookups, transport resolution, or the local store mirror. This
// test exercises that ingress path directly.

import (
	"errors"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/internal/message"
	"github.com/campfire-net/campfire/pkg/identity"
)

// TestForwardMessage_RejectsTraversalID verifies that forwardMessage rejects
// a Message whose ID is not a UUID before any store or transport call. The
// returned error must wrap message.ErrInvalidMessageID so callers (and tests)
// can match it semantically.
//
// The client is intentionally zero-valued except for an identity stub: the
// validation must fire before identity is even consulted. If it didn't, we'd
// see a nil-pointer panic or a different error from the store lookup.
func TestForwardMessage_RejectsTraversalID(t *testing.T) {
	// We need a non-nil identity for forwardMessage to proceed past its first
	// guard. Anything beyond that is irrelevant — the validation rejects before
	// we touch the store or transport.
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	c := &Client{
		identity: id,
	}

	cases := []struct {
		name string
		id   string
	}{
		{"path-traversal", "../../../../etc/pwned"},
		{"slash", "a/b"},
		{"dotdot", ".."},
		{"null-byte", "a\x00b"},
		{"empty", ""},
		{"trailing-garbage", "550e8400-e29b-41d4-a716-446655440000/../etc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &Message{ID: tc.id, Sender: "deadbeef"}
			_, err := c.forwardMessage("any-campfire-id", src)
			if err == nil {
				t.Fatalf("forwardMessage with ID %q returned nil error", tc.id)
			}
			if !errors.Is(err, message.ErrInvalidMessageID) {
				t.Errorf("expected ErrInvalidMessageID, got %T: %v", err, err)
			}
		})
	}
}
