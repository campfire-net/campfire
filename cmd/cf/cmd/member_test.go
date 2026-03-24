package cmd

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupMemberSetRoleEnv creates a minimal campfire environment for set-role tests.
// Returns campfireID, caller identity (full role), target identity, store, and fs transport.
func setupMemberSetRoleEnv(t *testing.T) (campfireID string, caller *identity.Identity, target *identity.Identity, s store.Store, fsT *fs.Transport) {
	t.Helper()

	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	var err error
	caller, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating caller identity: %v", err)
	}
	if err := caller.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving caller identity: %v", err)
	}

	target, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating target identity: %v", err)
	}

	s, err = store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID = cfID.PublicKeyHex()

	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory %s: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "invite-only",
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

	fsT = fs.New(transportBaseDir)

	if err := fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: caller.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing caller member record: %v", err)
	}

	if err := fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: target.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleWriter,
	}); err != nil {
		t.Fatalf("writing target member record: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: fsT.CampfireDir(campfireID),
		JoinProtocol: "invite-only",
		Role:         campfire.RoleFull,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding caller membership: %v", err)
	}

	return campfireID, caller, target, s, fsT
}

// memberSetRoleCore is a testable helper implementing the core set-role logic.
func memberSetRoleCore(campfireID, targetPubkeyHex, newRole string, callerID *identity.Identity, s store.Store, fsT *fs.Transport) error {
	switch newRole {
	case campfire.RoleObserver, campfire.RoleWriter, campfire.RoleFull:
		// valid
	default:
		return fmt.Errorf("invalid role %q: must be one of observer, writer, full", newRole)
	}

	if _, err := hex.DecodeString(targetPubkeyHex); err != nil {
		return fmt.Errorf("invalid public key hex: %w", err)
	}

	m, err := s.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return fmt.Errorf("not a member of campfire %s", campfireID[:12])
	}
	if campfire.EffectiveRole(m.Role) != campfire.RoleFull {
		return fmt.Errorf("role change requires full membership (your role: %s)", m.Role)
	}

	if targetPubkeyHex == callerID.PublicKeyHex() {
		return fmt.Errorf("cannot change your own role")
	}

	members, err := fsT.ListMembers(campfireID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	var targetMember *campfire.MemberRecord
	for i, mem := range members {
		if fmt.Sprintf("%x", mem.PublicKey) == targetPubkeyHex {
			targetMember = &members[i]
			break
		}
	}
	if targetMember == nil {
		return fmt.Errorf("member %s not found in campfire %s", targetPubkeyHex[:12], campfireID[:12])
	}

	previousRole := campfire.EffectiveRole(targetMember.Role)
	if previousRole == newRole {
		return fmt.Errorf("member %s already has role %s", targetPubkeyHex[:12], newRole)
	}

	if err := fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: targetMember.PublicKey,
		JoinedAt:  targetMember.JoinedAt,
		Role:      newRole,
	}); err != nil {
		return fmt.Errorf("writing updated member record: %w", err)
	}

	now := time.Now().UnixNano()
	state, err := fsT.ReadState(campfireID)
	if err != nil {
		return fmt.Errorf("reading campfire state: %w", err)
	}

	payload := fmt.Sprintf(
		`{"member":%q,"previous_role":%q,"new_role":%q,"changed_at":%d}`,
		targetPubkeyHex, previousRole, newRole, now,
	)
	sysMsg, err := message.NewMessage(
		state.PrivateKey, state.PublicKey,
		[]byte(payload),
		[]string{"campfire:member-role-changed"},
		nil,
	)
	if err != nil {
		return fmt.Errorf("creating system message: %w", err)
	}

	updatedMembers, _ := fsT.ListMembers(campfireID)
	cf := campfireFromState(state, updatedMembers)
	if err := sysMsg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(updatedMembers),
		state.JoinProtocol, state.ReceptionRequirements,
		campfire.RoleFull,
	); err != nil {
		return fmt.Errorf("adding provenance hop: %w", err)
	}

	return fsT.WriteMessage(campfireID, sysMsg)
}

// TestMemberSetRole_FullCanChange verifies a full-role caller can change another member's role.
func TestMemberSetRole_FullCanChange(t *testing.T) {
	campfireID, caller, target, s, fsT := setupMemberSetRoleEnv(t)
	defer s.Close()

	targetHex := fmt.Sprintf("%x", target.PublicKey)

	if err := memberSetRoleCore(campfireID, targetHex, campfire.RoleObserver, caller, s, fsT); err != nil {
		t.Fatalf("memberSetRoleCore: %v", err)
	}

	members, err := fsT.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	found := false
	for _, m := range members {
		if fmt.Sprintf("%x", m.PublicKey) == targetHex {
			found = true
			if m.Role != campfire.RoleObserver {
				t.Errorf("transport role = %q, want %q", m.Role, campfire.RoleObserver)
			}
		}
	}
	if !found {
		t.Error("target member not found in transport after role change")
	}
}

