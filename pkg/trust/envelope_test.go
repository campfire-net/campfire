package trust

import (
	"encoding/json"
	"testing"
)

// TestBuildEnvelope_Verified checks that TrustVerified propagates to the envelope.
func TestBuildEnvelope_Verified(t *testing.T) {
	env := BuildEnvelope("campfire-abc", TrustVerified, "hello")
	if env.RuntimeComputed.TrustChain != TrustVerified {
		t.Errorf("expected TrustVerified, got %q", env.RuntimeComputed.TrustChain)
	}
}

// TestBuildEnvelope_Unverified checks that TrustUnverified propagates.
func TestBuildEnvelope_Unverified(t *testing.T) {
	env := BuildEnvelope("campfire-abc", TrustUnverified, "hello")
	if env.RuntimeComputed.TrustChain != TrustUnverified {
		t.Errorf("expected TrustUnverified, got %q", env.RuntimeComputed.TrustChain)
	}
}

// TestBuildEnvelope_CrossRoot checks that TrustCrossRoot propagates.
func TestBuildEnvelope_CrossRoot(t *testing.T) {
	env := BuildEnvelope("campfire-abc", TrustCrossRoot, "hello")
	if env.RuntimeComputed.TrustChain != TrustCrossRoot {
		t.Errorf("expected TrustCrossRoot, got %q", env.RuntimeComputed.TrustChain)
	}
}

// TestBuildEnvelope_Relayed checks that TrustRelayed propagates.
func TestBuildEnvelope_Relayed(t *testing.T) {
	env := BuildEnvelope("campfire-abc", TrustRelayed, "hello")
	if env.RuntimeComputed.TrustChain != TrustRelayed {
		t.Errorf("expected TrustRelayed, got %q", env.RuntimeComputed.TrustChain)
	}
}

// TestBuildEnvelope_Structure verifies the full JSON shape matches §6.1.
func TestBuildEnvelope_Structure(t *testing.T) {
	env := BuildEnvelope("campfire-xyz", TrustVerified, "test content",
		WithCampfireName("my-campfire"),
		WithDirectoryRegistration(true),
		WithMemberCount(5),
		WithCreatedAge("2d"),
	)

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	for _, key := range []string{"verified", "runtime_computed", "campfire_asserted", "tainted"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing top-level key %q in envelope JSON", key)
		}
	}

	// verified.campfire_id
	var verified VerifiedFields
	if err := json.Unmarshal(raw["verified"], &verified); err != nil {
		t.Fatalf("unmarshal verified failed: %v", err)
	}
	if verified.CampfireID != "campfire-xyz" {
		t.Errorf("expected campfire_id=campfire-xyz, got %q", verified.CampfireID)
	}

	// runtime_computed fields
	var rc RuntimeComputedFields
	if err := json.Unmarshal(raw["runtime_computed"], &rc); err != nil {
		t.Fatalf("unmarshal runtime_computed failed: %v", err)
	}
	if rc.CampfireName != "my-campfire" {
		t.Errorf("expected campfire_name=my-campfire, got %q", rc.CampfireName)
	}
	if !rc.RegisteredInDirectory {
		t.Error("expected registered_in_directory=true")
	}
	if rc.TrustChain != TrustVerified {
		t.Errorf("expected trust_chain=verified, got %q", rc.TrustChain)
	}

	// campfire_asserted fields
	var ca CampfireAssertedFields
	if err := json.Unmarshal(raw["campfire_asserted"], &ca); err != nil {
		t.Fatalf("unmarshal campfire_asserted failed: %v", err)
	}
	if ca.MemberCount != 5 {
		t.Errorf("expected member_count=5, got %d", ca.MemberCount)
	}
	if ca.CreatedAge != "2d" {
		t.Errorf("expected created_age=2d, got %q", ca.CreatedAge)
	}

	// tainted fields
	var tainted TaintedFields
	if err := json.Unmarshal(raw["tainted"], &tainted); err != nil {
		t.Fatalf("unmarshal tainted failed: %v", err)
	}
	if tainted.ContentClassification != "tainted" {
		t.Errorf("expected content_classification=tainted, got %q", tainted.ContentClassification)
	}
}

// TestBuildEnvelope_WithOptions verifies option application.
func TestBuildEnvelope_WithOptions(t *testing.T) {
	env := BuildEnvelope("cf-1", TrustCrossRoot, nil,
		WithCampfireName("test-fire"),
		WithDirectoryRegistration(false),
		WithMemberCount(42),
		WithCreatedAge("7d"),
	)

	if env.RuntimeComputed.CampfireName != "test-fire" {
		t.Errorf("expected campfire_name=test-fire, got %q", env.RuntimeComputed.CampfireName)
	}
	if env.RuntimeComputed.RegisteredInDirectory {
		t.Error("expected registered_in_directory=false")
	}
	if env.CampfireAsserted.MemberCount != 42 {
		t.Errorf("expected member_count=42, got %d", env.CampfireAsserted.MemberCount)
	}
	if env.CampfireAsserted.CreatedAge != "7d" {
		t.Errorf("expected created_age=7d, got %q", env.CampfireAsserted.CreatedAge)
	}
}

// TestSanitize_Truncation checks that strings over maxLen are truncated.
func TestSanitize_Truncation(t *testing.T) {
	long := make([]byte, 2000)
	for i := range long {
		long[i] = 'a'
	}
	s := string(long)

	result, steps := Sanitize(s, 1024)
	if len(result) != 1024 {
		t.Errorf("expected length 1024, got %d", len(result))
	}
	if !containsStep(steps, "truncated") {
		t.Errorf("expected 'truncated' in steps, got %v", steps)
	}
}

