package cmd

import (
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// admitFSMemberIfNew handles the shared filesystem member-admission logic used by
// both joinFilesystem and autoJoinRootCampfire:
//  1. Check whether agentID is already in the transport's member list.
//  2. If not, write a member record (open protocol only — callers are responsible
//     for enforcing invite-only before calling this).
//  3. Write the campfire:member-joined system message with a provenance hop.
//
// Returns (joinedAt, role, alreadyOnDisk, err).
// joinedAt is the resolved Unix-nano timestamp (pre-admission timestamp if already on disk).
// role is the resolved role (from existing record or campfire.RoleFull for new joins).
func admitFSMemberIfNew(
	transport *fs.Transport,
	campfireID string,
	agentID *identity.Identity,
	state *campfire.CampfireState,
) (joinedAt int64, role string, alreadyOnDisk bool, err error) {
	members, err := transport.ListMembers(campfireID)
	if err != nil {
		return 0, "", false, fmt.Errorf("listing members: %w", err)
	}
	var existingJoinedAt int64
	var existingRole string
	for _, m := range members {
		if fmt.Sprintf("%x", m.PublicKey) == agentID.PublicKeyHex() {
			alreadyOnDisk = true
			existingJoinedAt = m.JoinedAt
			existingRole = m.Role
			break
		}
	}

	now := time.Now().UnixNano()
	if alreadyOnDisk {
		now = existingJoinedAt
		return now, existingRole, true, nil
	}

	// Write member record to transport directory.
	if err := transport.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  now,
		Role:      campfire.RoleFull,
	}); err != nil {
		return 0, "", false, fmt.Errorf("writing member record: %w", err)
	}

	// Write campfire:member-joined system message.
	sysMsg, err := message.NewMessage(
		state.PrivateKey, state.PublicKey,
		[]byte(fmt.Sprintf(`{"member":"%s","joined_at":%d}`, agentID.PublicKeyHex(), now)),
		[]string{"campfire:member-joined"},
		nil,
	)
	if err != nil {
		return 0, "", false, fmt.Errorf("creating system message: %w", err)
	}

	updatedMembers, _ := transport.ListMembers(campfireID)
	cf := campfireFromState(state, updatedMembers)
	if err := sysMsg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(updatedMembers),
		state.JoinProtocol, state.ReceptionRequirements,
		campfire.RoleFull,
	); err != nil {
		return 0, "", false, fmt.Errorf("adding provenance hop: %w", err)
	}

	if err := transport.WriteMessage(campfireID, sysMsg); err != nil {
		return 0, "", false, fmt.Errorf("writing system message: %w", err)
	}

	return now, campfire.RoleFull, false, nil
}

// autoJoinRootCampfire joins an open-protocol filesystem campfire automatically.
// It is only called when the campfire ID came from ProjectRoot() and the agent
// is not yet a member. Returns nil if successfully joined or if the campfire is
// invite-only (skips silently). Returns an error only on unexpected failures.
func autoJoinRootCampfire(campfireID string, agentID *identity.Identity, s store.Store) error {
	transportDir := resolveFSTransportDir(campfireID)
	tr := fs.ForDir(transportDir)

	// Read campfire state to check join protocol.
	state, err := tr.ReadState(campfireID)
	if err != nil {
		// Transport state not found — can't auto-join, skip silently.
		return nil
	}

	// Only auto-join open campfires.
	if state.JoinProtocol != "open" {
		return nil
	}

	now, role, _, err := admitFSMemberIfNew(tr, campfireID, agentID, state)
	if err != nil {
		return err
	}

	// Record membership in local store.
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: tr.CampfireDir(campfireID),
		JoinProtocol: state.JoinProtocol,
		Role:         role,
		JoinedAt:     now,
	}); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	fmt.Printf("Auto-joined campfire %s\n", campfireID[:12])
	return nil
}
