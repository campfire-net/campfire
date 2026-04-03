package protocol_test

// Tests for protocol.Client.Send() — campfire-agent-hcd.
//
// Integration tests use a real filesystem transport (temp dirs). No mocks.
// External transports (GitHub, P2P HTTP) require real infra and are not tested here.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupTestEnv creates a minimal test environment:
//   - A fresh temp dir for the store
//   - A fresh temp dir for the transport
//   - A generated agent identity
//   - An open store
//
// The caller is responsible for setting up campfires via setupFilesystemCampfire.
func setupTestEnv(t *testing.T) (agentID *identity.Identity, s store.Store, transportDir string) {
	t.Helper()
	storeDir := t.TempDir()
	transportDir = t.TempDir()

	var err error
	agentID, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err = store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	return agentID, s, transportDir
}

// setupFilesystemCampfire creates a campfire in the given transport base dir,
// writes the agent as a member, and records the membership in the store.
// Returns the campfire ID (hex public key).
func setupFilesystemCampfire(t *testing.T, agentID *identity.Identity, s store.Store, transportBaseDir string, role string) string {
	t.Helper()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	campfireID := cfID.PublicKeyHex()
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s directory: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	tr := fs.New(transportBaseDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      role,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          role,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireID
}

// TestSendFilesystem verifies that a full-member can send a message via the
// filesystem transport and the message is stored on disk.
func TestSendFilesystem(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("hello world"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg == nil {
		t.Fatal("Send returned nil message")
	}
	if msg.ID == "" {
		t.Fatal("message ID is empty")
	}

	// Verify the message is on disk.
	messagesDir := filepath.Join(transportDir, campfireID, "messages")
	entries, err := os.ReadDir(messagesDir)
	if err != nil {
		t.Fatalf("reading messages dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no message files found in transport directory")
	}

	// Verify the message has a valid sender and signature.
	if !msg.VerifySignature() {
		t.Error("message signature is invalid")
	}
	if fmt.Sprintf("%x", msg.Sender) != agentID.PublicKeyHex() {
		t.Errorf("sender mismatch: got %x, want %s", msg.Sender, agentID.PublicKeyHex())
	}
}

// TestSendFilesystem_PayloadRoundtrip verifies the payload is stored correctly
// and the message can be read back from the filesystem transport.
func TestSendFilesystem_PayloadRoundtrip(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	want := "test payload for roundtrip"
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"finding"},
		Antecedents: []string{},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if string(msg.Payload) != want {
		t.Errorf("payload mismatch: got %q, want %q", msg.Payload, want)
	}
	if len(msg.Tags) != 1 || msg.Tags[0] != "finding" {
		t.Errorf("tags mismatch: got %v, want [finding]", msg.Tags)
	}
}

// TestSendFilesystem_ProvenanceHop verifies a provenance hop is added and
// signed by the campfire key.
func TestSendFilesystem_ProvenanceHop(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("hop test"),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(msg.Provenance) == 0 {
		t.Fatal("expected at least one provenance hop")
	}
	hop := msg.Provenance[0]
	if !message.VerifyHop(msg.ID, hop) {
		t.Error("provenance hop signature is invalid")
	}
}

// TestSendFilesystem_Antecedents verifies that antecedents are attached to
// the sent message.
func TestSendFilesystem_Antecedents(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	// Send a first message to get its ID.
	first, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("first"),
	})
	if err != nil {
		t.Fatalf("Send first: %v", err)
	}

	// Send a reply with the first message as antecedent.
	reply, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte("reply"),
		Antecedents: []string{first.ID},
	})
	if err != nil {
		t.Fatalf("Send reply: %v", err)
	}
	if len(reply.Antecedents) != 1 || reply.Antecedents[0] != first.ID {
		t.Errorf("antecedents mismatch: got %v, want [%s]", reply.Antecedents, first.ID)
	}
}

