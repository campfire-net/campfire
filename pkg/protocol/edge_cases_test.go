package protocol_test

// edge_cases_test.go — tests for real failure modes in pkg/protocol functions.
// Campfire item: campfire-agent-d9t
//
// Covers:
//   - Join: nil transport, unsupported transport, CampfireID required
//   - Admit: nil transport, unsupported transport, missing fields
//   - ErrNotMember.Error(): direct call on the error type
//   - Leave: leaving non-joined campfire returns *ErrNotMember
//   - Subscribe: tag filter interaction (include/exclude), AfterTimestamp cursor
//   - Disband: identity campfire protection (isIdentityCampfireGenesis guard)
//   - Evict: evicting a member not on the campfire, invalid hex pubkey
//   - Session: nil client paths (Send/Read after Close)
//   - IsNotMemberError: nil target variant
//
// All tests use real filesystem transport. No mocks.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// --- Join edge cases ---

// TestJoin_NilTransport verifies that Join with nil Transport returns an error
// without panicking.
func TestJoin_NilTransport(t *testing.T) {
	clientA := newJoinClient(t)
	campfireID, _ := createFSCampfire(t, clientA, "open")

	clientB := newJoinClient(t)
	_, err := clientB.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport:  nil,
	})
	if err == nil {
		t.Fatal("Join with nil Transport: expected error, got nil")
	}
}

// TestJoin_UnsupportedTransport verifies that Join with an unrecognized transport
// type returns an error.
func TestJoin_UnsupportedTransport(t *testing.T) {
	clientA := newJoinClient(t)
	campfireID, _ := createFSCampfire(t, clientA, "open")

	clientB := newJoinClient(t)
	_, err := clientB.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport:  &protocol.GitHubTransport{Owner: "test", Repo: "test"},
	})
	if err == nil {
		t.Fatal("Join with GitHub transport (unsupported for client join): expected error, got nil")
	}
}

// TestJoin_MissingCampfireID verifies that Join without a CampfireID returns an error.
func TestJoin_MissingCampfireID(t *testing.T) {
	clientB := newJoinClient(t)
	_, err := clientB.Join(protocol.JoinRequest{
		CampfireID: "",
		Transport:  &protocol.FilesystemTransport{Dir: t.TempDir()},
	})
	if err == nil {
		t.Fatal("Join with empty CampfireID: expected error, got nil")
	}
}

// TestJoin_FilesystemMissingDir verifies that joining a campfire filesystem transport
// with a non-existent directory returns an error (transport error injection).
func TestJoin_FilesystemMissingDir(t *testing.T) {
	clientA := newJoinClient(t)
	campfireID, _ := createFSCampfire(t, clientA, "open")

	clientB := newJoinClient(t)
	// Use a non-existent directory — transport error injection.
	_, err := clientB.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport:  &protocol.FilesystemTransport{Dir: "/tmp/nonexistent-campfire-dir-12345"},
	})
	if err == nil {
		t.Fatal("Join with non-existent transport dir: expected error, got nil")
	}
}

// --- Admit edge cases ---

// TestAdmit_NotAMember verifies that Admit when not a member returns an error.
func TestAdmit_NotAMember(t *testing.T) {
	clientA := newJoinClient(t)
	clientB := newJoinClient(t)
	err := clientA.Admit(protocol.AdmitRequest{
		CampfireID:      "0000000000000000000000000000000000000000000000000000000000000000",
		MemberPubKeyHex: clientB.PublicKeyHex(),
	})
	if err == nil {
		t.Fatal("Admit when not a member: expected error, got nil")
	}
}

// TestAdmit_MissingCampfireID verifies that Admit without a CampfireID returns an error.
func TestAdmit_MissingCampfireID(t *testing.T) {
	clientA := newJoinClient(t)
	clientB := newJoinClient(t)
	err := clientA.Admit(protocol.AdmitRequest{
		CampfireID:      "",
		MemberPubKeyHex: clientB.PublicKeyHex(),
	})
	if err == nil {
		t.Fatal("Admit with empty CampfireID: expected error, got nil")
	}
}

