package trust

import (
	"encoding/json"
	"testing"
)

// TestBuildEnvelope_Adopted checks that TrustAdopted propagates to the envelope.
func TestBuildEnvelope_Adopted(t *testing.T) {
	env := BuildEnvelope("campfire-abc", TrustAdopted, "hello")
	if env.RuntimeComputed.TrustStatus != TrustAdopted {
		t.Errorf("expected TrustAdopted, got %q", env.RuntimeComputed.TrustStatus)
	}
}

// TestBuildEnvelope_Unknown checks that TrustUnknown propagates.
func TestBuildEnvelope_Unknown(t *testing.T) {
	env := BuildEnvelope("campfire-abc", TrustUnknown, "hello")
	if env.RuntimeComputed.TrustStatus != TrustUnknown {
		t.Errorf("expected TrustUnknown, got %q", env.RuntimeComputed.TrustStatus)
	}
}

// TestBuildEnvelope_Compatible checks that TrustCompatible propagates.
func TestBuildEnvelope_Compatible(t *testing.T) {
	env := BuildEnvelope("campfire-abc", TrustCompatible, "hello")
	if env.RuntimeComputed.TrustStatus != TrustCompatible {
		t.Errorf("expected TrustCompatible, got %q", env.RuntimeComputed.TrustStatus)
	}
}

// TestBuildEnvelope_Divergent checks that TrustDivergent propagates.
func TestBuildEnvelope_Divergent(t *testing.T) {
	env := BuildEnvelope("campfire-abc", TrustDivergent, "hello")
	if env.RuntimeComputed.TrustStatus != TrustDivergent {
		t.Errorf("expected TrustDivergent, got %q", env.RuntimeComputed.TrustStatus)
	}
}

// TestBuildEnvelope_Structure verifies the full JSON shape matches Trust v0.2 §6.1.
func TestBuildEnvelope_Structure(t *testing.T) {
	env := BuildEnvelope("campfire-xyz", TrustAdopted, "test content",
		WithCampfireName("my-campfire"),
		WithDirectoryRegistration(true),
		WithMemberCount(5),
		WithCreatedAge("2d"),
		WithFingerprintMatch(true),
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

	// runtime_computed fields — v0.2 uses trust_status + fingerprint_match
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
	if rc.TrustStatus != TrustAdopted {
		t.Errorf("expected trust_status=adopted, got %q", rc.TrustStatus)
	}
	if !rc.FingerprintMatch {
		t.Error("expected fingerprint_match=true")
	}

	// Verify JSON has trust_status and fingerprint_match (not trust_chain)
	var rcRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw["runtime_computed"], &rcRaw); err != nil {
		t.Fatalf("unmarshal runtime_computed raw failed: %v", err)
	}
	if _, ok := rcRaw["trust_status"]; !ok {
		t.Error("expected 'trust_status' in runtime_computed JSON (v0.2)")
	}
	if _, ok := rcRaw["fingerprint_match"]; !ok {
		t.Error("expected 'fingerprint_match' in runtime_computed JSON (v0.2)")
	}
	if _, ok := rcRaw["trust_chain"]; ok {
		t.Error("'trust_chain' must not appear in runtime_computed JSON (v0.2 — removed)")
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
	env := BuildEnvelope("cf-1", TrustCompatible, nil,
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
	env := BuildEnvelope("cf-dirty", TrustAdopted, dirty)

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

// TestBuildEnvelope_JoinProtocol_InviteOnly verifies that WithJoinProtocol sets
// join_protocol in campfire_asserted.
func TestBuildEnvelope_JoinProtocol_InviteOnly(t *testing.T) {
	env := BuildEnvelope("campfire-abc", TrustAdopted, "content",
		WithJoinProtocol("invite-only"),
	)
	if env.CampfireAsserted.JoinProtocol != "invite-only" {
		t.Errorf("expected join_protocol=invite-only, got %q", env.CampfireAsserted.JoinProtocol)
	}

	// Verify it appears in JSON.
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	var ca CampfireAssertedFields
	if err := json.Unmarshal(raw["campfire_asserted"], &ca); err != nil {
		t.Fatalf("unmarshal campfire_asserted: %v", err)
	}
	if ca.JoinProtocol != "invite-only" {
		t.Errorf("campfire_asserted.join_protocol = %q, want invite-only", ca.JoinProtocol)
	}
}

// TestBuildEnvelope_JoinProtocol_Open verifies "open" is passed through.
func TestBuildEnvelope_JoinProtocol_Open(t *testing.T) {
	env := BuildEnvelope("campfire-def", TrustUnknown, "content",
		WithJoinProtocol("open"),
	)
	if env.CampfireAsserted.JoinProtocol != "open" {
		t.Errorf("expected join_protocol=open, got %q", env.CampfireAsserted.JoinProtocol)
	}
}

// TestBuildEnvelope_JoinProtocol_Absent verifies that when WithJoinProtocol is
// not called, join_protocol is absent from JSON (omitempty).
func TestBuildEnvelope_JoinProtocol_Absent(t *testing.T) {
	env := BuildEnvelope("campfire-ghi", TrustUnknown, "content")
	if env.CampfireAsserted.JoinProtocol != "" {
		t.Errorf("expected empty join_protocol, got %q", env.CampfireAsserted.JoinProtocol)
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	var caRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw["campfire_asserted"], &caRaw); err != nil {
		t.Fatalf("unmarshal campfire_asserted: %v", err)
	}
	if _, ok := caRaw["join_protocol"]; ok {
		t.Error("join_protocol should be absent from JSON when not set (omitempty)")
	}
}
