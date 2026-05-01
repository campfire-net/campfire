// Package convention_test asserts that tag constants shared between the
// identity extension and delegation packages stay in sync.
package convention_test

import (
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/delegation"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/identity"
)

// TestRevokedTagMatchesConventionConstant guards against silent drift between
// the identity.IdentityRevokedTag constant (cf-convention-extensions/identity/identity.go) and
// delegation.RevokedTag (cf-convention-extensions/delegation/trust.go).
//
// The delegation package cannot import identity (would create a cross-extension dependency),
// so the string "identity:revoked" is duplicated by necessity.
// This test is the enforcement mechanism (campfire-3a8): if either constant
// changes, this test will fail immediately.
func TestRevokedTagMatchesConventionConstant(t *testing.T) {
	if delegation.RevokedTag != identity.IdentityRevokedTag {
		t.Errorf("delegation.RevokedTag %q != identity.IdentityRevokedTag %q — update both constants together",
			delegation.RevokedTag, identity.IdentityRevokedTag)
	}
}