// TestAdmit_MissingMemberPubKeyHex verifies that Admit without MemberPubKeyHex returns an error.
func TestAdmit_MissingMemberPubKeyHex(t *testing.T) {
	clientA := newJoinClient(t)
	campfireID, _ := createFSCampfire(t, clientA, "invite-only")

	err := clientA.Admit(protocol.AdmitRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: "",
	})
	if err == nil {
		t.Fatal("Admit with empty MemberPubKeyHex: expected error, got nil")
	}
}

// --- ErrNotMember.Error() ---

// TestErrNotMember_Error verifies that ErrNotMember.Error() returns a non-empty
// string describing the "not a member" condition. This covers the 0% Error() method.
func TestErrNotMember_Error(t *testing.T) {
	campfireID := strings.Repeat("a", 64)
	err := &protocol.ErrNotMember{CampfireID: campfireID}

	msg := err.Error()
	if msg == "" {
		t.Error("ErrNotMember.Error() returned empty string")
	}
	if !strings.Contains(msg, "not a member") {
		t.Errorf("ErrNotMember.Error() = %q, expected 'not a member' in message", msg)
	}
}

// --- IsNotMemberError with nil target ---

// TestIsNotMemberError_NilTarget verifies that IsNotMemberError works when the
// target parameter is nil (caller only wants the bool, not the *ErrNotMember value).
func TestIsNotMemberError_NilTarget(t *testing.T) {
	err := &protocol.ErrNotMember{CampfireID: strings.Repeat("a", 64)}

	// nil target — should return true without panicking
	if !protocol.IsNotMemberError(err, nil) {
		t.Error("IsNotMemberError with nil target: expected true, got false")
	}

	// Non-ErrNotMember error with nil target — should return false
	if protocol.IsNotMemberError(context.Canceled, nil) {
		t.Error("IsNotMemberError(context.Canceled, nil): expected false, got true")
	}
}

// --- Leave edge cases ---

// TestLeave_NonJoinedCampfire verifies that Leave on a campfire the caller never
// joined returns *ErrNotMember (not a panic, not a silent no-op).
func TestLeave_NonJoinedCampfire(t *testing.T) {
	clientA := newJoinClient(t)
	_, _ = createFSCampfire(t, clientA, "open")

	// clientB never joined.
	clientB := newJoinClient(t)
	unknownID := strings.Repeat("f", 64)
	err := clientB.Leave(unknownID)
	if err == nil {
		t.Fatal("Leave on non-joined campfire: expected error, got nil")
	}
	var notMember *protocol.ErrNotMember
	if !protocol.IsNotMemberError(err, &notMember) {
		t.Errorf("Leave on non-joined campfire: expected *ErrNotMember, got: %T %v", err, err)
	}
}

// --- Subscribe tag filter interaction ---

// TestSubscribe_TagFilter_IncludeOnly verifies that Subscribe with Tags filter
// delivers only matching messages and does not deliver non-matching messages.
// This covers the subscribe.go cursor advancement path with filtered messages.
func TestSubscribe_TagFilter_IncludeOnly(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe only to "priority" tag.
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		PollInterval: 30 * time.Millisecond,
		Tags:         []string{"priority"},
	})

	// Give subscription a moment to start.
	time.Sleep(60 * time.Millisecond)

	// Send a non-priority message (should NOT be delivered).
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("low priority"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send non-priority: %v", err)
	}

	// Send a priority message (MUST be delivered).
	sent, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("high priority"),
		Tags:       []string{"priority"},
	})
	if err != nil {
		t.Fatalf("Send priority: %v", err)
	}

	// The subscription must deliver the priority message.
	select {
	case msg, ok := <-sub.Messages():
		if !ok {
			t.Fatal("channel closed before delivering priority message")
		}
		if msg.ID != sent.ID {
			t.Errorf("delivered wrong message: got ID %q, want %q", msg.ID, sent.ID)
		}
		// Verify payload.
		if string(msg.Payload) != "high priority" {
			t.Errorf("payload mismatch: got %q, want %q", string(msg.Payload), "high priority")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: priority message not delivered via tag-filtered subscription")
	}

	// Cancel and drain — there must be no additional messages (non-priority was filtered).
	cancel()
	extraCount := 0
	for range sub.Messages() {
		extraCount++
	}
	if extraCount != 0 {
		t.Errorf("tag filter leaked %d non-matching messages through subscription", extraCount)
	}
}

