package tagspec_test

import (
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/internal/tagspec"
)

// TestPrefixesNonEmpty verifies all prefix constants are non-empty strings.
func TestPrefixesNonEmpty(t *testing.T) {
	prefixes := []struct {
		name string
		val  string
	}{
		{"CampfirePrefix", tagspec.CampfirePrefix},
		{"ConventionPrefix", tagspec.ConventionPrefix},
		{"SessionPrefix", tagspec.SessionPrefix},
	}
	for _, p := range prefixes {
		if p.val == "" {
			t.Errorf("%s must be non-empty", p.name)
		}
	}
}

// TestCampfirePrefixDistinct verifies campfire: and session: prefixes differ.
func TestCampfirePrefixDistinct(t *testing.T) {
	if tagspec.CampfirePrefix == tagspec.SessionPrefix {
		t.Error("CampfirePrefix and SessionPrefix must be distinct")
	}
	if tagspec.CampfirePrefix == tagspec.ConventionPrefix {
		t.Error("CampfirePrefix and ConventionPrefix must be distinct")
	}
}

// TestPrefixesEndWithColon verifies all prefixes end with ":" per campfire convention.
func TestPrefixesEndWithColon(t *testing.T) {
	for _, p := range []string{tagspec.CampfirePrefix, tagspec.ConventionPrefix, tagspec.SessionPrefix} {
		if !strings.HasSuffix(p, ":") {
			t.Errorf("prefix %q must end with ':', got %q", p, p)
		}
	}
}
