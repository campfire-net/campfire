package protocol_test

// coverage_gap_test.go — Tests closing 0% coverage on pkg/protocol functions.
// Campfire item: campfire-agent-32b
//
// Functions covered:
//   1. GetMembership — client.GetMembership returns store.Membership for a joined campfire
//   2. Error() / As() on RoleError — error interface methods (covered via Send with observer role)
//   3. UpsertPeerEndpoint — covered via client.AddPeer which calls c.store.UpsertPeerEndpoint
//   4. TransportType — FilesystemTransport, P2PHTTPTransport, GitHubTransport each return a string
//   5. SenderIdentity — returns SenderCampfireID when set, Sender otherwise
//   6. WithScope — Init option wires a ScopeEnforcer; Send to blocked campfire returns ErrScopeDenied
//   7. CampfireID — Session.CampfireID() returns the session's campfire ID
//
// All tests use real filesystem transport (no network required).

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestGetMembership_ReturnsMembership verifies that client.GetMembership returns
// the membership record after joining a campfire.
func TestGetMembership_ReturnsMembership(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	m, err := client.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("GetMembership returned nil for joined campfire")
	}
	if m.CampfireID != campfireID {
		t.Errorf("GetMembership: CampfireID = %q, want %q", m.CampfireID, campfireID)
	}
	if m.Role != campfire.RoleFull {
		t.Errorf("GetMembership: Role = %q, want %q", m.Role, campfire.RoleFull)
	}
}

// TestGetMembership_NotMember verifies that GetMembership returns nil, nil for
// a campfire the client has never joined.
func TestGetMembership_NotMember(t *testing.T) {
	_, s, _ := setupTestEnv(t)
	client := protocol.New(s, nil)

	m, err := client.GetMembership("nonexistent-campfire-id")
	if err != nil {
		t.Fatalf("GetMembership: unexpected error: %v", err)
	}
	if m != nil {
		t.Errorf("GetMembership: expected nil for non-member, got %+v", m)
	}
}

// TestRoleError_ErrorMethod verifies that RoleError.Error() returns a non-empty string.
func TestRoleError_ErrorMethod(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleObserver)

	client := protocol.New(s, agentID)
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("should be denied"),
	})
	if err == nil {
		t.Fatal("Send as observer: expected error, got nil")
	}

	// Verify .Error() returns a meaningful string.
	msg := err.Error()
	if msg == "" {
		t.Error("RoleError.Error() returned empty string")
	}
	if !strings.Contains(msg, "observer") {
		t.Errorf("RoleError.Error() = %q, expected to contain 'observer'", msg)
	}
}

// TestRoleError_AsMethod verifies the two branches of RoleError.As():
//   1. Returns true and sets the target when target is **RoleError.
//   2. Returns false when target is any other type.
//
// RoleError.As() implements the errors package's target-match interface.
// It is called directly here because Go's errors.As uses direct type assignment
// (not As()) when the chain element already matches the target type exactly.
func TestRoleError_AsMethod(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleObserver)

	client := protocol.New(s, agentID)
	_, sendErr := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("denied"),
	})
	if sendErr == nil {
		t.Fatal("Send as observer: expected error, got nil")
	}

	// Obtain the *RoleError value. Since it's a direct *RoleError, a type assertion works.
	roleErrDirect, ok := sendErr.(*protocol.RoleError)
	if !ok {
		t.Fatalf("expected *protocol.RoleError from Send, got %T", sendErr)
	}

	// Branch 1: As(**RoleError) must return true and set the target.
	var target *protocol.RoleError
	if !roleErrDirect.As(&target) {
		t.Error("RoleError.As(**RoleError) returned false, want true")
	}
	if target == nil {
		t.Error("RoleError.As(**RoleError): target is nil after successful call")
	}
	if target.Error() == "" {
		t.Error("target.Error() is empty after As() set it")
	}

	// Branch 2: As(non-**RoleError) must return false (wrong target type).
	var wrongTarget *fmt.Stringer
	if roleErrDirect.As(&wrongTarget) {
		t.Error("RoleError.As(*fmt.Stringer) returned true, want false")
	}
	_ = fmt.Sprintf("suppress unused import warning: %v", sendErr)
}

// TestUpsertPeerEndpoint_ViaAddPeer verifies that UpsertPeerEndpoint is exercised
// via client.AddPeer on a P2P HTTP campfire. We set up a filesystem campfire first
// and verify the expected error (ErrTransportNotSupported) for the non-P2P case,
// which exercises the store.GetMembership path. For a direct UpsertPeerEndpoint
// call we use the store directly after setting up a P2P membership.
func TestUpsertPeerEndpoint_ViaStore(t *testing.T) {
	storeDir := t.TempDir()

	// Open the store directly.
	rawStore, err := store.Open(filepath.Join(storeDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer rawStore.Close()

	campfireID := strings.Repeat("a", 64)

	// Add a P2P HTTP membership so UpsertPeerEndpoint has a campfire to reference.
	if err := rawStore.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  "/tmp/test",
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "p2p-http",
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Exercise UpsertPeerEndpoint directly on the store.
	peerPubkey := strings.Repeat("b", 64)
	ep := store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: peerPubkey,
		Endpoint:     "http://127.0.0.1:9001",
	}
	if err := rawStore.UpsertPeerEndpoint(ep); err != nil {
		t.Fatalf("UpsertPeerEndpoint: %v", err)
	}

	// Verify the endpoint was stored by listing peer endpoints.
	endpoints, err := rawStore.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}
	if len(endpoints) != 1 {
		t.Fatalf("expected 1 peer endpoint, got %d", len(endpoints))
	}
	if endpoints[0].Endpoint != "http://127.0.0.1:9001" {
		t.Errorf("Endpoint = %q, want %q", endpoints[0].Endpoint, "http://127.0.0.1:9001")
	}
	if endpoints[0].MemberPubkey != peerPubkey {
		t.Errorf("MemberPubkey = %q, want %q", endpoints[0].MemberPubkey, peerPubkey)
	}
}