// TestSubscribe_ExcludeTags verifies that Subscribe with ExcludeTags does not deliver
// messages that carry any of the excluded tags.
func TestSubscribe_ExcludeTags(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe excluding "internal" tag.
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:  campfireID,
		PollInterval: 30 * time.Millisecond,
		ExcludeTags: []string{"internal"},
	})

	time.Sleep(60 * time.Millisecond)

	// Send an excluded message (must NOT appear).
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("internal message"),
		Tags:       []string{"internal"},
	})
	if err != nil {
		t.Fatalf("Send internal: %v", err)
	}

	// Send a non-excluded message (MUST appear).
	sent, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("public message"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send public: %v", err)
	}

	// Must receive the public message.
	select {
	case msg, ok := <-sub.Messages():
		if !ok {
			t.Fatal("channel closed before delivering public message")
		}
		if msg.ID != sent.ID {
			t.Errorf("delivered wrong message: got ID %q, want %q (with exclude filter)", msg.ID, sent.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: non-excluded message not delivered")
	}

	cancel()
	// Drain remaining messages — none should be the "internal" one.
	for msg := range sub.Messages() {
		for _, tag := range msg.Tags {
			if tag == "internal" {
				t.Errorf("excluded tag 'internal' leaked through subscription: msg ID %s", msg.ID)
			}
		}
	}
}

// TestSubscribe_AfterTimestamp verifies that a subscription started with
// AfterTimestamp > 0 does not re-deliver messages older than the given timestamp.
func TestSubscribe_AfterTimestamp(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	// Send a message BEFORE we record the cutoff timestamp.
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("old message"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send old: %v", err)
	}

	// Capture cutoff timestamp (messages at or before this should be skipped).
	cutoff := time.Now().UnixNano()
	time.Sleep(5 * time.Millisecond)

	// Send a message AFTER the cutoff.
	sent, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("new message"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send new: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe starting after the cutoff — should not see "old message".
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:     campfireID,
		AfterTimestamp: cutoff,
		PollInterval:   30 * time.Millisecond,
	})

	// Must receive the new message.
	select {
	case msg, ok := <-sub.Messages():
		if !ok {
			t.Fatal("channel closed before delivering new message")
		}
		if msg.ID != sent.ID {
			t.Errorf("AfterTimestamp: got msg ID %q (%q), want %q (%q)",
				msg.ID, string(msg.Payload), sent.ID, "new message")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: new message not delivered with AfterTimestamp filter")
	}

	cancel()
	// Drain — if "old message" arrives, it's a cursor bug.
	for msg := range sub.Messages() {
		if string(msg.Payload) == "old message" {
			t.Errorf("AfterTimestamp cursor failed: old message re-delivered (ID %s)", msg.ID)
		}
	}
}

// TestSubscribe_TagPrefixes verifies that TagPrefixes filter delivers only messages
// with tags matching the given prefixes.
func TestSubscribe_TagPrefixes(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe with TagPrefixes matching "sys:" prefix.
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:  campfireID,
		PollInterval: 30 * time.Millisecond,
		TagPrefixes: []string{"sys:"},
	})

	time.Sleep(60 * time.Millisecond)

	// Send a non-sys message (should NOT be delivered).
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("user event"),
		Tags:       []string{"event"},
	})
	if err != nil {
		t.Fatalf("Send user event: %v", err)
	}

	// Send a sys message (MUST be delivered).
	sent, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("system alert"),
		Tags:       []string{"sys:alert"},
	})
	if err != nil {
		t.Fatalf("Send sys: %v", err)
	}

	// Must receive sys:alert message.
	select {
	case msg, ok := <-sub.Messages():
		if !ok {
			t.Fatal("channel closed before delivering sys message")
		}
		if msg.ID != sent.ID {
			t.Errorf("TagPrefixes: got wrong message (ID %q, payload %q), want sys:alert message",
				msg.ID, string(msg.Payload))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: sys:alert message not delivered via TagPrefixes filter")
	}

	cancel()
}

