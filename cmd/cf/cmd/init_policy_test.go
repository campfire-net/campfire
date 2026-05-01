package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureInitWithPolicy runs cf init --policy <preset> in a temp CF_HOME and
// returns (stdout, error). The CF_HOME is returned so callers can inspect files.
func captureInitWithPolicy(t *testing.T, preset string) (cfHome string, err error) {
	t.Helper()
	cfHome = t.TempDir()
	t.Setenv("CF_HOME", cfHome)
	t.Setenv("CF_PASSPHRASE", "")

	initCmd.Flags().Set("force", "false")        //nolint:errcheck
	initCmd.Flags().Set("name", "")              //nolint:errcheck
	initCmd.Flags().Set("session", "false")      //nolint:errcheck
	initCmd.Flags().Set("policy", preset)        //nolint:errcheck
	defer initCmd.Flags().Set("policy", "")      //nolint:errcheck
	rootCmd.SetArgs([]string{"init", "--policy", preset})
	return cfHome, rootCmd.Execute()
}

// TestInitPolicy_PersonalDeveloper verifies that cf init --policy personal-developer
// writes grant-template.json with the correct preset, max_depth=1, multi_owner=false,
// and the four expected conventions (rd, dontguess, social, reach).
func TestInitPolicy_PersonalDeveloper(t *testing.T) {
	cfHome, err := captureInitWithPolicy(t, PolicyPresetPersonalDeveloper)
	if err != nil {
		t.Fatalf("cf init --policy personal-developer failed: %v", err)
	}

	tmpl := readGrantTemplate(t, cfHome)

	if tmpl.Preset != PolicyPresetPersonalDeveloper {
		t.Errorf("preset = %q, want %q", tmpl.Preset, PolicyPresetPersonalDeveloper)
	}
	if tmpl.MaxDepth != 1 {
		t.Errorf("max_depth = %d, want 1 (no delegation)", tmpl.MaxDepth)
	}
	if tmpl.MultiOwner {
		t.Errorf("multi_owner = true, want false for personal-developer")
	}
	assertHasConventions(t, tmpl, []string{"rd", "dontguess", "social", "reach"})

	// personal-developer: owner TTL should be 7d
	if tmpl.OwnerTTL != "7d" {
		t.Errorf("owner_ttl = %q, want 7d", tmpl.OwnerTTL)
	}
}

// TestInitPolicy_TeamMember verifies that cf init --policy team-member writes
// grant-template.json with multi_owner=true, depth 2, and the four conventions.
func TestInitPolicy_TeamMember(t *testing.T) {
	cfHome, err := captureInitWithPolicy(t, PolicyPresetTeamMember)
	if err != nil {
		t.Fatalf("cf init --policy team-member failed: %v", err)
	}

	tmpl := readGrantTemplate(t, cfHome)

	if tmpl.Preset != PolicyPresetTeamMember {
		t.Errorf("preset = %q, want %q", tmpl.Preset, PolicyPresetTeamMember)
	}
	if tmpl.MaxDepth != 2 {
		t.Errorf("max_depth = %d, want 2 (delegation enabled)", tmpl.MaxDepth)
	}
	if !tmpl.MultiOwner {
		t.Errorf("multi_owner = false, want true for team-member")
	}
	assertHasConventions(t, tmpl, []string{"rd", "dontguess", "social", "reach"})
}

