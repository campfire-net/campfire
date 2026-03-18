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

// autoJoinRootCampfire joins an open-protocol filesystem campfire automatically.
// It is only called when the campfire ID came from ProjectRoot() and the agent
// is not yet a member. Returns nil if successfully joined or if the campfire is
// invite-only (skips silently). Returns an error only on unexpected failures.
func autoJoinRootCampfire(campfireID string, agentID *identity.Identity, s *store.Store) error {
	transport := fs.New(fs.DefaultBaseDir())

	// Read campfire state to check join protocol.
	state, err := transport.ReadState(campfireID)
	if err != nil {
		// Transport state not found — can't auto-join, skip silently.
		return nil
	}

	// Only auto-join open campfires.
	if state.JoinProtocol != "open" {
		return nil
	}

	// Check if already on disk (pre-admitted via cf admit).
	members, err := transport.ListMembers(campfireID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	alreadyOnDisk := false
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
		// Pre-admitted — use the existing join timestamp and role.
		now = existingJoinedAt
	} else {
		// Write member record to transport directory.
		if err := transport.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: agentID.PublicKey,
			JoinedAt:  now,
			Role:      campfire.RoleFull,
		}); err != nil {
			return fmt.Errorf("writing member record: %w", err)
		}
		existingRole = campfire.RoleFull
	}

	// Write campfire:member-joined system message (only if newly admitted).
	if !alreadyOnDisk {
		sysMsg, err := message.NewMessage(
			state.PrivateKey, state.PublicKey,
			[]byte(fmt.Sprintf(`{"member":"%s","joined_at":%d}`, agentID.PublicKeyHex(), now)),
			[]string{"campfire:member-joined"},
			nil,
		)
		if err != nil {
			return fmt.Errorf("creating system message: %w", err)
		}

		updatedMembers, _ := transport.ListMembers(campfireID)
		cf := campfireFromState(state, updatedMembers)
		if err := sysMsg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(updatedMembers),
			state.JoinProtocol, state.ReceptionRequirements,
		); err != nil {
			return fmt.Errorf("adding provenance hop: %w", err)
		}

		if err := transport.WriteMessage(campfireID, sysMsg); err != nil {
			return fmt.Errorf("writing system message: %w", err)
		}
	}

	// Record membership in local store.
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transport.CampfireDir(campfireID),
		JoinProtocol: state.JoinProtocol,
		Role:         existingRole,
		JoinedAt:     now,
	}); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	fmt.Printf("Auto-joined campfire %s\n", campfireID[:12])
	return nil
}
