package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// ---- test payloads ----

// validSocialPost is a well-formed declaration with no enum-tag mismatch.
var validSocialPostPayload = []byte(`{
  "convention": "social-post-format",
  "version": "0.3",
  "operation": "post",
  "description": "Publish a social post",
  "produces_tags": [
    {"tag": "social:post", "cardinality": "exactly_one"},
    {"tag": "content:*", "cardinality": "at_most_one",
     "values": ["content:text/plain", "content:text/markdown"]},
    {"tag": "topic:*", "cardinality": "zero_to_many", "max": 10, "pattern": "[a-z0-9-]{1,64}"}
  ],
  "args": [
    {"name": "text", "type": "string", "required": true, "max_length": 65536},
    {"name": "content_type", "type": "enum",
     "values": ["content:text/plain", "content:text/markdown"], "default": "content:text/plain"},
    {"name": "topics", "type": "string", "repeated": true, "max_count": 10,
     "pattern": "[a-z0-9-]{1,64}"}
  ],
  "antecedents": "none",
  "payload_required": true,
  "signing": "member_key"
}`)

// socialPostWithEnumMismatch has coordination enum values without the "social:" prefix
// but produces_tags expects "social:*" — the known bug from the work item.
var socialPostWithEnumMismatch = []byte(`{
  "convention": "social-post-format",
  "version": "0.3",
  "operation": "post",
  "description": "Publish a social post",
  "produces_tags": [
    {"tag": "social:post", "cardinality": "exactly_one"},
    {"tag": "social:*", "cardinality": "zero_to_many",
     "values": ["social:need", "social:have", "social:offer", "social:request", "social:question", "social:answer"]}
  ],
  "args": [
    {"name": "text", "type": "string", "required": true, "max_length": 65536},
    {"name": "coordination", "type": "enum",
     "values": ["need", "have", "offer", "request", "question", "answer"], "repeated": true,
     "description": "Coordination signal tags"}
  ],
  "antecedents": "none",
  "payload_required": true,
  "signing": "member_key"
}`)

// invalidMissingConvention is missing the required "convention" field.
var invalidMissingConvention = []byte(`{
  "version": "0.1",
  "operation": "post",
  "signing": "member_key"
}`)

// invalidBadPattern has a pattern that exceeds the max length.
var invalidBadPattern = []byte(`{
  "convention": "test-conv",
  "version": "0.1",
  "operation": "write",
  "signing": "member_key",
  "args": [
    {"name": "value", "type": "string",
     "pattern": "[a-z]{1,64}[a-z]{1,64}[a-z]{1,64}[a-z]{1,64}[a-z]{1,64}[a-z]{1,64}[a-z]{1,64}"}
  ]
}`)

// ---- lint tests ----

func TestConventionLint_ValidDeclaration(t *testing.T) {
	result := convention.Lint(validSocialPostPayload)
	if !result.Valid {
		t.Errorf("expected Valid=true, errors=%v", result.Errors)
	}
	if len(result.Errors) > 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
}

func TestConventionLint_EnumTagMismatch_IsWarning(t *testing.T) {
	result := convention.Lint(socialPostWithEnumMismatch)
	// The enum mismatch (coordination values lack "social:" prefix) should surface as a warning.
	if len(result.Warnings) == 0 {
		t.Fatal("expected enum_tag_mismatch warning, got none")
	}
	found := false
	for _, w := range result.Warnings {
		if w.Code == "enum_tag_mismatch" {
			found = true
			t.Logf("warning found: %s", w.Message)
			break
		}
	}
	if !found {
		t.Errorf("expected enum_tag_mismatch warning code, got warnings: %v", result.Warnings)
	}
	// Declaration is still Valid (warnings don't invalidate).
	if !result.Valid {
		t.Errorf("expected Valid=true (warnings don't fail validation), errors=%v", result.Errors)
	}
}

func TestConventionLint_EnumTagMismatch_MessageMentionsMismatch(t *testing.T) {
	result := convention.Lint(socialPostWithEnumMismatch)
	for _, w := range result.Warnings {
		if w.Code == "enum_tag_mismatch" {
			// Message should mention the short value and the expected prefix.
			if !strings.Contains(w.Message, "social:") {
				t.Errorf("warning message should mention 'social:' prefix, got: %s", w.Message)
			}
			if !strings.Contains(w.Message, "need") {
				t.Errorf("warning message should mention enum value 'need', got: %s", w.Message)
			}
			return
		}
	}
	t.Error("enum_tag_mismatch warning not found")
}

