package http_test

// Tests for handleSync record-to-message conversion behavior.
//
// Covered bead: workspace-gzs8
// SWEEP-TEST: handleSync — recordToMessage conversion errors silently drop messages.
//
// Two cases:
//  1. A MessageRecord with a malformed Sender hex in the store is silently omitted
//     from the sync response (documents the current "continue on error" behavior).
//  2. An all-fields-valid record appears in the sync response
//     (regression guard: if "continue" is changed to "return error" this fails).

import (
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// buildValidRecord creates and stores a well-formed MessageRecord, returning it.
func buildValidRecord(t *testing.T, s *store.Store, campfireID string) store.MessageRecord {
	t.Helper()
	id := tempIdentity(t)
	return storeMessageRecord(t, s, campfireID, id)
}

// buildMalformedSenderRecord inserts a MessageRecord whose Sender field is an
// odd-length hex string. hex.DecodeString will reject it, so recordToMessage
// returns an error and handleSync silently skips the record.
func buildMalformedSenderRecord(t *testing.T, s *store.Store, campfireID string) store.MessageRecord {
	t.Helper()
	id := tempIdentity(t)
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("malformed sender test"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message for malformed record: %v", err)
	}
	rec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      "notvalidhex!", // odd-length / non-hex: hex.DecodeString will fail
		Payload:     msg.Payload,
		Tags:        []string{"test"},
		Antecedents: nil,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
		Provenance:  nil,
		ReceivedAt:  time.Now().UnixNano(),
	}
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("storing malformed record: %v", err)
	}
	return rec
}

// TestHandleSyncMalformedSenderSilentlyDropped documents that a record whose
// Sender field cannot be decoded as hex is silently omitted from the sync
// response. The caller receives 200 OK with fewer messages than stored.
//
// This is the current behaviour. The test's purpose is to make the silent-drop
// visible: if someone changes the "continue" to a hard error in the future,
// this test will catch it — as will TestHandleSyncValidRecordIncluded.
func TestHandleSyncMalformedSenderSilentlyDropped(t *testing.T) {
	campfireID := "sync-malformed-sender"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+400)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Insert one valid record and one malformed record.
	buildValidRecord(t, s, campfireID)
	buildMalformedSenderRecord(t, s, campfireID)

	// Sync: store has 2 records but only 1 has a decodable Sender.
	msgs, err := cfhttp.Sync(ep, campfireID, 0, id)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// The malformed record is silently dropped; only the valid one is returned.
	if len(msgs) != 1 {
		t.Errorf("expected 1 message (malformed record silently dropped), got %d", len(msgs))
	}
}

// TestHandleSyncValidRecordIncluded is a regression guard that verifies a
// well-formed record always appears in the sync response.
// If the "continue" in handleSync were changed to a hard 500 return, this test
// would fail whenever any record is malformed — but more importantly, if the
// conversion logic were broken entirely, this test catches it.
func TestHandleSyncValidRecordIncluded(t *testing.T) {
	campfireID := "sync-valid-record"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+401)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Insert exactly one valid record.
	rec := buildValidRecord(t, s, campfireID)

	msgs, err := cfhttp.Sync(ep, campfireID, 0, id)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ID != rec.ID {
		t.Errorf("message ID mismatch: got %s, want %s", msgs[0].ID, rec.ID)
	}
	if !msgs[0].VerifySignature() {
		t.Error("synced message has invalid signature")
	}
}

