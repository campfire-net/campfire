package protocol_test

// scope_client_test.go — Integration tests for ScopeEnforcer wired into Client.
//
// TDD-driven integration tests for campfire-agent-fie.
// Tests use a real filesystem transport — no mock enforcer, no mock transport.

import (
	"errors"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// TestClient_ScopeEnforcer_BlocksCampfire verifies that a client initialised with
// a campfire allowlist denies Send to campfires not on the list and permits Send
// to campfires that are on the list.
func TestClient_ScopeEnforcer_BlocksCampfire(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)

	// Create two campfires.
	allowedID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)
	blockedID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	// Build a client restricted to allowedID only.
	client := protocol.New(s, agentID)
	client.SetScope(protocol.ScopeConfig{
		Campfires: []string{allowedID},
	})

	// Send to allowed campfire should succeed.
	_, err := client.Send(protocol.SendRequest{
		CampfireID: allowedID,
		Payload:    []byte("hello from allowed"),
	})
	if err != nil {
		t.Fatalf("Send to allowed campfire: unexpected error: %v", err)
	}

	// Send to blocked campfire should return ErrScopeDenied.
	_, err = client.Send(protocol.SendRequest{
		CampfireID: blockedID,
		Payload:    []byte("should be denied"),
	})
	if err == nil {
		t.Fatal("Send to blocked campfire: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Send to blocked campfire: expected errors.Is(err, ErrScopeDenied), got: %v", err)
	}
}

// TestClient_ScopeEnforcer_BlocksOperation verifies that a client restricted to
// "read" operation class denies Send (write) but permits Read.
func TestClient_ScopeEnforcer_BlocksOperation(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	// Restrict to read-only operations.
	client := protocol.New(s, agentID)
	client.SetScope(protocol.ScopeConfig{
		OperationClasses: []string{"read"},
	})

	// Read should succeed (read is in the allowed classes).
	_, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		SkipSync:   true,
	})
	if err != nil {
		t.Fatalf("Read with read-only scope: unexpected error: %v", err)
	}

	// Send should be denied (write is not in allowed classes).
	_, err = client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("should be denied"),
	})
	if err == nil {
		t.Fatal("Send with read-only scope: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Send with read-only scope: expected errors.Is(err, ErrScopeDenied), got: %v", err)
	}
}

// TestClient_ScopeEnforcer_UnrestrictedWhenEmpty verifies that a client with no
// scope config (empty Campfires and OperationClasses) allows all operations.
func TestClient_ScopeEnforcer_UnrestrictedWhenEmpty(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	// No scope config — unrestricted.
	client := protocol.New(s, agentID)

	// Send should succeed.
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("unrestricted send"),
	})
	if err != nil {
		t.Fatalf("Send with no scope: unexpected error: %v", err)
	}

	// Read should succeed.
	_, err = client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		SkipSync:   true,
	})
	if err != nil {
		t.Fatalf("Read with no scope: unexpected error: %v", err)
	}
}

// TestClient_ScopeEnforcer_AdminOperations verifies that a client restricted to
// "write" denies Leave and Evict (admin operations).
func TestClient_ScopeEnforcer_AdminOperations(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	// Restrict to write-only (no admin).
	client := protocol.New(s, agentID)
	client.SetScope(protocol.ScopeConfig{
		OperationClasses: []string{"write"},
	})

	// Leave should be denied before checking membership.
	err := client.Leave(campfireID)
	if err == nil {
		t.Fatal("Leave with write-only scope: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Leave with write-only scope: expected errors.Is(err, ErrScopeDenied), got: %v", err)
	}

	// Evict should be denied before self-eviction check or membership check.
	_, err = client.Evict(protocol.EvictRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: strings.Repeat("a", 64),
	})
	if err == nil {
		t.Fatal("Evict with write-only scope: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Evict with write-only scope: expected errors.Is(err, ErrScopeDenied), got: %v", err)
	}
}

// TestClient_ScopeEnforcer_AwaitBlockedByScope verifies that Await() is subject
// to scope enforcement — calling it with a campfire not in the allowlist returns
// ErrScopeDenied before any store or transport access.
func TestClient_ScopeEnforcer_AwaitBlockedByScope(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	allowedID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)
	blockedID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	client.SetScope(protocol.ScopeConfig{
		Campfires: []string{allowedID},
	})

	// Await on a blocked campfire should return ErrScopeDenied immediately.
	_, err := client.Await(t.Context(), protocol.AwaitRequest{
		CampfireID:  blockedID,
		TargetMsgID: "some-msg-id",
	})
	if err == nil {
		t.Fatal("Await on blocked campfire: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Await on blocked campfire: expected errors.Is(err, ErrScopeDenied), got: %v", err)
	}
}

// TestClient_ScopeEnforcer_AwaitBlockedByOperation verifies that Await() is
// denied when the operation class "read" is not in the scope allowlist.
func TestClient_ScopeEnforcer_AwaitBlockedByOperation(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	client.SetScope(protocol.ScopeConfig{
		OperationClasses: []string{"write"},
	})

	_, err := client.Await(t.Context(), protocol.AwaitRequest{
		CampfireID:  campfireID,
		TargetMsgID: "some-msg-id",
	})
	if err == nil {
		t.Fatal("Await with write-only scope: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Await with write-only scope: expected errors.Is(err, ErrScopeDenied), got: %v", err)
	}
}

// TestClient_ScopeEnforcer_DisbandBlockedByScope verifies that Disband() returns
// ErrScopeDenied when the operation class restricts admin operations.
func TestClient_ScopeEnforcer_DisbandBlockedByScope(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	// Restrict to read-only (no admin).
	client := protocol.New(s, agentID)
	client.SetScope(protocol.ScopeConfig{
		OperationClasses: []string{"read"},
	})

	err := client.Disband(campfireID)
	if err == nil {
		t.Fatal("Disband with read-only scope: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Disband with read-only scope: expected errors.Is(err, ErrScopeDenied), got: %v", err)
	}
}

// TestClient_ScopeEnforcer_CreateBlockedByScope verifies that Create() returns
// ErrScopeDenied when the operation class restricts admin operations.
func TestClient_ScopeEnforcer_CreateBlockedByScope(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)

	// Restrict to read-only (no admin).
	client := protocol.New(s, agentID)
	client.SetScope(protocol.ScopeConfig{
		OperationClasses: []string{"read"},
	})

	_, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: transportDir},
	})
	if err == nil {
		t.Fatal("Create with read-only scope: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Create with read-only scope: expected errors.Is(err, ErrScopeDenied), got: %v", err)
	}
}

// TestClient_ScopeEnforcer_MembersBlockedByScope verifies that Members() is
// subject to scope enforcement (campfire allowlist + read op class).
func TestClient_ScopeEnforcer_MembersBlockedByScope(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	allowedID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)
	blockedID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	client.SetScope(protocol.ScopeConfig{
		Campfires: []string{allowedID},
	})

	// Members for allowed campfire should work.
	_, err := client.Members(allowedID)
	if err != nil {
		t.Fatalf("Members on allowed campfire: unexpected error: %v", err)
	}

	// Members for blocked campfire should return ErrScopeDenied.
	_, err = client.Members(blockedID)
	if err == nil {
		t.Fatal("Members on blocked campfire: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Members on blocked campfire: expected ErrScopeDenied, got: %v", err)
	}
}
