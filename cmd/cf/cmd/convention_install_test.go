package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	campfire "github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/store"
	fstransport "github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// testInstallDecl builds a minimal valid convention declaration payload for testing.
func testInstallDecl(convention, operation, version string) []byte {
	d := map[string]any{
		"convention":  convention,
		"version":     version,
		"operation":   operation,
		"description": "Test " + operation + " operation",
		"produces_tags": []map[string]any{
			{"tag": convention + ":" + operation, "cardinality": "exactly_one"},
		},
		"args": []map[string]any{
			{"name": "text", "type": "string", "required": true, "max_length": 1000},
		},
		"signing": "member_key",
	}
	b, _ := json.Marshal(d)
	return b
}

// TestConventionInstall_FromFile installs a single declaration file and verifies it
// appears in the campfire and in cf help output.
func TestConventionInstall_FromFile(t *testing.T) {
	payload := testInstallDecl("install-conv", "post", "0.1")
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	// Write declaration to a temp file.
	dir := t.TempDir()
	declFile := filepath.Join(dir, "post.json")
	if err := os.WriteFile(declFile, payload, 0600); err != nil {
		t.Fatalf("writing decl file: %v", err)
	}

	if err := runConventionInstall(conventionInstallCmd, []string{campfireID[:12], declFile}); err != nil {
		t.Fatalf("install from file: %v", err)
	}

	// Verify the declaration is in the store.
	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected declaration message in store, got none")
	}

	// Verify cf help shows the operation.
	decls, err := listConventionOperations(context.Background(), s, campfireID)
	if err != nil {
		t.Fatalf("listing convention operations: %v", err)
	}
	found := false
	for _, d := range decls {
		if d.Operation == "post" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected operation 'post' to appear in cf help, operations found: %v", decls)
	}
}

// TestConventionInstall_FromDirectory installs all .json files in a directory.
func TestConventionInstall_FromDirectory(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	dir := t.TempDir()
	// Write two declaration files.
	for _, op := range []string{"send", "receive"} {
		payload := testInstallDecl("dir-conv", op, "0.1")
		if err := os.WriteFile(filepath.Join(dir, op+".json"), payload, 0600); err != nil {
			t.Fatalf("writing %s.json: %v", op, err)
		}
	}
	// Write a non-.json file — should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0600); err != nil {
		t.Fatalf("writing readme.txt: %v", err)
	}

	if err := runConventionInstall(conventionInstallCmd, []string{campfireID[:12], dir}); err != nil {
		t.Fatalf("install from directory: %v", err)
	}

	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	decls, err := listConventionOperations(context.Background(), s, campfireID)
	if err != nil {
		t.Fatalf("listing operations: %v", err)
	}
	opNames := map[string]bool{}
	for _, d := range decls {
		opNames[d.Operation] = true
	}
	if !opNames["send"] || !opNames["receive"] {
		t.Errorf("expected both 'send' and 'receive' operations, got: %v", opNames)
	}
}

// TestConventionInstall_DuplicateSkip verifies that installing the same declaration twice
// skips the second install rather than posting a duplicate.
func TestConventionInstall_DuplicateSkip(t *testing.T) {
	payload := testInstallDecl("dup-conv", "op", "0.1")
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	dir := t.TempDir()
	declFile := filepath.Join(dir, "op.json")
	if err := os.WriteFile(declFile, payload, 0600); err != nil {
		t.Fatalf("writing decl: %v", err)
	}

	// Install once.
	if err := runConventionInstall(conventionInstallCmd, []string{campfireID[:12], declFile}); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// Install again — should succeed (skip, not error).
	if err := runConventionInstall(conventionInstallCmd, []string{campfireID[:12], declFile}); err != nil {
		t.Fatalf("second install (should skip duplicate): %v", err)
	}

	// Verify only one declaration message was posted.
	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected exactly 1 declaration message (duplicate skipped), got %d", len(msgs))
	}
}

