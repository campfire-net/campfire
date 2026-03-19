package cmd

// role.go — client-side enforcement of membership roles.
// Phase 1: client enforces only. Transport-level enforcement is future work.

import (
	"fmt"
	"strings"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// roleEnforcementError is a sentinel error type for role enforcement failures.
// Tests use isRoleError() to distinguish role errors from other errors.
type roleEnforcementError struct {
	msg string
}

func (e *roleEnforcementError) Error() string { return e.msg }

// isRoleError returns true if the error is a role enforcement error.
func isRoleError(err error) bool {
	_, ok := err.(*roleEnforcementError)
	return ok
}

// hasSystemTag returns true if any tag in the list is a campfire:* system tag.
func hasSystemTag(tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, "campfire:") {
			return true
		}
	}
	return false
}

// checkRoleCanSend verifies that the membership role allows sending the
// message with the given tags. Returns a roleEnforcementError if not allowed.
func checkRoleCanSend(role string, tags []string) error {
	effective := campfire.EffectiveRole(role)
	switch effective {
	case campfire.RoleObserver:
		return &roleEnforcementError{
			msg: "role observer: cannot send messages (read-only membership)",
		}
	case campfire.RoleWriter:
		if hasSystemTag(tags) {
			return &roleEnforcementError{
				msg: "role writer: cannot send campfire:* system messages (requires full membership)",
			}
		}
		return nil
	default: // full
		return nil
	}
}

// sendFilesystemWithRoleCheck is the role-enforcing wrapper around sendFilesystem.
// It checks the membership role from the store before attempting to send.
// This is the integration point used by tests and the send command.
func sendFilesystemWithRoleCheck(campfireID, payload string, tags, antecedents []string, instance string, agentID *identity.Identity, s *store.Store) error {
	m, err := s.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return fmt.Errorf("not a member of campfire %s", campfireID[:min(12, len(campfireID))])
	}

	if err := checkRoleCanSend(m.Role, tags); err != nil {
		return err
	}

	_, err = sendFilesystem(campfireID, payload, tags, antecedents, instance, agentID, m.TransportDir)
	return err
}
