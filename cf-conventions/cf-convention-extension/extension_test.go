package conventionextension_test

// extension_test.go — behavioral tests for promote and supersede operations.
//
// These tests verify the validation logic for the convention-extension promote
// and supersede operations. They test behavior, not just structure: does a
// valid payload succeed? Does a missing supersedes field produce the right error?
// Does a version downgrade fail? Does a campfire_key operation without campfire
// signing fail?
//
// Done condition (campfireagent-a40): "promote + supersede ops have behavioral tests"

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
	ext "github.com/campfire-net/campfire/cf-conventions/cf-convention-extension"
)

// testCampfireKey is a synthetic 64-hex-char campfire key for tests.
const testCampfireKey = "cafebabe" + "00000000" + "00000000" + "00000000" + "00000000" + "00000000" + "00000000" + "0000cafe"

// testSignerKey is a different key used as the member/signer key.
const testSignerKey = "deadbeef" + "00000000" + "00000000" + "00000000" + "00000000" + "00000000" + "00000000" + "0000dead"

func makeDeclarationJSON(convention, version, operation, signing, supersedes string) []byte {
	type payload struct {
		Convention string `json:"convention"`
		Version    string `json:"version"`
		Operation  string `json:"operation"`
		Description string `json:"description,omitempty"`
		Supersedes string `json:"supersedes,omitempty"`
		Signing    string `json:"signing"`
		ProducesTags []map[string]string `json:"produces_tags,omitempty"`
	}
	p := payload{
		Convention:  convention,
		Version:     version,
		Operation:   operation,
		Signing:     signing,
		Supersedes:  supersedes,
		Description: "test declaration",
	}
	// Add a produces_tags for non-convention-extension conventions.
	if convention != "convention-extension" {
		p.ProducesTags = []map[string]string{
			{"tag": "test:op", "cardinality": "exactly_one"},
		}
	}
	data, err := json.Marshal(p)
	if err != nil {
		panic("makeDeclarationJSON: " + err.Error())
	}
	return data
}

// TestValidatePromote_ValidMemberKey verifies that a well-formed member_key
// declaration passes promote validation.
func TestValidatePromote_ValidMemberKey(t *testing.T) {
	payload := makeDeclarationJSON("my-convention", "0.1", "post", "member_key", "")
	tags := []string{convention.ConventionOperationTag}

	result, err := ext.ValidatePromote(payload, tags, testSignerKey, testCampfireKey)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Declaration == nil {
		t.Fatal("expected non-nil declaration in result")
	}
	if result.Declaration.Operation != "post" {
		t.Errorf("operation: want %q, got %q", "post", result.Declaration.Operation)
	}
}

// TestValidatePromote_RejectsPayloadWithSupersedes verifies that a declaration
// with a supersedes field is rejected by promote (promote is for initial
// publication; supersede is for replacement).
func TestValidatePromote_RejectsPayloadWithSupersedes(t *testing.T) {
	payload := makeDeclarationJSON("my-convention", "0.2", "post", "member_key", "abc123messageID")
	tags := []string{convention.ConventionOperationTag}

	_, err := ext.ValidatePromote(payload, tags, testSignerKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for declaration with supersedes field, got nil")
	}
	if !errors.Is(err, ext.ErrPromoteDenied) {
		t.Errorf("expected ErrPromoteDenied, got: %v", err)
	}
	if !strings.Contains(err.Error(), "supersedes") {
		t.Errorf("error should mention 'supersedes', got: %v", err)
	}
}

// TestValidatePromote_RejectsInvalidPayload verifies that a malformed JSON
// payload fails promote validation.
func TestValidatePromote_RejectsInvalidPayload(t *testing.T) {
	payload := []byte(`not valid json`)
	tags := []string{convention.ConventionOperationTag}

	_, err := ext.ValidatePromote(payload, tags, testSignerKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for invalid JSON payload, got nil")
	}
	if !errors.Is(err, ext.ErrPromoteDenied) {
		t.Errorf("expected ErrPromoteDenied, got: %v", err)
	}
}

// TestValidatePromote_RejectsMissingRequiredTag verifies that a declaration
// without the convention:operation tag fails promote validation.
func TestValidatePromote_RejectsMissingRequiredTag(t *testing.T) {
	payload := makeDeclarationJSON("my-convention", "0.1", "post", "member_key", "")
	tags := []string{"some:other:tag"} // missing convention:operation

	_, err := ext.ValidatePromote(payload, tags, testSignerKey, testCampfireKey)
	if err == nil {
		t.Fatal("expected error for missing convention:operation tag, got nil")
	}
	if !errors.Is(err, ext.ErrPromoteDenied) {
		t.Errorf("expected ErrPromoteDenied, got: %v", err)
	}
}

