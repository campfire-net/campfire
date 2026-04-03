package cmd

// role.go — client-side enforcement of membership roles.
// Phase 1: client enforces only. Transport-level enforcement is future work.

import (
	"strings"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
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
		if strings.HasPrefix(t, campfire.TagPrefix) {
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

// sendWithRoleCheck sends a message via protocol.Client.Send, which
// enforces membership roles. Role errors are wrapped as roleEnforcementError so
// that test helpers using isRoleError() continue to work.
func sendWithRoleCheck(campfireID, payload string, tags, antecedents []string, instance string, agentID *identity.Identity, s store.Store) error {
	client := protocol.New(s, agentID)
	_, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte(payload),
		Tags:        tags,
		Antecedents: antecedents,
		Instance:    instance,
	})
	if err == nil {
		return nil
	}
	// Translate protocol.RoleError to the local roleEnforcementError so that
	// test helpers using isRoleError() continue to distinguish role errors.
	var re *protocol.RoleError
	if protocol.IsRoleError(err, &re) {
		return &roleEnforcementError{msg: re.Error()}
	}
	return err
}