// TestInitPolicy_PublicAgent verifies that cf init --policy public-agent writes
// grant-template.json with multi_owner=false, depth 1, and the four conventions
// with tighter op patterns (no admin ops like "list" or "*").
func TestInitPolicy_PublicAgent(t *testing.T) {
	cfHome, err := captureInitWithPolicy(t, PolicyPresetPublicAgent)
	if err != nil {
		t.Fatalf("cf init --policy public-agent failed: %v", err)
	}

	tmpl := readGrantTemplate(t, cfHome)

	if tmpl.Preset != PolicyPresetPublicAgent {
		t.Errorf("preset = %q, want %q", tmpl.Preset, PolicyPresetPublicAgent)
	}
	if tmpl.MaxDepth != 1 {
		t.Errorf("max_depth = %d, want 1 (no delegation)", tmpl.MaxDepth)
	}
	if tmpl.MultiOwner {
		t.Errorf("multi_owner = true, want false for public-agent")
	}
	assertHasConventions(t, tmpl, []string{"rd", "dontguess", "social", "reach"})

	// public-agent: owner TTL should be 24h (not 7d) — tighter ceiling
	if tmpl.OwnerTTL != "24h" {
		t.Errorf("owner_ttl = %q, want 24h for public-agent (tighter ceiling)", tmpl.OwnerTTL)
	}
}

// TestInitPolicy_TeamMemberDiffersFromPersonalDeveloper verifies that the two
// most similar presets produce detectably different grant-template.json files.
func TestInitPolicy_TeamMemberDiffersFromPersonalDeveloper(t *testing.T) {
	cfHome1, err := captureInitWithPolicy(t, PolicyPresetPersonalDeveloper)
	if err != nil {
		t.Fatalf("personal-developer init failed: %v", err)
	}
	cfHome2, err := captureInitWithPolicy(t, PolicyPresetTeamMember)
	if err != nil {
		t.Fatalf("team-member init failed: %v", err)
	}

	tmpl1 := readGrantTemplate(t, cfHome1)
	tmpl2 := readGrantTemplate(t, cfHome2)

	// They differ in at least preset name, max_depth, and multi_owner.
	if tmpl1.Preset == tmpl2.Preset {
		t.Errorf("both presets wrote the same preset name: %q", tmpl1.Preset)
	}
	if tmpl1.MaxDepth == tmpl2.MaxDepth {
		t.Errorf("expected max_depth to differ: both = %d", tmpl1.MaxDepth)
	}
	if tmpl1.MultiOwner == tmpl2.MultiOwner {
		t.Errorf("expected multi_owner to differ: both = %v", tmpl1.MultiOwner)
	}
}

// TestInitPolicy_UnknownPresetRejected verifies that an unknown preset name
// returns an error and does NOT write grant-template.json.
func TestInitPolicy_UnknownPresetRejected(t *testing.T) {
	cfHome := t.TempDir()
	t.Setenv("CF_HOME", cfHome)
	t.Setenv("CF_PASSPHRASE", "")

	initCmd.Flags().Set("force", "false")          //nolint:errcheck
	initCmd.Flags().Set("name", "")                //nolint:errcheck
	initCmd.Flags().Set("session", "false")        //nolint:errcheck
	initCmd.Flags().Set("policy", "unknown-preset") //nolint:errcheck
	defer initCmd.Flags().Set("policy", "")        //nolint:errcheck
	rootCmd.SetArgs([]string{"init", "--policy", "unknown-preset"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown policy preset, got nil")
	}
	if !strings.Contains(err.Error(), "unknown policy preset") {
		t.Errorf("expected 'unknown policy preset' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown-preset") {
		t.Errorf("expected the bad preset name in the error, got: %v", err)
	}

	// grant-template.json must NOT be written
	tmplPath := filepath.Join(cfHome, grantTemplateFile)
	if _, statErr := os.Stat(tmplPath); statErr == nil {
		t.Errorf("grant-template.json should not be written for unknown preset")
	}
}

// TestInitPolicy_NoFlag_NoGrantTemplate verifies that cf init with no --policy flag
// does NOT write grant-template.json (backward-compatible behavior).
func TestInitPolicy_NoFlag_NoGrantTemplate(t *testing.T) {
	cfHome := t.TempDir()
	t.Setenv("CF_HOME", cfHome)
	t.Setenv("CF_PASSPHRASE", "")

	initCmd.Flags().Set("force", "false")   //nolint:errcheck
	initCmd.Flags().Set("name", "")         //nolint:errcheck
	initCmd.Flags().Set("session", "false") //nolint:errcheck
	initCmd.Flags().Set("policy", "")       //nolint:errcheck
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf init (no flags) failed: %v", err)
	}

	// identity.json must exist (existing behavior unchanged)
	if _, err := os.Stat(filepath.Join(cfHome, "identity.json")); err != nil {
		t.Errorf("identity.json not found: %v", err)
	}

	// grant-template.json must NOT exist
	tmplPath := filepath.Join(cfHome, grantTemplateFile)
	if _, err := os.Stat(tmplPath); err == nil {
		t.Errorf("grant-template.json should not exist when --policy is not set")
	}
}

// TestInitPolicy_GrantTemplatePermissions verifies that grant-template.json is written
// with 0600 permissions (not world-readable).
func TestInitPolicy_GrantTemplatePermissions(t *testing.T) {
	cfHome, err := captureInitWithPolicy(t, PolicyPresetPersonalDeveloper)
	if err != nil {
		t.Fatalf("cf init --policy personal-developer failed: %v", err)
	}

	path := filepath.Join(cfHome, grantTemplateFile)
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("grant-template.json not found: %v", statErr)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("grant-template.json permissions = %04o, want 0600", perm)
	}
}