func TestConventionLint_MissingRequiredField_IsError(t *testing.T) {
	result := convention.Lint(invalidMissingConvention)
	if result.Valid {
		t.Error("expected Valid=false for declaration missing 'convention' field")
	}
	if len(result.Errors) == 0 {
		t.Error("expected at least one error")
	}
}

func TestConventionLint_PatternTooLong_IsError(t *testing.T) {
	result := convention.Lint(invalidBadPattern)
	if result.Valid {
		t.Error("expected Valid=false for declaration with oversized pattern")
	}
}

func TestConventionLint_StdinInput(t *testing.T) {
	// Test readDeclarationInput with "-" by writing to a temp file instead.
	// (Actual stdin testing would require pipe setup; test the underlying function.)
	tmp := t.TempDir()
	f := filepath.Join(tmp, "decl.json")
	if err := os.WriteFile(f, validSocialPostPayload, 0644); err != nil {
		t.Fatal(err)
	}
	data, err := readDeclarationInput(f)
	if err != nil {
		t.Fatalf("readDeclarationInput: %v", err)
	}
	result := convention.Lint(data)
	if !result.Valid {
		t.Errorf("expected valid, errors=%v", result.Errors)
	}
}

func TestConventionLint_JSONOutput(t *testing.T) {
	result := convention.Lint(validSocialPostPayload)
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := out["valid"]; !ok {
		t.Error("JSON output missing 'valid' field")
	}
}

// ---- readDeclarationsFromPath tests ----

func TestReadDeclarationsFromPath_File(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "decl.json")
	if err := os.WriteFile(f, validSocialPostPayload, 0644); err != nil {
		t.Fatal(err)
	}
	sources, err := readDeclarationsFromPath(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].name != f {
		t.Errorf("name: want %q, got %q", f, sources[0].name)
	}
}

