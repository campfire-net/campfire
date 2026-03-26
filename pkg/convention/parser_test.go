package convention

import (
	"encoding/json"
	"testing"
	"time"
)

const testSenderKey = "abc123"
const testCampfireKey = "campfire456"

func tags(tt ...string) []string { return tt }

// socialPostPayload is §16.1 test vector.
var socialPostPayload = []byte(`{
    "convention": "social-post-format",
    "version": "0.3",
    "operation": "post",
    "description": "Publish a social post",
    "produces_tags": [
      {"tag": "social:post", "cardinality": "exactly_one"},
      {"tag": "content:*", "cardinality": "at_most_one",
       "values": ["content:text/plain", "content:text/markdown", "content:application/json"]},
      {"tag": "topic:*", "cardinality": "zero_to_many", "max": 10, "pattern": "[a-z0-9-]{1,64}"},
      {"tag": "social:*", "cardinality": "zero_to_many",
       "values": ["social:need", "social:have", "social:offer", "social:request", "social:question", "social:answer"]}
    ],
    "args": [
      {"name": "text", "type": "string", "required": true, "max_length": 65536,
       "description": "Post content"},
      {"name": "content_type", "type": "enum",
       "values": ["text/plain", "text/markdown", "application/json"], "default": "text/plain"},
      {"name": "topics", "type": "string", "repeated": true, "max_count": 10,
       "pattern": "[a-z0-9-]{1,64}", "description": "Topic tags (without 'topic:' prefix)"},
      {"name": "coordination", "type": "enum",
       "values": ["need", "have", "offer", "request", "question", "answer"], "repeated": true,
       "description": "Coordination signal tags"}
    ],
    "antecedents": "none",
    "payload_required": true,
    "signing": "member_key"
}`)

func TestParse_ValidSocialPost(t *testing.T) {
	decl, result, err := Parse(tags(ConventionOperationTag), socialPostPayload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("expected valid, got warnings: %v", result.Warnings)
	}
	if decl.Convention != "social-post-format" {
		t.Errorf("convention = %q, want %q", decl.Convention, "social-post-format")
	}
	if decl.Operation != "post" {
		t.Errorf("operation = %q, want %q", decl.Operation, "post")
	}
	if decl.Signing != "member_key" {
		t.Errorf("signing = %q, want %q", decl.Signing, "member_key")
	}
	if len(decl.Args) != 4 {
		t.Errorf("args count = %d, want 4", len(decl.Args))
	}
	if len(decl.ProducesTags) != 4 {
		t.Errorf("produces_tags count = %d, want 4", len(decl.ProducesTags))
	}
	if decl.SignerType != SignerMemberKey {
		t.Errorf("signer type = %q, want %q", decl.SignerType, SignerMemberKey)
	}
}

func TestParse_ValidVote(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "upvote",
		"description": "Upvote a post",
		"antecedents": "exactly_one(target)",
		"produces_tags": []any{
			map[string]any{"tag": "social:upvote", "cardinality": "exactly_one"},
		},
		"signing": "member_key",
	})
	decl, result, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("expected valid, got warnings: %v", result.Warnings)
	}
	if decl.Antecedents != "exactly_one(target)" {
		t.Errorf("antecedents = %q, want %q", decl.Antecedents, "exactly_one(target)")
	}
}

func TestParse_ValidMultiStepWorkflow(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention":  "profile-management",
		"version":     "0.1",
		"operation":   "update-profile",
		"description": "Update user profile with lookup",
		"signing":     "member_key",
		"steps": []any{
			map[string]any{
				"action":         "query",
				"description":    "Look up current profile",
				"result_binding": "current",
			},
			map[string]any{
				"action":      "send",
				"description": "Send updated profile",
				"tags":        []any{"profile:update"},
				"future_payload": map[string]any{
					"previous": "$current.message_id",
				},
			},
		},
	})
	decl, result, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("expected valid, got warnings: %v", result.Warnings)
	}
	if len(decl.Steps) != 2 {
		t.Errorf("steps count = %d, want 2", len(decl.Steps))
	}
}

func TestParse_ValidCampfireKeyOp(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention":  "beacon-registry",
		"version":     "0.1",
		"operation":   "register",
		"description": "Register a beacon",
		"signing":     "campfire_key",
		"produces_tags": []any{
			map[string]any{"tag": "beacon:registered", "cardinality": "exactly_one"},
		},
	})
	// senderKey == campfireKey -> authorized
	key := "same-key-hex"
	decl, result, err := Parse(tags(ConventionOperationTag), payload, key, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("expected valid, got warnings: %v", result.Warnings)
	}
	if !result.CampfireKeyAuthorized {
		t.Error("expected CampfireKeyAuthorized = true")
	}
	if decl.SignerType != SignerCampfireKey {
		t.Errorf("signer type = %q, want %q", decl.SignerType, SignerCampfireKey)
	}
}

func TestParse_MissingConventionOperationTag(t *testing.T) {
	_, _, err := Parse(tags("other:tag"), socialPostPayload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for missing convention:operation tag")
	}
}

func TestParse_DuplicateTag(t *testing.T) {
	_, _, err := Parse(tags(ConventionOperationTag, ConventionOperationTag), socialPostPayload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for duplicate convention:operation tag")
	}
}