// TestPolicyTemplateFor_AllPresetsRoundTrip verifies that policyTemplateFor returns
// a non-nil template for all three valid preset names, and that each template
// round-trips through JSON without error.
func TestPolicyTemplateFor_AllPresetsRoundTrip(t *testing.T) {
	for _, preset := range []string{
		PolicyPresetPersonalDeveloper,
		PolicyPresetTeamMember,
		PolicyPresetPublicAgent,
	} {
		preset := preset
		t.Run(preset, func(t *testing.T) {
			tmpl, err := policyTemplateFor(preset)
			if err != nil {
				t.Fatalf("policyTemplateFor(%q) error: %v", preset, err)
			}
			if tmpl == nil {
				t.Fatalf("policyTemplateFor(%q) returned nil", preset)
			}

			// Round-trip through JSON
			data, marshalErr := json.Marshal(tmpl)
			if marshalErr != nil {
				t.Fatalf("json.Marshal failed: %v", marshalErr)
			}
			var decoded PolicyTemplate
			if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
				t.Fatalf("json.Unmarshal failed: %v", unmarshalErr)
			}
			if decoded.Preset != preset {
				t.Errorf("round-trip preset = %q, want %q", decoded.Preset, preset)
			}
		})
	}
}

// TestPolicyTemplateFor_UnknownPreset verifies that policyTemplateFor returns
// an error for an unrecognized preset name.
func TestPolicyTemplateFor_UnknownPreset(t *testing.T) {
	_, err := policyTemplateFor("not-a-preset")
	if err == nil {
		t.Fatal("expected error for unknown preset, got nil")
	}
	if !strings.Contains(err.Error(), "unknown policy preset") {
		t.Errorf("expected 'unknown policy preset' in error, got: %v", err)
	}
}

// --- helpers ---

// readGrantTemplate reads and parses grant-template.json from cfHome.
func readGrantTemplate(t *testing.T, cfHome string) *PolicyTemplate {
	t.Helper()
	path := filepath.Join(cfHome, grantTemplateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("grant-template.json not found in %s: %v", cfHome, err)
	}
	var tmpl PolicyTemplate
	if err := json.Unmarshal(data, &tmpl); err != nil {
		t.Fatalf("parsing grant-template.json: %v", err)
	}
	return &tmpl
}

// assertHasConventions verifies that tmpl.Caps includes at least one capability
// entry for each expected convention name.
func assertHasConventions(t *testing.T, tmpl *PolicyTemplate, wantConventions []string) {
	t.Helper()
	seen := make(map[string]bool)
	for _, cap := range tmpl.Caps {
		seen[cap.Convention] = true
	}
	for _, conv := range wantConventions {
		if !seen[conv] {
			t.Errorf("grant-template.json missing capability for convention %q (preset %q)", conv, tmpl.Preset)
		}
	}
}