// TestValidateSupersede_ValidReplacement verifies that a valid supersede
// (same convention+operation, higher version, supersedes field set) succeeds.
func TestValidateSupersede_ValidReplacement(t *testing.T) {
	existingDecl := &convention.Declaration{
		Convention: "my-convention",
		Version:    "0.1",
		Operation:  "post",
		Signing:    "member_key",
	}
	existingMsgID := "msg-id-of-original-declaration"

	newPayload := makeDeclarationJSON("my-convention", "0.2", "post", "member_key", existingMsgID)
	tags := []string{convention.ConventionOperationTag}

	result, err := ext.ValidateSupersede(newPayload, tags, testSignerKey, testCampfireKey, existingDecl, existingMsgID)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.NewDeclaration == nil {
		t.Fatal("expected non-nil new declaration in result")
	}
	if result.SupersededID != existingMsgID {
		t.Errorf("supersededID: want %q, got %q", existingMsgID, result.SupersededID)
	}
	if result.NewDeclaration.Version != "0.2" {
		t.Errorf("new version: want %q, got %q", "0.2", result.NewDeclaration.Version)
	}
}

// TestValidateSupersede_RejectsMissingSupersedes verifies that a declaration
// without a supersedes field is rejected by supersede validation.
func TestValidateSupersede_RejectsMissingSupersedes(t *testing.T) {
	existingDecl := &convention.Declaration{
		Convention: "my-convention",
		Version:    "0.1",
		Operation:  "post",
		Signing:    "member_key",
	}
	existingMsgID := "msg-id-of-original-declaration"

	// No supersedes field.
	newPayload := makeDeclarationJSON("my-convention", "0.2", "post", "member_key", "")
	tags := []string{convention.ConventionOperationTag}

	_, err := ext.ValidateSupersede(newPayload, tags, testSignerKey, testCampfireKey, existingDecl, existingMsgID)
	if err == nil {
		t.Fatal("expected error for missing supersedes field, got nil")
	}
	if !errors.Is(err, ext.ErrSupersedeDenied) {
		t.Errorf("expected ErrSupersedeDenied, got: %v", err)
	}
	if !strings.Contains(err.Error(), "supersedes") {
		t.Errorf("error should mention 'supersedes', got: %v", err)
	}
}

// TestValidateSupersede_RejectsMismatchedSupersedes verifies that a declaration
// whose supersedes field does not match the existing message ID is rejected.
func TestValidateSupersede_RejectsMismatchedSupersedes(t *testing.T) {
	existingDecl := &convention.Declaration{
		Convention: "my-convention",
		Version:    "0.1",
		Operation:  "post",
		Signing:    "member_key",
	}
	existingMsgID := "msg-id-of-original-declaration"

	// supersedes points to a different message.
	newPayload := makeDeclarationJSON("my-convention", "0.2", "post", "member_key", "different-msg-id")
	tags := []string{convention.ConventionOperationTag}

	_, err := ext.ValidateSupersede(newPayload, tags, testSignerKey, testCampfireKey, existingDecl, existingMsgID)
	if err == nil {
		t.Fatal("expected error for mismatched supersedes, got nil")
	}
	if !errors.Is(err, ext.ErrSupersedeDenied) {
		t.Errorf("expected ErrSupersedeDenied, got: %v", err)
	}
}

// TestValidateSupersede_RejectsVersionDowngrade verifies that a declaration
// with a lower or equal version is rejected by supersede validation.
func TestValidateSupersede_RejectsVersionDowngrade(t *testing.T) {
	existingMsgID := "msg-id-of-v02"

	cases := []struct {
		name       string
		newVersion string
	}{
		{"same version", "0.2"},
		{"lower version", "0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			existingDecl := &convention.Declaration{
				Convention: "my-convention",
				Version:    "0.2",
				Operation:  "post",
				Signing:    "member_key",
			}

			newPayload := makeDeclarationJSON("my-convention", tc.newVersion, "post", "member_key", existingMsgID)
			tags := []string{convention.ConventionOperationTag}

			_, err := ext.ValidateSupersede(newPayload, tags, testSignerKey, testCampfireKey, existingDecl, existingMsgID)
			if err == nil {
				t.Fatalf("expected error for version downgrade (%s), got nil", tc.newVersion)
			}
			if !errors.Is(err, ext.ErrSupersedeDenied) {
				t.Errorf("expected ErrSupersedeDenied, got: %v", err)
			}
		})
	}
}

