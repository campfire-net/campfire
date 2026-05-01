package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// setupAdoptEnv creates a CF_HOME with identity, store, and a ~home campfire.
// Returns: agentID, store, cfHomeDir, homeID, cleanup.
func setupAdoptEnv(t *testing.T) (*identity.Identity, store.Store, string, string) {
	t.Helper()

	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()

	t.Setenv("CF_HOME", cfHomeDir)
	cfHome = "" // reset cached value

	// Set up isolated beacon dir to avoid scanning real ~/.campfire/beacons.
	beaconDir := filepath.Join(cfHomeDir, "beacons")
	if err := os.MkdirAll(beaconDir, 0700); err != nil {
		t.Fatalf("creating beacon dir: %v", err)
	}
	t.Setenv("CF_BEACON_DIR", beaconDir)

	// Generate agent identity.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Open store.
	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Create ~home campfire using the shared helper from home_test.go.
	homeID := createTestCampfire(t, agentID, s, transportBaseDir)

	// Set "home" alias so ~home resolves correctly.
	aliasStore := naming.NewAliasStore(cfHomeDir)
	if err := aliasStore.Set("home", homeID); err != nil {
		t.Fatalf("setting home alias: %v", err)
	}

	t.Cleanup(func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
		os.Unsetenv("CF_BEACON_DIR")
	})

	return agentID, s, cfHomeDir, homeID
}

// makeAdoptDecl returns a minimal valid declaration payload for testing.
func makeAdoptDecl(conventionName, operation, version string) []byte {
	d := map[string]any{
		"convention":  conventionName,
		"version":     version,
		"operation":   operation,
		"description": "Test " + operation + " operation",
		"produces_tags": []map[string]any{
			{"tag": conventionName + ":" + operation, "cardinality": "exactly_one"},
		},
		"args": []map[string]any{
			{"name": "text", "type": "string", "required": true, "max_length": 1000},
		},
		"signing": "member_key",
	}
	b, _ := json.Marshal(d)
	return b
}

// TestConventionAdopt_FromFile verifies that adopting a file posts to ~home
// and that cf trust show then lists it.
func TestConventionAdopt_FromFile(t *testing.T) {
	agentID, s, cfHomeDir, homeID := setupAdoptEnv(t)
	_ = agentID
	_ = cfHomeDir

	// Write declaration to a temp file.
	payload := makeAdoptDecl("adopt-conv", "post", "0.1")
	declFile := filepath.Join(t.TempDir(), "test-decl.json")
	if err := os.WriteFile(declFile, payload, 0644); err != nil {
		t.Fatalf("writing decl file: %v", err)
	}

	// Run adopt.
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"convention", "adopt", declFile})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("convention adopt failed: %v\noutput: %s", err, out.String())
	}
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)

	// Verify message posted to ~home.
	msgs, err := s.ListMessages(homeID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected declaration message in ~home, got none")
	}

	// Verify output mentions ok.
	outStr := out.String()
	if !strings.Contains(outStr, "ok") {
		t.Errorf("expected 'ok' in output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "Adopted 1") {
		t.Errorf("expected 'Adopted 1' in output, got: %s", outStr)
	}
}

// TestConventionAdopt_TrustShowReflectsAdoption verifies that after adoption,
// loadLocalPolicyEngine sees the convention as "seed" source.
func TestConventionAdopt_TrustShowReflectsAdoption(t *testing.T) {
	agentID, s, cfHomeDir, _ := setupAdoptEnv(t)
	_ = agentID
	_ = cfHomeDir

	// Adopt a declaration directly via the internal function.
	payload := makeAdoptDecl("trust-check-conv", "fire", "0.2")
	declFile := filepath.Join(t.TempDir(), "decl.json")
	if err := os.WriteFile(declFile, payload, 0644); err != nil {
		t.Fatalf("writing decl file: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"convention", "adopt", declFile})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("convention adopt failed: %v\noutput: %s", err, out.String())
	}
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)

	// Now loadLocalPolicyEngine should see the adopted convention.
	engine := loadLocalPolicyEngine(s)
	adopted := engine.ListAdopted()
	if len(adopted) == 0 {
		t.Fatal("expected at least one adopted convention after adopt, got none")
	}

	found := false
	for _, ac := range adopted {
		if ac.Convention == "trust-check-conv" && ac.Operation == "fire" {
			found = true
			if ac.Source != "seed" {
				t.Errorf("expected source 'seed', got %q", ac.Source)
			}
			break
		}
	}
	if !found {
		t.Errorf("adopted convention trust-check-conv/fire not found in policy engine; got: %+v", adopted)
	}
}

