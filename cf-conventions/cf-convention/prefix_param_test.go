// Package convention — tag-prefix denylist parameter tests (campfireagent-28f, leak #6 closure).
//
// These tests verify that:
//  1. Parse accepts a deniedPrefixes parameter (replaces the hard-coded import of pkg/naming).
//  2. An empty prefix list causes all tag-prefixed productions to be accepted (no false denials).
//  3. A custom prefix list accepts only the declared prefixes; other prefixes pass through.
//  4. The DefaultDeniedTagPrefixes list preserves existing behavior (naming: and campfire: still denied).
package convention

import (
	"testing"
)

// TestParse_EmptyPrefixList verifies that Parse with an empty prefix list does NOT
// deny tags by prefix (though exact-match denials still apply).
//
// Adversarial condition 1: empty prefix list → all prefix-based denials suppressed.
func TestParse_EmptyPrefixList(t *testing.T) {
	// "naming:foo" would be denied under the default list. With an empty list it is allowed.
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "naming:foo", "cardinality": "exactly_one"},
		},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey, nil)
	if err != nil {
		t.Errorf("expected no error with empty prefix list, got: %v", err)
	}
}

// TestParse_CustomPrefixList verifies that a caller-supplied prefix list is honoured:
// a tag matching the custom prefix is denied; a tag NOT matching the custom prefix passes.
//
// Adversarial condition 2: custom prefix list.
func TestParse_CustomPrefixList(t *testing.T) {
	customPrefixes := []string{"custom:"}

	// "custom:tag" should be denied.
	payloadDenied := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "custom:tag", "cardinality": "exactly_one"},
		},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payloadDenied, testSenderKey, testCampfireKey, customPrefixes)
	if err == nil {
		t.Error("expected error for tag matching custom denied prefix, got nil")
	}

	// "naming:foo" is NOT in the custom prefix list → allowed (no default list applied).
	payloadAllowed := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "naming:foo", "cardinality": "exactly_one"},
		},
	})
	_, _, err = Parse(tags(ConventionOperationTag), payloadAllowed, testSenderKey, testCampfireKey, customPrefixes)
	if err != nil {
		t.Errorf("expected no error for tag not in custom prefix list, got: %v", err)
	}
}

// TestParse_DefaultPrefixListPreservesNamingDenial verifies that DefaultDeniedTagPrefixes
// still denies naming:-prefixed tags (existing behaviour preserved).
//
// Adversarial condition 3: default list matches existing behaviour.
func TestParse_DefaultPrefixListPreservesNamingDenial(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "naming:foo", "cardinality": "exactly_one"},
		},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey, DefaultDeniedTagPrefixes)
	if err == nil {
		t.Error("expected error: naming:foo should be denied by DefaultDeniedTagPrefixes")
	}
}

// TestParse_DefaultPrefixListPreservesCampfireDenial verifies that DefaultDeniedTagPrefixes
// still denies campfire:-prefixed tags (existing behaviour preserved).
func TestParse_DefaultPrefixListPreservesCampfireDenial(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "campfire:custom", "cardinality": "exactly_one"},
		},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey, DefaultDeniedTagPrefixes)
	if err == nil {
		t.Error("expected error: campfire:custom should be denied by DefaultDeniedTagPrefixes")
	}
}

// TestParse_NamingURIExceptionWithDefaultPrefixes verifies the naming-uri exception still
// works when DefaultDeniedTagPrefixes is passed: the naming-uri convention may produce
// naming: tags even when the default prefix list includes "naming:".
func TestParse_NamingURIExceptionWithDefaultPrefixes(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "naming-uri", "version": "1", "operation": "register", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "naming:name:foo", "cardinality": "exactly_one"},
		},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey, DefaultDeniedTagPrefixes)
	if err != nil {
		t.Errorf("naming-uri exception should still work with DefaultDeniedTagPrefixes: %v", err)
	}
}