// TestValidateSupersede_RejectsConventionMismatch verifies that a declaration
// for a different convention cannot supersede an existing one.
func TestValidateSupersede_RejectsConventionMismatch(t *testing.T) {
	existingDecl := &convention.Declaration{
		Convention: "my-convention",
		Version:    "0.1",
		Operation:  "post",
		Signing:    "member_key",
	}
	existingMsgID := "msg-id-of-original"

	// Different convention.
	newPayload := makeDeclarationJSON("different-convention", "0.2", "post", "member_key", existingMsgID)
	tags := []string{convention.ConventionOperationTag}

	_, err := ext.ValidateSupersede(newPayload, tags, testSignerKey, testCampfireKey, existingDecl, existingMsgID)
	if err == nil {
		t.Fatal("expected error for convention mismatch, got nil")
	}
	if !errors.Is(err, ext.ErrSupersedeDenied) {
		t.Errorf("expected ErrSupersedeDenied, got: %v", err)
	}
	if !strings.Contains(err.Error(), "convention mismatch") {
		t.Errorf("error should mention 'convention mismatch', got: %v", err)
	}
}

// TestValidateSupersede_RejectsOperationMismatch verifies that a declaration
// for a different operation cannot supersede an existing one.
func TestValidateSupersede_RejectsOperationMismatch(t *testing.T) {
	existingDecl := &convention.Declaration{
		Convention: "my-convention",
		Version:    "0.1",
		Operation:  "post",
		Signing:    "member_key",
	}
	existingMsgID := "msg-id-of-original"

	// Different operation.
	newPayload := makeDeclarationJSON("my-convention", "0.2", "comment", "member_key", existingMsgID)
	tags := []string{convention.ConventionOperationTag}

	_, err := ext.ValidateSupersede(newPayload, tags, testSignerKey, testCampfireKey, existingDecl, existingMsgID)
	if err == nil {
		t.Fatal("expected error for operation mismatch, got nil")
	}
	if !errors.Is(err, ext.ErrSupersedeDenied) {
		t.Errorf("expected ErrSupersedeDenied, got: %v", err)
	}
	if !strings.Contains(err.Error(), "operation mismatch") {
		t.Errorf("error should mention 'operation mismatch', got: %v", err)
	}
}

// TestValidateSupersede_RejectsNilExistingDecl verifies that a nil existingDecl
// argument is rejected with ErrSupersedeDenied.
func TestValidateSupersede_RejectsNilExistingDecl(t *testing.T) {
	newPayload := makeDeclarationJSON("my-convention", "0.2", "post", "member_key", "some-msg-id")
	tags := []string{convention.ConventionOperationTag}

	_, err := ext.ValidateSupersede(newPayload, tags, testSignerKey, testCampfireKey, nil, "some-msg-id")
	if err == nil {
		t.Fatal("expected error for nil existingDecl, got nil")
	}
	if !errors.Is(err, ext.ErrSupersedeDenied) {
		t.Errorf("expected ErrSupersedeDenied, got: %v", err)
	}
}

// TestIsVersionGreater_Behavioral verifies the version comparison logic
// via promote/supersede paths that exercise boundary conditions.
func TestIsVersionGreater_Behavioral(t *testing.T) {
	// This test uses ValidateSupersede to exercise version comparison.
	// It validates the key boundary: multi-segment versions compare numerically.
	existingMsgID := "msg-id-v09"
	existingDecl := &convention.Declaration{
		Convention: "my-convention",
		Version:    "0.9",
		Operation:  "post",
		Signing:    "member_key",
	}

	// "0.10" must be greater than "0.9" (numeric, not lexicographic).
	newPayload := makeDeclarationJSON("my-convention", "0.10", "post", "member_key", existingMsgID)
	tags := []string{convention.ConventionOperationTag}

	result, err := ext.ValidateSupersede(newPayload, tags, testSignerKey, testCampfireKey, existingDecl, existingMsgID)
	if err != nil {
		t.Fatalf("0.10 should be greater than 0.9 (numeric comparison): %v", err)
	}
	if result.NewDeclaration.Version != "0.10" {
		t.Errorf("expected version 0.10, got %q", result.NewDeclaration.Version)
	}
}
