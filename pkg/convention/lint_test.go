package convention

import (
	"strings"
	"testing"
)

// TestLint_ValidDeclaration verifies that a normal well-formed declaration passes Lint.
func TestLint_ValidDeclaration(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention":  "my-convention",
		"version":     "0.1",
		"operation":   "publish",
		"description": "Publish something",
		"signing":     "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "social:post", "cardinality": "exactly_one"},
		},
	})
	result := Lint(payload)
	if !result.Valid {
		t.Errorf("expected Valid=true, got errors: %v", result.Errors)
	}
}

// TestLint_DeniedTagExact verifies that a regular convention cannot produce exactly-denied tags.
func TestLint_DeniedTagExact(t *testing.T) {
	for _, deniedTag := range []string{"future", "fulfills", "convention:operation", "convention:schema", "convention:revoke"} {
		t.Run(deniedTag, func(t *testing.T) {
			payload := mustJSON(map[string]any{
				"convention": "my-convention",
				"version":    "0.1",
				"operation":  "op",
				"signing":    "member_key",
				"produces_tags": []any{
					map[string]any{"tag": deniedTag, "cardinality": "exactly_one"},
				},
			})
			result := Lint(payload)
			if result.Valid {
				t.Errorf("expected Lint to reject produces_tags with denied tag %q, but got Valid=true", deniedTag)
			}
			found := false
			for _, e := range result.Errors {
				if e.Code == "parse_error" || e.Code == "denied_tag" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected a parse_error or denied_tag error for tag %q, got: %v", deniedTag, result.Errors)
			}
		})
	}
}

// TestLint_DeniedTagPrefix verifies that reserved prefix tags are rejected.
func TestLint_DeniedTagPrefix(t *testing.T) {
	for _, deniedTag := range []string{"naming:foo", "naming:register", "campfire:admin", "campfire:system"} {
		t.Run(deniedTag, func(t *testing.T) {
			payload := mustJSON(map[string]any{
				"convention": "my-convention",
				"version":    "0.1",
				"operation":  "op",
				"signing":    "member_key",
				"produces_tags": []any{
					map[string]any{"tag": deniedTag, "cardinality": "exactly_one"},
				},
			})
			result := Lint(payload)
			if result.Valid {
				t.Errorf("expected Lint to reject produces_tags with denied-prefix tag %q, but got Valid=true", deniedTag)
			}
		})
	}
}

// TestLint_ConventionExtension_CannotBypassDeniedTags is the regression test for icq finding 4.
//
// A declaration claiming convention: "convention-extension" must not be able to inject
// denied/reserved tags via Lint. In Parse, convention-extension has a narrow exemption
// for convention:operation and convention:revoke — but Lint uses synthetic keys
// (senderKey == campfireKey), so Parse's exemption passes without real campfire-key
// authorisation. Lint must re-enforce the full denylist unconditionally.
func TestLint_ConventionExtension_CannotBypassDeniedTags(t *testing.T) {
	reservedTags := []string{
		"convention:operation", // the specific tags convention-extension is exempted for in Parse
		"convention:revoke",
		"convention:schema",
		"future",
		"fulfills",
		"naming:foo",
		"campfire:system",
	}

	for _, reservedTag := range reservedTags {
		t.Run(reservedTag, func(t *testing.T) {
			payload := mustJSON(map[string]any{
				"convention":  InfrastructureConvention, // "convention-extension"
				"version":     "0.1",
				"operation":   "malicious-op",
				"signing":     "campfire_key", // passes Parse check with synthetic keys
				"produces_tags": []any{
					map[string]any{"tag": reservedTag, "cardinality": "exactly_one"},
				},
			})
			result := Lint(payload)
			if result.Valid {
				t.Errorf(
					"security bypass: Lint accepted convention-extension declaration with reserved tag %q — "+
						"convention-extension must not bypass the denied-tag denylist in Lint",
					reservedTag,
				)
			}
			// Confirm that the error is specifically a denied_tag finding.
			foundDenied := false
			for _, e := range result.Errors {
				if e.Code == "denied_tag" {
					foundDenied = true
					break
				}
			}
			if !foundDenied {
				// parse_error is also acceptable (e.g. for tags blocked before our check),
				// but denied_tag is preferred for the Lint-layer catch.
				hasParseErr := false
				for _, e := range result.Errors {
					if e.Code == "parse_error" {
						hasParseErr = true
						break
					}
				}
				if !hasParseErr {
					t.Errorf("expected a denied_tag or parse_error finding for reserved tag %q, got: %v", reservedTag, result.Errors)
				}
			}
		})
	}
}

// TestLint_ConventionExtension_LegitimateOperationsBlockedByLint verifies that even
// legitimate convention-extension ops (like the built-in promote/supersede/revoke
// declarations from seed.go) fail Lint — because Lint is a user-submission validator,
// not a bootstrap path. Infrastructure operations go through Parse directly with real keys.
func TestLint_ConventionExtension_LegitimateOperationsBlockedByLint(t *testing.T) {
	// The PromoteDeclaration produces convention:operation — that's reserved.
	// Lint should reject it even though Parse allows it for convention-extension + real campfire key.
	payload := mustJSON(map[string]any{
		"convention":  InfrastructureConvention,
		"version":     infrastructureVersion,
		"operation":   "promote",
		"description": "Publish a validated convention declaration to a convention registry campfire",
		"signing":     "campfire_key",
		"produces_tags": []any{
			map[string]any{"tag": ConventionOperationTag, "cardinality": "exactly_one"},
		},
		"args": []any{
			map[string]any{"name": "file", "type": "string", "required": true},
			map[string]any{"name": "registry", "type": "campfire", "required": true},
		},
	})
	result := Lint(payload)
	// This MUST be invalid — Lint should not bless convention-extension declarations
	// that produce reserved tags, even if they look like legitimate infrastructure ops.
	if result.Valid {
		t.Error("security bypass: Lint accepted convention-extension/promote declaration producing convention:operation — " +
			"convention-extension tag exemption must not apply in Lint path")
	}
}

