// Package convention_test asserts that tag constants shared between the
// convention and delegation packages stay in sync.
package convention_test

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/convention/delegation"
)

// TestRevokedTagMatchesConventionConstant guards against silent drift between
// the convention.IdentityRevokedTag constant (pkg/convention/identity.go) and
// delegation.RevokedTag (pkg/convention/delegation/trust.go).
//
// The delegation package cannot import convention (convention imports
// delegation), so the string "identity:revoked" is duplicated by necessity.
// This test is the enforcement mechanism (campfire-3a8): if either constant
// changes, this test will fail immediately.
func TestRevokedTagMatchesConventionConstant(t *testing.T) {
	if delegation.RevokedTag != convention.IdentityRevokedTag {
		t.Errorf("delegation.RevokedTag %q != convention.IdentityRevokedTag %q — update both constants together",
			delegation.RevokedTag, convention.IdentityRevokedTag)
	}
}