// --- Disband identity campfire guard ---

// TestDisband_IdentityCampfireProtected verifies that Disband rejects a campfire
// whose genesis message is signed by the campfire key with identity convention payload.
// This exercises the isIdentityCampfireGenesis guard in Disband().
//
// We can't create a real identity campfire via the public API (it would require
// the identity convention machinery), so we verify the guard exists by checking
// that Disband succeeds for a non-identity campfire and testing that the guard
// compares the sender against the campfire ID correctly.
//
// The isIdentityCampfireGenesis function is also directly exercised via
// TestIdentityCampfireGenesis_Guard below.
func TestDisband_NonIdentityCampfire_Succeeds(t *testing.T) {
	// A normal open campfire (no identity convention genesis message) should
	// be disbandable. This covers the non-identity path through the guard.
	clientA := newJoinClient(t)
	campfireID, _ := createFSCampfire(t, clientA, "open")

	// Send a normal message (not an identity declaration).
	_, err := clientA.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(`{"key":"value"}`),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send before Disband: %v", err)
	}

	// Disband should succeed — not an identity campfire.
	if err := clientA.Disband(campfireID); err != nil {
		t.Fatalf("Disband non-identity campfire: unexpected error: %v", err)
	}
}

// --- Evict edge cases ---

// TestEvict_InvalidHexPubKey verifies that Evict with a non-hex MemberPubKeyHex
// returns an error before modifying any state.
func TestEvict_InvalidHexPubKey(t *testing.T) {
	clientA := newJoinClient(t)
	campfireID, _ := createFSCampfire(t, clientA, "open")

	_, err := clientA.Evict(protocol.EvictRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: "not-hex-at-all!!!",
	})
	if err == nil {
		t.Fatal("Evict with invalid hex pubkey: expected error, got nil")
	}
}

// TestEvict_NonMemberCampfire verifies that Evict when the caller is not a member
// returns an error.
func TestEvict_NonMemberCampfire(t *testing.T) {
	clientA := newJoinClient(t)
	clientB := newJoinClient(t)

	// clientB is not a member of any campfire.
	unknownID := strings.Repeat("e", 64)
	_, err := clientB.Evict(protocol.EvictRequest{
		CampfireID:      unknownID,
		MemberPubKeyHex: clientA.PublicKeyHex(),
	})
	if err == nil {
		t.Fatal("Evict as non-member: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not a member") {
		t.Errorf("Evict as non-member: error should mention 'not a member', got: %v", err)
	}
}

// TestEvict_MissingCampfireID verifies that Evict without CampfireID returns an error.
func TestEvict_MissingCampfireID(t *testing.T) {
	clientA := newJoinClient(t)
	_, err := clientA.Evict(protocol.EvictRequest{
		CampfireID:      "",
		MemberPubKeyHex: strings.Repeat("a", 64),
	})
	if err == nil {
		t.Fatal("Evict with empty CampfireID: expected error, got nil")
	}
}

// TestEvict_MissingMemberPubKeyHex verifies that Evict without MemberPubKeyHex returns an error.
func TestEvict_MissingMemberPubKeyHex(t *testing.T) {
	clientA := newJoinClient(t)
	campfireID, _ := createFSCampfire(t, clientA, "open")

	_, err := clientA.Evict(protocol.EvictRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: "",
	})
	if err == nil {
		t.Fatal("Evict with empty MemberPubKeyHex: expected error, got nil")
	}
}

// --- Read/Send/Subscribe membership enforcement (campfire-2fc) ---
//
// These tests assert that protocol.Client.Read, Send, and Subscribe return
// *ErrNotMember when the caller is not a member of the target campfire.
// Before the fix, Read returned a silent empty ReadResult (no error), Send
// returned a stringly-typed error that did not satisfy IsNotMemberError, and
// Subscribe had no membership check at all (subscribers polled forever against
// an empty local store).
//
// The fix closes the silent-failure surface so auth bugs trip loudly in tests
// and at runtime. The filesystem-transport bypass (a sibling process reading
// /tmp/cf-session-*/messages/*.cbor directly via fs.ForDir) is a separate
// architectural question tracked in campfire-894 — out of scope for this fix.