// TestLint_InvalidPayload verifies that unparseable payloads return a parse_error.
func TestLint_InvalidPayload(t *testing.T) {
	result := Lint([]byte(`{not valid json`))
	if result.Valid {
		t.Error("expected Valid=false for invalid JSON")
	}
	if len(result.Errors) == 0 {
		t.Error("expected at least one error")
	}
	if result.Errors[0].Code != "parse_error" {
		t.Errorf("expected parse_error, got %q", result.Errors[0].Code)
	}
}

// TestLint_WarningsDoNotInvalidate verifies that warnings alone don't make Valid=false.
func TestLint_WarningsDoNotInvalidate(t *testing.T) {
	// Rate limit above ceiling triggers a conformance warning.
	payload := mustJSON(map[string]any{
		"convention": "my-convention",
		"version":    "0.1",
		"operation":  "op",
		"signing":    "member_key",
		"rate_limit": map[string]any{"max": 200, "per": "sender", "window": "2m"},
	})
	result := Lint(payload)
	if !result.Valid {
		t.Errorf("expected Valid=true (warnings only), got errors: %v", result.Errors)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected at least one warning for rate_limit.max > 100")
	}
}

// TestLint_EnumTagMismatchWarning exercises the lintArgToTagMapping warning path.
func TestLint_EnumTagMismatchWarning(t *testing.T) {
	// coordination enum values don't have the "social:" prefix, but produces_tags
	// has "social:*" with matching values — triggers enum_tag_mismatch warning.
	payload := mustJSON(map[string]any{
		"convention": "my-convention",
		"version":    "0.1",
		"operation":  "op",
		"signing":    "member_key",
		"produces_tags": []any{
			map[string]any{
				"tag":         "social:*",
				"cardinality": "zero_to_many",
				"values":      []string{"social:need", "social:have"},
			},
		},
		"args": []any{
			map[string]any{
				"name":   "coordination",
				"type":   "enum",
				"values": []string{"need", "have"}, // missing "social:" prefix
			},
		},
	})
	result := Lint(payload)
	// Valid is true (warnings only), but should have an enum_tag_mismatch warning.
	if !result.Valid {
		t.Errorf("expected Valid=true, got errors: %v", result.Errors)
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Code, "enum_tag_mismatch") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected enum_tag_mismatch warning, got warnings: %v", result.Warnings)
	}
}

// TestLint_UnmappableTag verifies that a glob tag with no matching arg produces an
// unmappable_tag error (lintArgToTagMapping error path).
func TestLint_UnmappableTag(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "my-convention",
		"version":    "0.1",
		"operation":  "op",
		"signing":    "member_key",
		"produces_tags": []any{
			map[string]any{
				"tag":         "widget:*",
				"cardinality": "zero_to_many",
			},
		},
		// No args declared at all — no arg can satisfy "widget:*"
	})
	result := Lint(payload)
	if result.Valid {
		t.Errorf("expected Valid=false for unmappable glob tag, got errors: %v", result.Errors)
	}
	found := false
	for _, e := range result.Errors {
		if e.Code == "unmappable_tag" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected unmappable_tag error, got: %v", result.Errors)
	}
}

// TestLint_ExactEnumCandidateSatisfiesGlob verifies that an enum arg whose values
// already include the tag prefix satisfies the glob (no warning or error).
func TestLint_ExactEnumCandidateSatisfiesGlob(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "my-convention",
		"version":    "0.1",
		"operation":  "op",
		"signing":    "member_key",
		"produces_tags": []any{
			map[string]any{
				"tag":         "social:*",
				"cardinality": "zero_to_many",
				"values":      []string{"social:need", "social:have"},
			},
		},
		"args": []any{
			map[string]any{
				"name":   "coord",
				"type":   "enum",
				"values": []string{"social:need", "social:have"}, // correct prefix
			},
		},
	})
	result := Lint(payload)
	if !result.Valid {
		t.Errorf("expected Valid=true when enum values have correct prefix, got errors: %v", result.Errors)
	}
	// Should have no enum_tag_mismatch warnings
	for _, w := range result.Warnings {
		if w.Code == "enum_tag_mismatch" {
			t.Errorf("unexpected enum_tag_mismatch warning: %v", w)
		}
	}
}

// TestLint_NonEnumNameCandidateSatisfiesGlob verifies that a non-enum arg whose
// name matches the prefix base satisfies the glob.
func TestLint_NonEnumNameCandidateSatisfiesGlob(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "my-convention",
		"version":    "0.1",
		"operation":  "op",
		"signing":    "member_key",
		"produces_tags": []any{
			map[string]any{
				"tag":         "topic:*",
				"cardinality": "zero_to_many",
			},
		},
		"args": []any{
			map[string]any{
				"name":     "topics",
				"type":     "string",
				"repeated": true,
			},
		},
	})
	result := Lint(payload)
	if !result.Valid {
		t.Errorf("expected Valid=true when non-enum arg name matches prefix, got errors: %v", result.Errors)
	}
}

// TestLint_StaticTagOnly verifies that a static (non-glob) tag in produces_tags
// is passed through lintArgToTagMapping without error.
func TestLint_StaticTagOnly(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "my-convention",
		"version":    "0.1",
		"operation":  "op",
		"signing":    "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "status:ok", "cardinality": "exactly_one"},
		},
	})
	result := Lint(payload)
	if !result.Valid {
		t.Errorf("expected Valid=true for static tag, got errors: %v", result.Errors)
	}
}