func TestParse_InvalidJSON(t *testing.T) {
	_, _, err := Parse(tags(ConventionOperationTag), []byte(`{not json`), testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParse_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{"missing convention", map[string]any{"version": "1", "operation": "op", "signing": "member_key"}},
		{"missing version", map[string]any{"convention": "c", "operation": "op", "signing": "member_key"}},
		{"missing operation", map[string]any{"convention": "c", "version": "1", "signing": "member_key"}},
		{"missing signing", map[string]any{"convention": "c", "version": "1", "operation": "op"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse(tags(ConventionOperationTag), mustJSON(tt.payload), testSenderKey, testCampfireKey)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParse_UnknownArgType(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"args": []any{map[string]any{"name": "x", "type": "banana"}},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for unknown arg type")
	}
}

func TestParse_InvalidCardinality(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{map[string]any{"tag": "x:y", "cardinality": "many_to_many"}},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for invalid cardinality")
	}
}

func TestParse_InvalidAntecedentRule(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"antecedents": "exactly_two(target)",
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for invalid antecedent rule")
	}
}

func TestParse_UnsafePattern_TooLong(t *testing.T) {
	longPattern := "[a-z]" + string(make([]byte, 65)) // >64 chars
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"args": []any{map[string]any{"name": "x", "type": "string", "pattern": longPattern}},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for pattern too long")
	}
}

func TestParse_UnsafePattern_NestedQuantifier(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"args": []any{map[string]any{"name": "x", "type": "string", "pattern": "(a+)+"}},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for nested quantifier")
	}
}

func TestParse_UnsafePattern_TooManyAlternations(t *testing.T) {
	// 12 branches in one group
	pattern := "(a|b|c|d|e|f|g|h|i|j|k|l)"
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"args": []any{map[string]any{"name": "x", "type": "string", "pattern": pattern}},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for too many alternation branches")
	}
}

func TestParse_CampfireKeyNotSigned(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "campfire_key",
	})
	decl, result, err := Parse(tags(ConventionOperationTag), payload, "wrong-key", "campfire-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Valid {
		t.Error("expected Valid = false")
	}
	if result.CampfireKeyAuthorized {
		t.Error("expected CampfireKeyAuthorized = false")
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warnings")
	}
	// SignerType must be MemberKey when the sender is not the campfire key.
	// Granting SignerCampfireKey here would allow ResolveAuthority to bypass the gate
	// and return AuthorityOperational for an unauthorized declaration.
	if decl.SignerType != SignerMemberKey {
		t.Errorf("SignerType = %q, want %q — unauthorized campfire_key claim must not escalate signer type", decl.SignerType, SignerMemberKey)
	}
}

func TestParse_CampfireKeyWorkflowProhibited(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "campfire_key",
		"steps": []any{
			map[string]any{"action": "query"},
			map[string]any{"action": "send"},
		},
	})
	key := "same-key"
	_, _, err := Parse(tags(ConventionOperationTag), payload, key, key)
	if err == nil {
		t.Fatal("expected error for campfire_key operation with steps")
	}
}

func TestParse_StepsForwardReference(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"steps": []any{
			map[string]any{
				"action": "send",
				"future_payload": map[string]any{
					"ref": "$later.id",
				},
			},
			map[string]any{
				"action":         "query",
				"result_binding": "later",
			},
		},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for forward reference in steps")
	}
}

func TestParse_StepsUnboundVariable(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"steps": []any{
			map[string]any{
				"action": "send",
				"future_payload": map[string]any{
					"ref": "$nonexistent.id",
				},
			},
		},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for unbound variable reference")
	}
}

func TestParse_DeniedTag(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "convention:operation", "cardinality": "exactly_one"},
		},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for denied tag")
	}
}

func TestParse_DeniedTagPrefix(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "naming:foo", "cardinality": "exactly_one"},
		},
	})
	_, _, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for naming: prefixed tag")
	}
}

func TestParse_RateLimitCeiling(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"rate_limit": map[string]any{"max": 200, "per": "sender", "window": "30s"},
	})
	decl, result, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decl.RateLimit.Max != 100 {
		t.Errorf("rate_limit.max = %d, want 100", decl.RateLimit.Max)
	}
	if decl.RateLimit.Window != "1m" {
		t.Errorf("rate_limit.window = %q, want %q", decl.RateLimit.Window, "1m")
	}
	if len(result.Warnings) < 2 {
		t.Errorf("expected at least 2 warnings, got %d: %v", len(result.Warnings), result.Warnings)
	}
}

func TestParse_RateLimitInvalidPer(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"rate_limit": map[string]any{"max": 10, "per": "global", "window": "5m"},
	})
	_, result, err := Parse(tags(ConventionOperationTag), payload, testSenderKey, testCampfireKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Valid {
		t.Error("expected Valid = false for invalid per value")
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
		err   bool
	}{
		{"30s", 30 * time.Second, false},
		{"1m", 1 * time.Minute, false},
		{"24h", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"", 0, true},
		{"abc", 0, true},
		{"10x", 0, true},
		{"123", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseDuration(tt.input)
			if tt.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