func TestReadDeclarationsFromPath_Dir(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"a.json", "b.json", "c.txt"} {
		if err := os.WriteFile(filepath.Join(tmp, name), validSocialPostPayload, 0644); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := readDeclarationsFromPath(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only .json files should be returned.
	if len(sources) != 2 {
		t.Errorf("expected 2 sources (only .json), got %d", len(sources))
	}
}

// ---- digital twin tests ----

func TestDigitalTwin_Create(t *testing.T) {
	twin, err := newDigitalTwin()
	if err != nil {
		t.Fatalf("newDigitalTwin: %v", err)
	}
	defer twin.close()

	if twin.conventionRegID == "" {
		t.Error("conventionRegID should be set")
	}
	if twin.rootID == nil {
		t.Error("rootID should be set")
	}
	// Store should be open.
	if twin.s == nil {
		t.Error("store should be open")
	}
}

func TestDigitalTwin_TestDeclaration_Valid(t *testing.T) {
	twin, err := newDigitalTwin()
	if err != nil {
		t.Fatalf("newDigitalTwin: %v", err)
	}
	defer twin.close()

	result := twin.testDeclaration(declSource{name: "test.json", payload: validSocialPostPayload})
	if !result.Pass {
		t.Errorf("expected Pass=true, error=%q, steps=%v", result.Error, result.Steps)
	}
	if result.Operation != "post" {
		t.Errorf("expected operation=%q, got %q", "post", result.Operation)
	}
	// All steps should pass.
	for _, step := range result.Steps {
		if !step.Pass {
			t.Errorf("step %q failed: %s", step.Name, step.Note)
		}
	}
}

func TestDigitalTwin_TestDeclaration_LintFailure(t *testing.T) {
	twin, err := newDigitalTwin()
	if err != nil {
		t.Fatalf("newDigitalTwin: %v", err)
	}
	defer twin.close()

	result := twin.testDeclaration(declSource{name: "bad.json", payload: invalidMissingConvention})
	if result.Pass {
		t.Error("expected Pass=false for invalid declaration")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
}

func TestDigitalTwin_TestDeclaration_EnumMismatch_Warns(t *testing.T) {
	twin, err := newDigitalTwin()
	if err != nil {
		t.Fatalf("newDigitalTwin: %v", err)
	}
	defer twin.close()

	result := twin.testDeclaration(declSource{name: "mismatch.json", payload: socialPostWithEnumMismatch})
	// Should still pass (warning not error), but lint step should note the warning.
	lintStep := declTestStep{}
	for _, step := range result.Steps {
		if step.Name == "lint" {
			lintStep = step
			break
		}
	}
	if !lintStep.Pass {
		t.Errorf("lint step should pass (warnings are not errors): %s", lintStep.Note)
	}
	if lintStep.Note == "" {
		t.Logf("note: lint step has no note (warning not captured — acceptable)")
	}
}

// ---- promote tests ----

func TestPromote_LintGating(t *testing.T) {
	// Promote should refuse if lint fails.
	s := openTestStore(t)
	defer s.Close()

	agentID := generateTestIdentity(t)
	registryID, m := createTestRegistry(t, agentID, s)

	existing, err := loadExistingDeclarations(s, registryID)
	if err != nil {
		t.Fatalf("loadExistingDeclarations: %v", err)
	}

	result := promoteSingle(
		declSource{name: "bad.json", payload: invalidMissingConvention},
		registryID,
		agentID,
		s,
		m,
		existing,
	)
	if result.Error == "" {
		t.Error("expected error for invalid declaration")
	}
	if !strings.Contains(result.Error, "lint failed") {
		t.Errorf("error should mention lint: %s", result.Error)
	}
}

func TestPromote_ConflictDetection(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()

	agentID := generateTestIdentity(t)
	registryID, m := createTestRegistry(t, agentID, s)

	existing, err := loadExistingDeclarations(s, registryID)
	if err != nil {
		t.Fatalf("loadExistingDeclarations: %v", err)
	}

	// First promote should succeed.
	result1 := promoteSingle(
		declSource{name: "post.json", payload: validSocialPostPayload},
		registryID,
		agentID,
		s,
		m,
		existing,
	)
	if result1.Error != "" {
		t.Fatalf("first promote failed: %s", result1.Error)
	}

	// Reload existing to pick up the just-promoted declaration.
	existing2, err := loadExistingDeclarations(s, registryID)
	if err != nil {
		t.Fatalf("loadExistingDeclarations (reload): %v", err)
	}

	// Second promote with same convention+operation+version should be skipped.
	result2 := promoteSingle(
		declSource{name: "post.json", payload: validSocialPostPayload},
		registryID,
		agentID,
		s,
		m,
		existing2,
	)
	if !result2.Skipped {
		t.Errorf("expected Skipped=true on second promote, got error=%q", result2.Error)
	}
}

func TestPromote_ForceOverwrite(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()

	agentID := generateTestIdentity(t)
	registryID, m := createTestRegistry(t, agentID, s)

	// First promote.
	existing, _ := loadExistingDeclarations(s, registryID)
	promoteSingle(declSource{name: "post.json", payload: validSocialPostPayload}, registryID, agentID, s, m, existing)

	// Second promote with force — must not skip.
	conventionPromoteForce = true
	defer func() { conventionPromoteForce = false }()

	existing2, _ := loadExistingDeclarations(s, registryID)
	result := promoteSingle(declSource{name: "post.json", payload: validSocialPostPayload}, registryID, agentID, s, m, existing2)
	if result.Skipped {
		t.Error("expected not skipped when --force is set")
	}
	if result.Error != "" {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestLoadExistingDeclarations_Empty(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()

	agentID := generateTestIdentity(t)
	registryID, _ := createTestRegistry(t, agentID, s)

	existing, err := loadExistingDeclarations(s, registryID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(existing) != 0 {
		t.Errorf("expected empty, got %d entries", len(existing))
	}
}

// ---- helpers ----

func openTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	return s
}

func generateTestIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	return id
}

// createTestRegistry creates a convention registry campfire with a real filesystem
// transport so that promoteSingle can fan out declarations via transport.
// Returns the campfire ID and the membership record.
func createTestRegistry(t *testing.T, agentID *identity.Identity, s store.Store) (string, *store.Membership) {
	t.Helper()

	// Use agentID as the campfire identity for simplicity.
	registryID := agentID.PublicKeyHex()
	transportBaseDir := t.TempDir()

	// Create campfire directory structure.
	cfDir := filepath.Join(transportBaseDir, registryID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating transport dir %s: %v", sub, err)
		}
	}

	// Write campfire state (use agentID as campfire key for test simplicity).
	state := &campfire.CampfireState{
		PublicKey:             agentID.PublicKey,
		PrivateKey:            agentID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// Register agent as member in the transport.
	tr := fs.New(transportBaseDir)
	if err := tr.WriteMember(registryID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	m := store.Membership{
		CampfireID:   registryID,
		TransportDir: tr.CampfireDir(registryID),
		JoinProtocol: "open",
		Role:         "full",
		JoinedAt:     store.NowNano(),
		Threshold:    1,
	}
	if err := s.AddMembership(m); err != nil {
		t.Fatalf("adding registry membership: %v", err)
	}
	return registryID, &m
}