// TestSanitize_ControlChars checks stripping of control chars while preserving \n and \t.
func TestSanitize_ControlChars(t *testing.T) {
	s := "hello\x01\x02\x1Fworld\nand\ttabs"
	result, steps := Sanitize(s, 1024)

	if !containsStep(steps, "control_chars_stripped") {
		t.Errorf("expected 'control_chars_stripped' in steps, got %v", steps)
	}
	// \x01, \x02, \x1F should be removed
	for _, ch := range []rune{'\x01', '\x02', '\x1F'} {
		for _, r := range result {
			if r == ch {
				t.Errorf("control char %q should have been stripped", ch)
			}
		}
	}
	// \n and \t must be preserved
	found := false
	for _, r := range result {
		if r == '\n' {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected \\n to be preserved")
	}
	found = false
	for _, r := range result {
		if r == '\t' {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected \\t to be preserved")
	}
}

// TestSanitize_NullBytes checks removal of null bytes.
func TestSanitize_NullBytes(t *testing.T) {
	s := "foo\x00bar\x00baz"
	result, steps := Sanitize(s, 1024)

	if !containsStep(steps, "null_bytes_removed") {
		t.Errorf("expected 'null_bytes_removed' in steps, got %v", steps)
	}
	for _, r := range result {
		if r == '\x00' {
			t.Error("null byte should have been removed")
		}
	}
}

// TestSanitize_Clean verifies that a clean string produces no sanitization steps.
func TestSanitize_Clean(t *testing.T) {
	s := "hello world\nwith newline\tand tab"
	result, steps := Sanitize(s, 1024)

	if result != s {
		t.Errorf("expected unchanged string, got %q", result)
	}
	if len(steps) != 0 {
		t.Errorf("expected no sanitization steps for clean string, got %v", steps)
	}
}

// TestSanitizeContent_Map checks that string values in a map are sanitized.
func TestSanitizeContent_Map(t *testing.T) {
	input := map[string]any{
		"key": "hello\x01world",
	}
	out, steps := SanitizeContent(input, 1024)

	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", out)
	}
	val, ok := m["key"].(string)
	if !ok {
		t.Fatalf("expected string value, got %T", m["key"])
	}
	for _, r := range val {
		if r == '\x01' {
			t.Error("control char should have been stripped from map value")
		}
	}
	if !containsStep(steps, "control_chars_stripped") {
		t.Errorf("expected 'control_chars_stripped' in steps, got %v", steps)
	}
}

// TestSanitizeContent_EnvelopeMimicry checks that keys matching envelope structure are prefixed.
func TestSanitizeContent_EnvelopeMimicry(t *testing.T) {
	input := map[string]any{
		"verified":         "fake-verified",
		"runtime_computed": "fake-rc",
		"safe_key":         "safe-value",
	}
	out, steps := SanitizeContent(input, 1024)

	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", out)
	}
	if _, exists := m["verified"]; exists {
		t.Error("key 'verified' should have been renamed")
	}
	if _, exists := m["content_verified"]; !exists {
		t.Error("expected key 'content_verified' after mimicry escape")
	}
	if _, exists := m["content_runtime_computed"]; !exists {
		t.Error("expected key 'content_runtime_computed' after mimicry escape")
	}
	if _, exists := m["safe_key"]; !exists {
		t.Error("expected 'safe_key' to remain unchanged")
	}
	if !containsStep(steps, "envelope_mimicry_escaped") {
		t.Errorf("expected 'envelope_mimicry_escaped' in steps, got %v", steps)
	}
}

// TestSanitizeContent_Nested checks recursive sanitization of nested maps.
func TestSanitizeContent_Nested(t *testing.T) {
	input := map[string]any{
		"outer": map[string]any{
			"inner": "hello\x02world",
		},
	}
	out, steps := SanitizeContent(input, 1024)

	m := out.(map[string]any)
	inner := m["outer"].(map[string]any)
	val := inner["inner"].(string)

	for _, r := range val {
		if r == '\x02' {
			t.Error("control char should have been stripped from nested value")
		}
	}
	if !containsStep(steps, "control_chars_stripped") {
		t.Errorf("expected 'control_chars_stripped' in steps, got %v", steps)
	}
}

// TestBuildEnvelope_ContentSanitized checks that content with control chars is sanitized in the envelope.
func TestBuildEnvelope_ContentSanitized(t *testing.T) {
	dirty := "hello\x01\x02world"
	env := BuildEnvelope("cf-dirty", TrustVerified, dirty)

	sanitized, ok := env.Tainted.Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", env.Tainted.Content)
	}
	for _, r := range sanitized {
		if r == '\x01' || r == '\x02' {
			t.Error("control chars should have been stripped from envelope content")
		}
	}
	if !containsStep(env.RuntimeComputed.SanitizationApplied, "control_chars_stripped") {
		t.Errorf("expected 'control_chars_stripped' in sanitization_applied, got %v", env.RuntimeComputed.SanitizationApplied)
	}
}

// containsStep is a helper to check if a step string is in the steps slice.
func containsStep(steps []string, step string) bool {
	for _, s := range steps {
		if s == step {
			return true
		}
	}
	return false
}