// TestConventionInstall_LintFailure verifies that invalid declarations are rejected before posting.
func TestConventionInstall_LintFailure(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	// Write an invalid declaration (missing required fields).
	invalidPayload := []byte(`{"convention": "bad-conv"}`)
	dir := t.TempDir()
	declFile := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(declFile, invalidPayload, 0600); err != nil {
		t.Fatalf("writing invalid decl: %v", err)
	}

	err := runConventionInstall(conventionInstallCmd, []string{campfireID[:12], declFile})
	if err == nil {
		t.Fatal("expected error for invalid declaration, got nil")
	}

	// Verify no declaration was posted.
	s, err2 := openStore()
	if err2 != nil {
		t.Fatalf("opening store: %v", err2)
	}
	defer s.Close()

	msgs, err2 := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if err2 != nil {
		t.Fatalf("listing messages: %v", err2)
	}
	if len(msgs) != 0 {
		t.Errorf("expected no messages posted after lint failure, got %d", len(msgs))
	}
}

// TestConventionInstall_FromSourceCampfire verifies that --from copies declarations
// from a source campfire to the target campfire.
func TestConventionInstall_FromSourceCampfire(t *testing.T) {
	sourcePayload := testInstallDecl("from-conv", "notify", "0.1")

	// Use setupDispatchEnv to create a full environment with one campfire (source).
	sourceCampfireID, cleanup := setupDispatchEnv(t, sourcePayload)
	defer cleanup()

	// Create a second campfire in the same environment as the target.
	// setupDispatchEnv sets CF_HOME; create a second campfire in the same store.
	targetCampfireID := setupAdditionalCampfire(t)

	// Reset flag state before and after.
	conventionInstallFrom = sourceCampfireID
	defer func() { conventionInstallFrom = "" }()

	if err := runConventionInstall(conventionInstallCmd, []string{targetCampfireID[:12]}); err != nil {
		t.Fatalf("install --from source campfire: %v", err)
	}

	// Verify the declaration is now in the target campfire.
	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	decls, err := listConventionOperations(context.Background(), s, targetCampfireID)
	if err != nil {
		t.Fatalf("listing operations on target: %v", err)
	}
	found := false
	for _, d := range decls {
		if d.Operation == "notify" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'notify' operation on target campfire after --from copy, operations: %v", decls)
	}
}

// setupAdditionalCampfire creates a second campfire membership in the current CF_HOME environment.
// It must be called after setupDispatchEnv has run (which sets CF_HOME and creates identity/store).
func setupAdditionalCampfire(t *testing.T) string {
	t.Helper()

	agentID, s, err := requireAgentAndStore()
	if err != nil {
		t.Fatalf("requireAgentAndStore for second campfire: %v", err)
	}
	defer s.Close()

	campfireDir := filepath.Join(CFHome(), "campfire-transport-2")
	cf2, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating second campfire: %v", err)
	}
	cf2.AddMember(agentID.PublicKey)
	campfireID := cf2.PublicKeyHex()

	tr := fstransport.ForDir(campfireDir)
	if err := tr.Init(cf2); err != nil {
		t.Fatalf("initializing second campfire transport: %v", err)
	}
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  1,
	}); err != nil {
		t.Fatalf("writing member record for second campfire: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: campfireDir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("adding second campfire membership: %v", err)
	}
	return campfireID
}

// TestConventionInstall_ArgValidation verifies error cases for missing args.
func TestConventionInstall_ArgValidation(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	t.Run("no args", func(t *testing.T) {
		if err := runConventionInstall(conventionInstallCmd, []string{}); err == nil {
			t.Fatal("expected error with no args")
		}
	})

	t.Run("campfire only no file or from", func(t *testing.T) {
		if err := runConventionInstall(conventionInstallCmd, []string{campfireID[:12]}); err == nil {
			t.Fatal("expected error with campfire-id only and no --from")
		}
	})

	t.Run("both file and from", func(t *testing.T) {
		conventionInstallFrom = "some-campfire"
		defer func() { conventionInstallFrom = "" }()
		dir := t.TempDir()
		if err := runConventionInstall(conventionInstallCmd, []string{campfireID[:12], dir}); err == nil {
			t.Fatal("expected error when both file/dir and --from are provided")
		}
	})
}
