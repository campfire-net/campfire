// Package protocol_test verifies the cf-protocol public surface contract
// (campfireagent-753: Stage 1 scaffold).
//
// These tests prove:
//   1. The public surface types are reachable and compile correctly.
//   2. Type aliases match the underlying pkg/protocol types (package-boundary test).
//   3. Reserved tags and ops are accessible via the public surface.
//
// TDD note: these tests are written BEFORE the implementation and fail on HEAD
// until cf-protocol/protocol/ is created with the correct re-exports.
package protocol_test

import (
	"testing"

	cfprotocol "github.com/campfire-net/campfire/cf-protocol/protocol"
)

// TestPublicSurfaceCompiles verifies that the cf-protocol public surface
// types exist, are importable, and behave as documented.
// This is the package-boundary test for the L1-narrow depguard rule.
func TestPublicSurfaceCompiles(t *testing.T) {
	// Verify Message type is accessible and has expected fields.
	var msg cfprotocol.Message
	msg.ID = "test-id"
	msg.Payload = []byte("hello")
	msg.Tags = []string{"status"}
	msg.Antecedents = []string{}

	if msg.ID != "test-id" {
		t.Errorf("Message.ID = %q, want %q", msg.ID, "test-id")
	}
	if string(msg.Payload) != "hello" {
		t.Errorf("Message.Payload = %q, want %q", msg.Payload, "hello")
	}
}

// TestMemberRecordAccessible verifies MemberRecord is exported from cf-protocol.
func TestMemberRecordAccessible(t *testing.T) {
	var mr cfprotocol.MemberRecord
	mr.MemberPubkey = "abc123"
	mr.Role = "member"

	if mr.MemberPubkey != "abc123" {
		t.Errorf("MemberRecord.MemberPubkey = %q, want %q", mr.MemberPubkey, "abc123")
	}
}

// TestReservedTagsAccessible verifies the reserved tag constants are exported.
func TestReservedTagsAccessible(t *testing.T) {
	if cfprotocol.TagFuture == "" {
		t.Error("TagFuture must be non-empty")
	}
	if cfprotocol.TagFulfills == "" {
		t.Error("TagFulfills must be non-empty")
	}
}

// TestReservedOpsAccessible verifies the reserved-op floor symbols are exported
// from the cf-protocol public surface (campfireagent-935).
func TestReservedOpsAccessible(t *testing.T) {
	const wantCount = 10
	if got := len(cfprotocol.ReservedOps); got != wantCount {
		t.Errorf("ReservedOps: want %d ops, got %d", wantCount, got)
	}

	// IsReservedOp must return true for known reserved ops.
	knownReserved := []string{"disband", "evict", "admit", "grant", "revoke",
		"delegation-grant", "delegation-revoke", "delegation-accept",
		"member-roster", "compaction"}
	for _, op := range knownReserved {
		if !cfprotocol.IsReservedOp(op) {
			t.Errorf("IsReservedOp(%q) = false, want true", op)
		}
	}

	// IsReservedOp must return false for non-reserved ops.
	nonReserved := []string{"claim", "publish", "subscribe", "my-op", ""}
	for _, op := range nonReserved {
		if cfprotocol.IsReservedOp(op) {
			t.Errorf("IsReservedOp(%q) = true, want false", op)
		}
	}
}

// TestTransportInterfaceExists verifies the Transport interface is exported.
func TestTransportInterfaceExists(t *testing.T) {
	// Verify FilesystemTransport implements Transport.
	var tr cfprotocol.Transport = cfprotocol.FilesystemTransport{Dir: "/tmp"}
	if tr.TransportType() != "filesystem" {
		t.Errorf("FilesystemTransport.TransportType() = %q, want %q",
			tr.TransportType(), "filesystem")
	}
}

// TestSendRequestFields verifies SendRequest has the documented fields.
func TestSendRequestFields(t *testing.T) {
	req := cfprotocol.SendRequest{
		CampfireID: "abc",
		Payload:    []byte("payload"),
		Tags:       []string{"status"},
	}
	if req.CampfireID != "abc" {
		t.Errorf("SendRequest.CampfireID = %q, want %q", req.CampfireID, "abc")
	}
}

// TestReadRequestFields verifies ReadRequest has the documented fields.
func TestReadRequestFields(t *testing.T) {
	req := cfprotocol.ReadRequest{
		CampfireID:       "abc",
		IncludeCompacted: true,
	}
	if req.CampfireID != "abc" {
		t.Errorf("ReadRequest.CampfireID = %q, want %q", req.CampfireID, "abc")
	}
}

// TestAwaitRequestFields verifies AwaitRequest has the documented fields.
func TestAwaitRequestFields(t *testing.T) {
	req := cfprotocol.AwaitRequest{
		CampfireID:    "abc",
		TargetMsgID:   "msg-123",
	}
	if req.TargetMsgID != "msg-123" {
		t.Errorf("AwaitRequest.TargetMsgID = %q, want %q", req.TargetMsgID, "msg-123")
	}
}