// TestMemberSetRole_SystemMessageEmitted verifies a campfire:member-role-changed
// message is written to the transport after a role change.
func TestMemberSetRole_SystemMessageEmitted(t *testing.T) {
	campfireID, caller, target, s, fsT := setupMemberSetRoleEnv(t)
	defer s.Close()

	targetHex := fmt.Sprintf("%x", target.PublicKey)

	if err := memberSetRoleCore(campfireID, targetHex, campfire.RoleObserver, caller, s, fsT); err != nil {
		t.Fatalf("memberSetRoleCore: %v", err)
	}

	msgs, err := fsT.ListMessages(campfireID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	found := false
	for _, msg := range msgs {
		for _, tag := range msg.Tags {
			if tag == "campfire:member-role-changed" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("expected campfire:member-role-changed system message in transport, not found")
	}
}

// TestMemberSetRole_ObserverCannotChange verifies an observer cannot change roles.
func TestMemberSetRole_ObserverCannotChange(t *testing.T) {
	campfireID, _, target, s, fsT := setupMemberSetRoleEnv(t)
	defer s.Close()

	// Override caller's membership role to observer.
	observerID, _ := identity.Generate()
	if err := s.RemoveMembership(campfireID); err != nil {
		t.Fatalf("removing membership: %v", err)
	}
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: fsT.CampfireDir(campfireID),
		JoinProtocol: "invite-only",
		Role:         campfire.RoleObserver,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding observer membership: %v", err)
	}

	targetHex := fmt.Sprintf("%x", target.PublicKey)
	err := memberSetRoleCore(campfireID, targetHex, campfire.RoleWriter, observerID, s, fsT)
	if err == nil {
		t.Fatal("expected error when observer tries to change role, got nil")
	}
}

// TestMemberSetRole_WriterCannotChange verifies a writer cannot change roles.
func TestMemberSetRole_WriterCannotChange(t *testing.T) {
	campfireID, _, target, s, fsT := setupMemberSetRoleEnv(t)
	defer s.Close()

	writerID, _ := identity.Generate()
	if err := s.RemoveMembership(campfireID); err != nil {
		t.Fatalf("removing membership: %v", err)
	}
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: fsT.CampfireDir(campfireID),
		JoinProtocol: "invite-only",
		Role:         campfire.RoleWriter,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding writer membership: %v", err)
	}

	targetHex := fmt.Sprintf("%x", target.PublicKey)
	err := memberSetRoleCore(campfireID, targetHex, campfire.RoleFull, writerID, s, fsT)
	if err == nil {
		t.Fatal("expected error when writer tries to change role, got nil")
	}
}

// TestMemberSetRole_SelfChangeBlocked verifies a member cannot change their own role.
func TestMemberSetRole_SelfChangeBlocked(t *testing.T) {
	campfireID, caller, _, s, fsT := setupMemberSetRoleEnv(t)
	defer s.Close()

	callerHex := fmt.Sprintf("%x", caller.PublicKey)
	err := memberSetRoleCore(campfireID, callerHex, campfire.RoleObserver, caller, s, fsT)
	if err == nil {
		t.Fatal("expected error when member tries to change their own role, got nil")
	}
}

// TestMemberSetRole_InvalidRole verifies that an invalid role returns an error.
func TestMemberSetRole_InvalidRole(t *testing.T) {
	campfireID, caller, target, s, fsT := setupMemberSetRoleEnv(t)
	defer s.Close()

	targetHex := fmt.Sprintf("%x", target.PublicKey)
	err := memberSetRoleCore(campfireID, targetHex, "superadmin", caller, s, fsT)
	if err == nil {
		t.Fatal("expected error for invalid role, got nil")
	}
}

// TestMemberSetRole_TargetNotFound verifies an error when target is not a member.
func TestMemberSetRole_TargetNotFound(t *testing.T) {
	campfireID, caller, _, s, fsT := setupMemberSetRoleEnv(t)
	defer s.Close()

	unknownID, _ := identity.Generate()
	unknownHex := fmt.Sprintf("%x", unknownID.PublicKey)
	err := memberSetRoleCore(campfireID, unknownHex, campfire.RoleObserver, caller, s, fsT)
	if err == nil {
		t.Fatal("expected error when target is not a member, got nil")
	}
}

// TestMemberSetRole_AlreadySameRole verifies an error when target already has the requested role.
func TestMemberSetRole_AlreadySameRole(t *testing.T) {
	campfireID, caller, target, s, fsT := setupMemberSetRoleEnv(t)
	defer s.Close()

	targetHex := fmt.Sprintf("%x", target.PublicKey)
	// Target is already a writer — requesting writer again should error.
	err := memberSetRoleCore(campfireID, targetHex, campfire.RoleWriter, caller, s, fsT)
	if err == nil {
		t.Fatal("expected error when requesting same role, got nil")
	}
}