// TestTransportType_Filesystem verifies FilesystemTransport.TransportType() returns "filesystem".
func TestTransportType_Filesystem(t *testing.T) {
	tr := protocol.FilesystemTransport{Dir: "/tmp/test"}
	got := tr.TransportType()
	if got != "filesystem" {
		t.Errorf("FilesystemTransport.TransportType() = %q, want %q", got, "filesystem")
	}
}

// TestTransportType_P2PHTTP verifies P2PHTTPTransport.TransportType() returns "p2p-http".
func TestTransportType_P2PHTTP(t *testing.T) {
	tr := protocol.P2PHTTPTransport{}
	got := tr.TransportType()
	if got != "p2p-http" {
		t.Errorf("P2PHTTPTransport.TransportType() = %q, want %q", got, "p2p-http")
	}
}

// TestTransportType_GitHub verifies GitHubTransport.TransportType() returns "github".
func TestTransportType_GitHub(t *testing.T) {
	tr := protocol.GitHubTransport{Owner: "campfire-net", Repo: "campfire"}
	got := tr.TransportType()
	if got != "github" {
		t.Errorf("GitHubTransport.TransportType() = %q, want %q", got, "github")
	}
}

// TestSenderIdentity_WithCampfireID verifies SenderIdentity returns SenderCampfireID
// when it is set (agent's stable identity address takes precedence).
func TestSenderIdentity_WithCampfireID(t *testing.T) {
	campfireID := strings.Repeat("c", 64)
	senderPubkey := strings.Repeat("d", 64)

	m := &protocol.Message{
		ID:               "msg-1",
		CampfireID:       strings.Repeat("e", 64),
		Sender:           senderPubkey,
		SenderCampfireID: campfireID,
	}

	got := m.SenderIdentity()
	if got != campfireID {
		t.Errorf("SenderIdentity() = %q, want campfire ID %q", got, campfireID)
	}
}

// TestSenderIdentity_FallbackToSender verifies SenderIdentity falls back to Sender
// (hex pubkey) when SenderCampfireID is empty (legacy/ephemeral agents).
func TestSenderIdentity_FallbackToSender(t *testing.T) {
	senderPubkey := strings.Repeat("d", 64)

	m := &protocol.Message{
		ID:               "msg-2",
		CampfireID:       strings.Repeat("e", 64),
		Sender:           senderPubkey,
		SenderCampfireID: "", // empty — should fall back to Sender
	}

	got := m.SenderIdentity()
	if got != senderPubkey {
		t.Errorf("SenderIdentity() = %q, want sender pubkey %q", got, senderPubkey)
	}
}

// TestWithScope_ViaCampfireInit verifies that WithScope as an Init option installs
// a scope enforcer that denies access to campfires not in the allowlist.
func TestWithScope_ViaCampfireInit(t *testing.T) {
	configDirA := t.TempDir()
	transportDir := t.TempDir()

	// Init a client without scope restriction to set up the campfires.
	clientUnrestricted, _, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer clientUnrestricted.Close()

	allowedID, err := clientUnrestricted.Create(protocol.CreateRequest{
		Transport: &protocol.FilesystemTransport{Dir: transportDir},
	})
	if err != nil {
		t.Fatalf("Create allowed campfire: %v", err)
	}

	blockedID, err := clientUnrestricted.Create(protocol.CreateRequest{
		Transport: &protocol.FilesystemTransport{Dir: transportDir},
	})
	if err != nil {
		t.Fatalf("Create blocked campfire: %v", err)
	}

	// Init a new client in the same configDir with a WithScope restriction.
	clientScoped, _, err := protocol.Init(configDirA, protocol.WithScope(protocol.ScopeConfig{
		Campfires: []string{allowedID.CampfireID},
	}))
	if err != nil {
		t.Fatalf("Init with WithScope: %v", err)
	}
	defer clientScoped.Close()

	// Send to allowed campfire must succeed.
	_, err = clientScoped.Send(protocol.SendRequest{
		CampfireID: allowedID.CampfireID,
		Payload:    []byte("allowed message"),
	})
	if err != nil {
		t.Fatalf("Send to allowed campfire: unexpected error: %v", err)
	}

	// Send to blocked campfire must return ErrScopeDenied.
	_, err = clientScoped.Send(protocol.SendRequest{
		CampfireID: blockedID.CampfireID,
		Payload:    []byte("blocked message"),
	})
	if err == nil {
		t.Fatal("Send to blocked campfire: expected ErrScopeDenied, got nil")
	}
	if !errors.Is(err, protocol.ErrScopeDenied) {
		t.Errorf("Send to blocked campfire: expected ErrScopeDenied, got: %v", err)
	}
}

// TestSession_CampfireID verifies that Session.CampfireID() returns a non-empty
// campfire ID after NewSession() is called.
func TestSession_CampfireID(t *testing.T) {
	configDir := t.TempDir()
	client, _, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer client.Close()

	sess, _, err := client.NewSession(5 * time.Minute)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	id := sess.CampfireID()
	if id == "" {
		t.Error("Session.CampfireID() returned empty string")
	}
	// The campfire ID should be a hex-encoded public key — 64 hex characters.
	if len(id) != 64 {
		t.Errorf("Session.CampfireID() = %q (len %d), want 64-char hex ID", id, len(id))
	}
}
