package http_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestPollReceivesMessages: pre-store 2 messages, call Poll(cursor=0),
// assert msgs returned and newCursor = ReceivedAt of newest.
func TestPollReceivesMessages(t *testing.T) {
	campfireID := "poll-client-recv"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+120)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Pre-store 2 messages.
	rec1 := storeMessageRecord(t, s, campfireID, id)
	time.Sleep(time.Millisecond)
	rec2 := storeMessageRecord(t, s, campfireID, id)
	_ = rec1

	msgs, newCursor, err := cfhttp.Poll(ep, campfireID, 0, 2, id)
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}
	if newCursor != rec2.ReceivedAt {
		t.Errorf("newCursor = %d, want %d", newCursor, rec2.ReceivedAt)
	}
}

// TestPollTimeout: no messages, timeout=1, expect (nil, cursor, nil) within 2s.
func TestPollClientTimeout(t *testing.T) {
	campfireID := "poll-client-timeout"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+121)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	cursor := time.Now().UnixNano()
	start := time.Now()
	msgs, newCursor, err := cfhttp.Poll(ep, campfireID, cursor, 1, id)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Poll timeout returned unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil msgs on timeout, got %d messages", len(msgs))
	}
	if newCursor != cursor {
		t.Errorf("cursor advanced on timeout: newCursor = %d, want %d", newCursor, cursor)
	}
	if elapsed > 2*time.Second {
		t.Errorf("poll took too long: %v (expected <= 2s)", elapsed)
	}
}

// TestPollAuthFailure: server returns 401, assert Poll returns non-nil error.
func TestPollAuthFailure(t *testing.T) {
	campfireID := "poll-client-auth"
	idServer := tempIdentity(t)
	idStranger := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	// Only idServer is a member; idStranger is not registered.
	addPeerEndpoint(t, s, campfireID, idServer.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+122)
	startTransportWithSelf(t, addr, s, idServer)
	ep := fmt.Sprintf("http://%s", addr)

	// idStranger not in peer list → 403 (member check fails).
	_, _, err := cfhttp.Poll(ep, campfireID, 0, 1, idStranger)
	if err == nil {
		t.Fatal("expected error for non-member poll, got nil")
	}
}

// TestPollCursorAdvances: call Poll twice, second call passes cursor from first,
// assert no duplicates (second poll times out with no new messages).
func TestPollCursorAdvances(t *testing.T) {
	campfireID := "poll-client-cursor"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+123)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Store 1 message.
	storeMessageRecord(t, s, campfireID, id)

	// First poll: should return 1 message and advance cursor.
	msgs1, cursor1, err := cfhttp.Poll(ep, campfireID, 0, 2, id)
	if err != nil {
		t.Fatalf("first Poll error: %v", err)
	}
	if len(msgs1) != 1 {
		t.Fatalf("expected 1 message in first poll, got %d", len(msgs1))
	}

	// Second poll with advanced cursor: no new messages → timeout → nil.
	msgs2, cursor2, err := cfhttp.Poll(ep, campfireID, cursor1, 1, id)
	if err != nil {
		t.Fatalf("second Poll error: %v", err)
	}
	if msgs2 != nil {
		t.Errorf("expected no new messages in second poll, got %d", len(msgs2))
	}
	// Cursor stays the same after timeout (same as what we sent).
	if cursor2 != cursor1 {
		t.Errorf("cursor should not advance on timeout: got %d, want %d", cursor2, cursor1)
	}
}

// TestPollReturnsCorrectMessageContent verifies the returned messages are
// CBOR-decoded correctly and match what was stored.
func TestPollReturnsCorrectMessageContent(t *testing.T) {
	campfireID := "poll-client-content"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+124)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Deliver a message via the HTTP transport (which stores it).
	msg := newTestMessage(t, id)
	if err := cfhttp.Deliver(ep, campfireID, msg, id); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	msgs, _, err := cfhttp.Poll(ep, campfireID, 0, 2, id)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	got := msgs[0]
	if got.ID != msg.ID {
		t.Errorf("message ID mismatch: got %s, want %s", got.ID, msg.ID)
	}
	// Verify payload is preserved.
	if string(got.Payload) != string(msg.Payload) {
		t.Errorf("payload mismatch: got %q, want %q", got.Payload, msg.Payload)
	}
	// suppress unused
	var _ message.Message = got
}
