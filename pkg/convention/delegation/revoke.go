// Package delegation implements grant validation and trust resolution for the
// identity-delegation convention. This file adds PostRevoke, the symmetric
// write-path helper for PostGrant (identity-delegation-revocation.md §4).
package delegation

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// revokeBody is the JSON payload sent by PostRevoke.
type revokeBody struct {
	ChildPubkey string `json:"child_pubkey"`
}

// PostRevoke constructs and posts an identity:revoked message from the client's
// identity (the revoking parent) for childPubkey, in the given campfire.
// Returns the posted *message.Message on success.
//
// Validation:
//   - client must not be nil.
//   - len(childPubkey) must equal ed25519.PublicKeySize (32 bytes).
//   - len(campfireID) must be > 0.
//
// PostRevoke does not verify that a matching grant exists (per spec — see
// identity-delegation-revocation.md §4), does not auto-retry on send failure,
// and does not deduplicate revocations.
//
// The client's identity IS the revoking parent; callers that want to revoke
// from a different parent construct a different client.
//
// ctx is accepted for future cancellation support; the current
// protocol.Client.Send signature does not take one.
func PostRevoke(
	ctx context.Context,
	client *protocol.Client,
	campfireID []byte,
	childPubkey ed25519.PublicKey,
) (*message.Message, error) {
	if client == nil {
		return nil, errors.New("PostRevoke: client is nil")
	}
	if len(childPubkey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("PostRevoke: child pubkey must be %d bytes, got %d", ed25519.PublicKeySize, len(childPubkey))
	}
	if len(campfireID) == 0 {
		return nil, errors.New("PostRevoke: campfire ID is empty")
	}

	_ = ctx // reserved for future cancellation

	campfireHex := hex.EncodeToString(campfireID)
	payload, err := json.Marshal(revokeBody{
		ChildPubkey: hex.EncodeToString(childPubkey),
	})
	if err != nil {
		return nil, fmt.Errorf("PostRevoke: marshal payload: %w", err)
	}

	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireHex,
		Payload:    payload,
		Tags:       []string{RevokedTag},
	})
	if err != nil {
		return nil, fmt.Errorf("PostRevoke: send: %w", err)
	}
	return msg, nil
}
