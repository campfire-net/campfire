package http

// Tests for validateMessageToSign — internal package tests so we can call the
// unexported function directly.
//
// Done-condition coverage (campfire-agent-nd7):
//   1. validateMessageToSign(hopSignInput, nil) returns nil — nil store skips
//      the HasMessage cross-check.
//   2. validateMessageToSign(hopSignInput, realStore) where message exists
//      returns nil.
//   3. validateMessageToSign(hopSignInput, realStore) where message does NOT
//      exist returns error.

import (
	"fmt"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
)

// stubMessageStore is a minimal store.MessageStore for unit-testing
// validateMessageToSign without an on-disk SQLite database.
type stubMessageStore struct {
	known map[string]bool
}

func (s *stubMessageStore) HasMessage(id string) (bool, error) {
	return s.known[id], nil
}

// hopSignBytes returns a CBOR-encoded HopSignInput with the given messageID.
func hopSignBytes(t *testing.T, messageID string) []byte {
	t.Helper()
	campfirePub := make([]byte, 32)
	for i := range campfirePub {
		campfirePub[i] = byte(i + 1)
	}
	hop := message.HopSignInput{
		MessageID:             messageID,
		CampfireID:            campfirePub,
		MembershipHash:        campfirePub,
		MemberCount:           2,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		Timestamp:             time.Now().UnixNano(),
	}
	b, err := cfencoding.Marshal(hop)
	if err != nil {
		t.Fatalf("marshaling HopSignInput: %v", err)
	}
	return b
}

// TestValidateMessageToSign_NilStore verifies done-condition 1:
// validateMessageToSign with a nil store skips the HasMessage cross-check and
// returns nil for a structurally valid HopSignInput.
func TestValidateMessageToSign_NilStore(t *testing.T) {
	msg := hopSignBytes(t, "some-message-id")
	if err := validateMessageToSign(msg, nil); err != nil {
		t.Errorf("expected nil error with nil store, got: %v", err)
	}
}

// TestValidateMessageToSign_RealStoreMessageExists verifies done-condition 2:
// validateMessageToSign with a real store returns nil when the MessageID is
// present in the store.
func TestValidateMessageToSign_RealStoreMessageExists(t *testing.T) {
	const msgID = "existing-message-id"
	ms := &stubMessageStore{known: map[string]bool{msgID: true}}
	msg := hopSignBytes(t, msgID)
	if err := validateMessageToSign(msg, ms); err != nil {
		t.Errorf("expected nil error for existing message, got: %v", err)
	}
}

// TestValidateMessageToSign_RealStoreMessageMissing verifies done-condition 3:
// validateMessageToSign with a real store returns an error when the MessageID
// is NOT present in the store.
func TestValidateMessageToSign_RealStoreMessageMissing(t *testing.T) {
	ms := &stubMessageStore{known: map[string]bool{}}
	msg := hopSignBytes(t, "fabricated-message-id-not-in-store")
	err := validateMessageToSign(msg, ms)
	if err == nil {
		t.Error("expected error for missing message, got nil")
	}
}

// TestValidateMessageToSign_EmptyPayload verifies that an empty payload is
// rejected regardless of store.
func TestValidateMessageToSign_EmptyPayload(t *testing.T) {
	if err := validateMessageToSign(nil, nil); err == nil {
		t.Error("expected error for nil payload, got nil")
	}
	if err := validateMessageToSign([]byte{}, nil); err == nil {
		t.Error("expected error for empty payload, got nil")
	}
}

// TestValidateMessageToSign_ArbitraryBytes verifies that random bytes are
// rejected as neither HopSignInput nor MessageSignInput.
func TestValidateMessageToSign_ArbitraryBytes(t *testing.T) {
	if err := validateMessageToSign([]byte("not a cbor payload"), nil); err == nil {
		t.Error("expected error for arbitrary bytes, got nil")
	}
}

// TestValidateMessageToSign_StoreError verifies that a store lookup error is
// propagated as an error (not silently swallowed).
func TestValidateMessageToSign_StoreError(t *testing.T) {
	errStore := &errorMessageStore{err: fmt.Errorf("simulated store failure")}
	msg := hopSignBytes(t, "some-id")
	err := validateMessageToSign(msg, errStore)
	if err == nil {
		t.Error("expected error from store lookup failure, got nil")
	}
}

// errorMessageStore always returns an error from HasMessage.
type errorMessageStore struct {
	err error
}

func (e *errorMessageStore) HasMessage(_ string) (bool, error) {
	return false, e.err
}
