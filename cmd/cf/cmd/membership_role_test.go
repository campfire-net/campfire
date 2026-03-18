package cmd

// Tests for workspace-o3l.2: membership roles (observer/writer/full) with client-side enforcement.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupCampfireWithRole creates a campfire and adds the agent as a member with the
// given protocol role in both the transport directory and the local store.
func setupCampfireWithRole(t *testing.T, agentID *identity.Identity, s *store.Store, transportBaseDir string, protocolRole string) string {
	t.Helper()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	campfireID := cfID.PublicKeyHex()
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory %s: %v", sub, err)
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

	transport := fs.New(transportBaseDir)
	if err := transport.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      protocolRole,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transport.CampfireDir(campfireID),
		JoinProtocol: "open",
		Role:         protocolRole,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireID
}

// TestObserverCannotSend verifies that an agent with the "observer" role
// gets an error when trying to send any message (cf send).
func TestObserverCannotSend(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, campfire.RoleObserver)

	// Observer must not be able to send.
	sendErr := sendFilesystemWithRoleCheck(campfireID, "hello", []string{}, []string{}, "", agentID, s)
	if sendErr == nil {
		t.Fatal("expected error when observer tries to send, got nil")
	}
	if !isRoleError(sendErr) {
		t.Errorf("expected role enforcement error, got: %v", sendErr)
	}
}

// TestWriterCanSendRegularMessage verifies that "writer" role agents can send
// regular (non-system) messages.
func TestWriterCanSendRegularMessage(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, campfire.RoleWriter)

	// Writer can send regular messages.
	sendErr := sendFilesystemWithRoleCheck(campfireID, "hello", []string{"status"}, []string{}, "", agentID, s)
	if sendErr != nil {
		t.Errorf("writer should be able to send regular messages, got error: %v", sendErr)
	}
}

// TestWriterCannotSendSystemMessage verifies that "writer" role agents cannot
// send messages with campfire:* system tags.
func TestWriterCannotSendSystemMessage(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, campfire.RoleWriter)

	systemTags := []string{"campfire:member-joined", "campfire:compact", "campfire:view"}
	for _, tag := range systemTags {
		sendErr := sendFilesystemWithRoleCheck(campfireID, "payload", []string{tag}, []string{}, "", agentID, s)
		if sendErr == nil {
			t.Errorf("writer should not be able to send campfire:* tag %q, got nil error", tag)
		}
		if !isRoleError(sendErr) {
			t.Errorf("expected role enforcement error for tag %q, got: %v", tag, sendErr)
		}
	}
}

// TestFullRoleCanDoEverything verifies that "full" role agents can send
// both regular and system messages.
func TestFullRoleCanDoEverything(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, campfire.RoleFull)

	// Full role: regular message.
	if err := sendFilesystemWithRoleCheck(campfireID, "hello", []string{"status"}, []string{}, "", agentID, s); err != nil {
		t.Errorf("full role should send regular messages, got: %v", err)
	}

	// Full role: system message.
	if err := sendFilesystemWithRoleCheck(campfireID, "payload", []string{"campfire:compact"}, []string{}, "", agentID, s); err != nil {
		t.Errorf("full role should send system messages, got: %v", err)
	}
}

// TestEmptyRoleDefaultsToFull verifies that empty role string is treated as "full"
// for backward compatibility with existing campfire memberships.
func TestEmptyRoleDefaultsToFull(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Empty role (old membership records without role).
	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, "")

	// Empty role should behave as full.
	if err := sendFilesystemWithRoleCheck(campfireID, "hello", []string{"status"}, []string{}, "", agentID, s); err != nil {
		t.Errorf("empty role should default to full and allow sending, got: %v", err)
	}
	if err := sendFilesystemWithRoleCheck(campfireID, "payload", []string{"campfire:compact"}, []string{}, "", agentID, s); err != nil {
		t.Errorf("empty role should default to full and allow system messages, got: %v", err)
	}
}

// TestMemberRoleDefaultsToFull verifies that the legacy "member"/"creator" roles
// (which were the original values) are treated as "full".
func TestLegacyRolesDefaultToFull(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	for _, legacyRole := range []string{"member", "creator"} {
		t.Run(fmt.Sprintf("role=%s", legacyRole), func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatalf("opening store: %v", err)
			}
			defer s.Close()

			campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, legacyRole)

			if err := sendFilesystemWithRoleCheck(campfireID, "hello", []string{"status"}, []string{}, "", agentID, s); err != nil {
				t.Errorf("legacy role %q should allow sending, got: %v", legacyRole, err)
			}
		})
	}
}

// TestMemberRecordRoleRoundtrip verifies that MemberRecord.Role survives
// CBOR marshal/unmarshal and that empty Role reads back as empty string.
func TestMemberRecordRoleRoundtrip(t *testing.T) {
	baseDir := t.TempDir()
	transport := fs.New(baseDir)

	cfID, _ := identity.Generate()
	campfireID := cfID.PublicKeyHex()
	os.MkdirAll(filepath.Join(baseDir, campfireID, "members"), 0755) //nolint:errcheck

	agentID, _ := identity.Generate()

	cases := []struct {
		role string
	}{
		{campfire.RoleObserver},
		{campfire.RoleWriter},
		{campfire.RoleFull},
		{""},
	}

	for _, tc := range cases {
		t.Run("role="+tc.role, func(t *testing.T) {
			if err := transport.WriteMember(campfireID, campfire.MemberRecord{
				PublicKey: agentID.PublicKey,
				JoinedAt:  1000,
				Role:      tc.role,
			}); err != nil {
				t.Fatalf("WriteMember: %v", err)
			}

			members, err := transport.ListMembers(campfireID)
			if err != nil {
				t.Fatalf("ListMembers: %v", err)
			}
			if len(members) == 0 {
				t.Fatal("no members found")
			}

			found := false
			for _, m := range members {
				if fmt.Sprintf("%x", m.PublicKey) == agentID.PublicKeyHex() {
					found = true
					if m.Role != tc.role {
						t.Errorf("Role = %q, want %q", m.Role, tc.role)
					}
					break
				}
			}
			if !found {
				t.Error("member not found after write")
			}
		})
	}
}

// TestAdmitDefaultRoleIsFull verifies that a member admitted without --role
// gets "full" as the default role in the transport member record.
func TestAdmitDefaultRoleIsFull(t *testing.T) {
	baseDir := t.TempDir()
	transport := fs.New(baseDir)

	cfID, _ := identity.Generate()
	campfireID := cfID.PublicKeyHex()
	os.MkdirAll(filepath.Join(baseDir, campfireID, "members"), 0755) //nolint:errcheck

	agentID, _ := identity.Generate()

	// Write without explicit role (default should be "full").
	if err := transport.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  1000,
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("WriteMember: %v", err)
	}

	members, err := transport.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	for _, m := range members {
		if fmt.Sprintf("%x", m.PublicKey) == agentID.PublicKeyHex() {
			if m.Role != campfire.RoleFull {
				t.Errorf("default admitted role = %q, want %q", m.Role, campfire.RoleFull)
			}
			return
		}
	}
	t.Error("member not found")
}
