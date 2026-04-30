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

// TestSessionTagWireValues verifies the session:open and session:close tag wire values
// are frozen at their canonical strings (design v2 §10.6).
func TestSessionTagWireValues(t *testing.T) {
	if tagspec.TagSessionOpen != "session:open" {
		t.Errorf("TagSessionOpen = %q, want %q", tagspec.TagSessionOpen, "session:open")
	}
	if tagspec.TagSessionClose != "session:close" {
		t.Errorf("TagSessionClose = %q, want %q", tagspec.TagSessionClose, "session:close")
	}
}

// TestSessionTagsUseSessionPrefix verifies that both session lifecycle tags
// start with the SessionPrefix (not campfire: or convention:).
func TestSessionTagsUseSessionPrefix(t *testing.T) {
	for _, tag := range []struct {
		name string
		val  string
	}{
		{"TagSessionOpen", tagspec.TagSessionOpen},
		{"TagSessionClose", tagspec.TagSessionClose},
	} {
		if !strings.HasPrefix(tag.val, tagspec.SessionPrefix) {
			t.Errorf("%s = %q does not start with SessionPrefix %q",
				tag.name, tag.val, tagspec.SessionPrefix)
		}
		if strings.HasPrefix(tag.val, tagspec.CampfirePrefix) {
			t.Errorf("%s = %q must NOT start with CampfirePrefix", tag.name, tag.val)
		}
		if strings.HasPrefix(tag.val, tagspec.ConventionPrefix) {
			t.Errorf("%s = %q must NOT start with ConventionPrefix", tag.name, tag.val)
		}
	}
}

// TestSessionTagsDistinct verifies that session:open and session:close are different.
func TestSessionTagsDistinct(t *testing.T) {
	if tagspec.TagSessionOpen == tagspec.TagSessionClose {
		t.Errorf("TagSessionOpen and TagSessionClose must differ; both = %q", tagspec.TagSessionOpen)
	}
}