// TestSendFilesystem_Instance verifies the Instance field is propagated to
// the message (tainted metadata, not covered by signature).
func TestSendFilesystem_Instance(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("msg"),
		Instance:   "implementer",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg.Instance != "implementer" {
		t.Errorf("instance mismatch: got %q, want %q", msg.Instance, "implementer")
	}
}

// TestSendFilesystem_NotMember verifies that Send returns an error when the
// agent is not a member of the campfire.
func TestSendFilesystem_NotMember(t *testing.T) {
	agentID, s, _ := setupTestEnv(t)

	client := protocol.New(s, agentID)
	_, err := client.Send(protocol.SendRequest{
		CampfireID: strings.Repeat("a", 64),
		Payload:    []byte("msg"),
	})
	if err == nil {
		t.Fatal("expected error for non-member, got nil")
	}
}

// TestSendFilesystem_ObserverRejected verifies that an observer-role member
// cannot send messages via the filesystem transport.
func TestSendFilesystem_ObserverRejected(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleObserver)

	client := protocol.New(s, agentID)
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("msg"),
	})
	if err == nil {
		t.Fatal("expected role error for observer, got nil")
	}
	var roleErr *protocol.RoleError
	if !protocol.IsRoleError(err, &roleErr) {
		t.Errorf("expected RoleError, got: %v", err)
	}
}

// TestSendFilesystem_WriterSystemTagRejected verifies that a writer-role member
// cannot send campfire:* system messages.
func TestSendFilesystem_WriterSystemTagRejected(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleWriter)

	client := protocol.New(s, agentID)
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("msg"),
		Tags:       []string{"campfire:compact"},
	})
	if err == nil {
		t.Fatal("expected role error for writer sending system tag, got nil")
	}
	var roleErr *protocol.RoleError
	if !protocol.IsRoleError(err, &roleErr) {
		t.Errorf("expected RoleError, got: %v", err)
	}
}

// TestSendFilesystem_WriterCanSendNonSystem verifies that a writer-role member
// can send non-system messages.
func TestSendFilesystem_WriterCanSendNonSystem(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleWriter)

	client := protocol.New(s, agentID)
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("hello"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("writer should be able to send non-system messages: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
}

// TestRoleError_ErrorsAs verifies that RoleError satisfies the errors.As idiom,
// which is the documented contract in the Send doc comment. This is the
// campfire-agent-oqp fix: RoleError.As() must be implemented so that
// errors.As(err, &roleErr) works for wrapped errors too.
func TestRoleError_ErrorsAs(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleObserver)

	client := protocol.New(s, agentID)
	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("msg"),
	})
	if err == nil {
		t.Fatal("expected error for observer, got nil")
	}

	// Primary assertion: errors.As must work directly.
	var roleErr *protocol.RoleError
	if !errors.As(err, &roleErr) {
		t.Errorf("errors.As(*RoleError) returned false; err = %v", err)
	}
	if roleErr == nil {
		t.Error("errors.As set roleErr to nil")
	}

	// Secondary assertion: IsRoleError still works (backward compat).
	var roleErr2 *protocol.RoleError
	if !protocol.IsRoleError(err, &roleErr2) {
		t.Errorf("IsRoleError returned false; err = %v", err)
	}
}

// TestSendFilesystem_MultipleMessages verifies that multiple messages can be
// sent to the same campfire and each gets a unique ID.
func TestSendFilesystem_MultipleMessages(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		msg, err := client.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte(fmt.Sprintf("message %d", i)),
		})
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		if seen[msg.ID] {
			t.Errorf("duplicate message ID: %s", msg.ID)
		}
		seen[msg.ID] = true
	}

	messagesDir := filepath.Join(transportDir, campfireID, "messages")
	entries, err := os.ReadDir(messagesDir)
	if err != nil {
		t.Fatalf("reading messages dir: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("expected 5 message files, got %d", len(entries))
	}
}