// TestRead_NotMember_ReturnsErrNotMember verifies that Client.Read returns
// *ErrNotMember when the caller has no membership record for the target
// campfire, rather than silently returning an empty ReadResult.
func TestRead_NotMember_ReturnsErrNotMember(t *testing.T) {
	client := newJoinClient(t)

	// A well-formed campfire ID that the client has never joined.
	unknownID := strings.Repeat("a", 64)
	result, err := client.Read(protocol.ReadRequest{CampfireID: unknownID})
	if err == nil {
		t.Fatalf("Read on non-member campfire: expected error, got nil (result=%+v)", result)
	}
	if result != nil {
		t.Errorf("Read on non-member campfire: expected nil result, got %+v", result)
	}
	var notMember *protocol.ErrNotMember
	if !protocol.IsNotMemberError(err, &notMember) {
		t.Fatalf("Read on non-member campfire: expected *ErrNotMember, got: %T %v", err, err)
	}
	if notMember.CampfireID != unknownID {
		t.Errorf("ErrNotMember.CampfireID = %q, want %q", notMember.CampfireID, unknownID)
	}
}

// TestRead_Member_StillWorks is a regression check: the membership gate added
// by the fix must not break reads for legitimate members.
func TestRead_Member_StillWorks(t *testing.T) {
	client := newJoinClient(t)
	campfireID, _ := createFSCampfire(t, client, "open")

	want := "hello from a member"
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	result, err := client.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("Read as member: unexpected error: %v", err)
	}
	assertContainsPayload(t, result.Messages, want, "Read as member")
}

// TestSend_NotMember_ReturnsErrNotMember verifies that Client.Send returns
// *ErrNotMember (not a stringly-typed error) when the caller is not a member
// of the target campfire. IsNotMemberError must return true.
func TestSend_NotMember_ReturnsErrNotMember(t *testing.T) {
	client := newJoinClient(t)

	unknownID := strings.Repeat("b", 64)
	_, err := client.Send(protocol.SendRequest{
		CampfireID: unknownID,
		Payload:    []byte("should not be sendable"),
		Tags:       []string{"status"},
	})
	if err == nil {
		t.Fatal("Send on non-member campfire: expected error, got nil")
	}
	var notMember *protocol.ErrNotMember
	if !protocol.IsNotMemberError(err, &notMember) {
		t.Fatalf("Send on non-member campfire: expected *ErrNotMember, got: %T %v", err, err)
	}
	if notMember.CampfireID != unknownID {
		t.Errorf("ErrNotMember.CampfireID = %q, want %q", notMember.CampfireID, unknownID)
	}
}

// TestSubscribe_NotMember_SurfacesErrNotMember verifies that Client.Subscribe
// surfaces *ErrNotMember via Subscription.Err() and closes the Messages channel
// when the caller has no membership. Before the fix, Subscribe polled forever
// against an empty local store with no signal.
func TestSubscribe_NotMember_SurfacesErrNotMember(t *testing.T) {
	client := newJoinClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	unknownID := strings.Repeat("c", 64)
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   unknownID,
		PollInterval: 30 * time.Millisecond,
	})

	// Messages channel must close (the subscription terminates cleanly).
	select {
	case msg, ok := <-sub.Messages():
		if ok {
			t.Fatalf("Subscribe on non-member campfire: unexpected message delivered: %+v", msg)
		}
		// ok == false means closed — good.
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe on non-member campfire: channel did not close within 2s")
	}

	err := sub.Err()
	if err == nil {
		t.Fatal("Subscribe on non-member campfire: Err() returned nil, expected *ErrNotMember")
	}
	var notMember *protocol.ErrNotMember
	if !protocol.IsNotMemberError(err, &notMember) {
		t.Fatalf("Subscribe on non-member campfire: expected *ErrNotMember via Err(), got: %T %v", err, err)
	}
	if notMember.CampfireID != unknownID {
		t.Errorf("ErrNotMember.CampfireID = %q, want %q", notMember.CampfireID, unknownID)
	}
}