// TestConventionAdopt_InvalidDeclarationFails verifies that a bad JSON file
// returns a lint error and does NOT post to ~home.
func TestConventionAdopt_InvalidDeclarationFails(t *testing.T) {
	agentID, s, cfHomeDir, homeID := setupAdoptEnv(t)
	_ = agentID
	_ = cfHomeDir

	// Write an invalid declaration (missing required fields).
	badPayload := []byte(`{"convention": "", "version": "", "operation": ""}`)
	declFile := filepath.Join(t.TempDir(), "bad-decl.json")
	if err := os.WriteFile(declFile, badPayload, 0644); err != nil {
		t.Fatalf("writing decl file: %v", err)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"convention", "adopt", declFile})
	err := rootCmd.Execute()
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)

	if err == nil {
		t.Fatal("expected error for invalid declaration, got nil")
	}

	// Verify nothing was posted to ~home.
	msgs, listErr := s.ListMessages(homeID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if listErr != nil {
		t.Fatalf("listing messages: %v", listErr)
	}
	if len(msgs) != 0 {
		t.Errorf("expected no messages in ~home after lint failure, got %d", len(msgs))
	}
}

// TestConventionAdopt_DuplicateSkipped verifies that adopting the same declaration
// twice skips the duplicate on the second call.
func TestConventionAdopt_DuplicateSkipped(t *testing.T) {
	agentID, s, cfHomeDir, homeID := setupAdoptEnv(t)
	_ = agentID
	_ = cfHomeDir

	payload := makeAdoptDecl("dup-conv", "post", "0.1")
	declFile := filepath.Join(t.TempDir(), "dup-decl.json")
	if err := os.WriteFile(declFile, payload, 0644); err != nil {
		t.Fatalf("writing decl file: %v", err)
	}

	adopt := func() (string, error) {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs([]string{"convention", "adopt", declFile})
		err := rootCmd.Execute()
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		return out.String(), err
	}

	// First adoption: should succeed.
	out1, err1 := adopt()
	if err1 != nil {
		t.Fatalf("first adopt failed: %v\noutput: %s", err1, out1)
	}
	if !strings.Contains(out1, "Adopted 1") {
		t.Errorf("expected 'Adopted 1' in first output, got: %s", out1)
	}

	// Second adoption: should skip the duplicate.
	out2, err2 := adopt()
	if err2 != nil {
		t.Fatalf("second adopt failed unexpectedly: %v\noutput: %s", err2, out2)
	}
	if !strings.Contains(out2, "skip") {
		t.Errorf("expected 'skip' in second output, got: %s", out2)
	}
	if !strings.Contains(out2, "Adopted 0") {
		t.Errorf("expected 'Adopted 0' in second output, got: %s", out2)
	}

	// Verify only one message in ~home (not duplicated).
	msgs, err := s.ListMessages(homeID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected exactly 1 message in ~home, got %d", len(msgs))
	}
}

// TestConventionAdopt_FromCampfire verifies that --from copies declarations
// from a source campfire to ~home.
func TestConventionAdopt_FromCampfire(t *testing.T) {
	agentID, s, cfHomeDir, homeID := setupAdoptEnv(t)
	_ = cfHomeDir

	// Create a second (source) campfire and post a declaration to it.
	transportBaseDir := t.TempDir()
	sourceID := createTestCampfire(t, agentID, s, transportBaseDir)

	// Post a declaration to the source campfire directly via the store.
	payload := makeAdoptDecl("from-conv", "notify", "0.3")

	tr := fs.New(transportBaseDir)
	_ = tr // transport exists but we post via protocol
	agentStore, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store for source post: %v", err)
	}
	defer agentStore.Close()

	// Use adoptSingle to post the declaration to the source campfire first.
	src := declSource{name: "from-conv/notify", payload: payload}
	existing := make(map[string]*convention.Declaration)
	adopted, fp, opName, postErr := adoptSingle(src, sourceID, agentID, agentStore, existing)
	if postErr != nil {
		t.Fatalf("posting to source campfire: %v", postErr)
	}
	if !adopted {
		t.Fatalf("expected adopted=true, op=%s fp=%s", opName, fp)
	}

	// Now run convention adopt --from <sourceID>.
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"convention", "adopt", "--from", sourceID})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("convention adopt --from failed: %v\noutput: %s", err, out.String())
	}
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)

	// Verify declaration appeared in ~home.
	msgs, listErr := s.ListMessages(homeID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if listErr != nil {
		t.Fatalf("listing ~home messages: %v", listErr)
	}
	if len(msgs) == 0 {
		t.Fatal("expected declaration in ~home after --from adopt, got none")
	}

	// Verify output mentions success.
	outStr := out.String()
	if !strings.Contains(outStr, "Adopted 1") {
		t.Errorf("expected 'Adopted 1' in output, got: %s", outStr)
	}
}
