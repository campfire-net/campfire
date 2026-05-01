package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestArgv0DispatchDetectName verifies that isMulticallInvocation correctly
// identifies when the binary is invoked under a non-cf name.
func TestArgv0DispatchDetectName(t *testing.T) {
	tests := []struct {
		name       string
		binaryName string
		want       bool
	}{
		{"cf is not multicall", "cf", false},
		{"cf with path", "/usr/local/bin/cf", false},
		{"dontguess is multicall", "dontguess", true},
		{"social is multicall", "social", true},
		{"rd is multicall", "rd", true},
		{"cf-primitives is not multicall", "cf-primitives", false},
		{"empty falls back to cf", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMulticallInvocation(tt.binaryName)
			if got != tt.want {
				t.Errorf("isMulticallInvocation(%q) = %v, want %v", tt.binaryName, got, tt.want)
			}
		})
	}
}

// TestArgv0DispatchRejectInjection verifies that argument-injection via a
// crafted binary name is rejected. An argv[0] containing shell metacharacters,
// path separators, or whitespace must not reach dispatchConventionOp as-is.
func TestArgv0DispatchRejectInjection(t *testing.T) {
	maliciousNames := []string{
		"../../etc/passwd",
		"social; rm -rf /",
		"social\x00null",
		"so cial",
		"social\ttab",
	}
	for _, name := range maliciousNames {
		t.Run(name, func(t *testing.T) {
			if isMulticallInvocation(name) {
				// It detected a multicall — that's fine, but sanitize must not
				// allow the raw name through. Verify sanitizeBinaryName returns
				// a safe form or empty.
				sanitized := sanitizeBinaryName(name)
				// A safe name must be non-empty and contain only word chars/hyphens.
				for _, ch := range sanitized {
					if !isNameChar(ch) {
						t.Errorf("sanitized name %q contains unsafe char %q", sanitized, ch)
					}
				}
			}
		})
	}
}

// isNameChar returns true if c is a safe character for a campfire/app name.
func isNameChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.'
}

// TestPerAppConfigOverlay verifies that when cf is invoked as "social",
// per-app config at `~/.cf/apps/social/config.toml` overlays the base config.
func TestPerAppConfigOverlay(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_HOME", "")
	cfHome = "" // reset package-level flag

	// Write global config.
	globalDir := filepath.Join(fakeHome, ".cf")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatalf("creating global dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config.toml"), []byte(`
[identity]
display_name = "baron"
`), 0600); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	// Write app-specific config under ~/.cf/apps/social/config.toml.
	appDir := filepath.Join(globalDir, "apps", "social")
	if err := os.MkdirAll(appDir, 0700); err != nil {
		t.Fatalf("creating app dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.toml"), []byte(`
[identity]
display_name = "social-user"
`), 0600); err != nil {
		t.Fatalf("writing app config: %v", err)
	}

	projectDir := t.TempDir()
	cfg, layers, _, err := loadConfigWithApp(globalDir, projectDir, "social")
	if err != nil {
		t.Fatalf("loadConfigWithApp: %v", err)
	}

	// App overlay should take highest priority among global-tier layers.
	if cfg.Identity.DisplayName != "social-user" {
		t.Errorf("display_name = %q, want %q", cfg.Identity.DisplayName, "social-user")
	}

	// App config layer should appear in layers list.
	found := false
	for _, l := range layers {
		if l.Source == "app" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected an 'app' layer in config layers, got none")
	}
}

// TestPerAppConfigOverlay_NoAppConfig verifies that loadConfigWithApp behaves
// identically to the standard LoadConfig when no app-specific config exists.
func TestPerAppConfigOverlay_NoAppConfig(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_HOME", "")
	cfHome = ""

	globalDir := filepath.Join(fakeHome, ".cf")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatalf("creating global dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config.toml"), []byte(`
[identity]
display_name = "global-user"
`), 0600); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	projectDir := t.TempDir()
	cfg, _, _, err := loadConfigWithApp(globalDir, projectDir, "nonexistent-app")
	if err != nil {
		t.Fatalf("loadConfigWithApp: %v", err)
	}

	// Without an app config, falls back to global.
	if cfg.Identity.DisplayName != "global-user" {
		t.Errorf("display_name = %q, want %q", cfg.Identity.DisplayName, "global-user")
	}
}

// TestPerAppConfigOverlay_ProjectOverridesApp verifies that project-level config
// (closer to CWD) takes precedence over the app-level config.
func TestPerAppConfigOverlay_ProjectOverridesApp(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_HOME", "")
	cfHome = ""

	globalDir := filepath.Join(fakeHome, ".cf")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatalf("creating global dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config.toml"), []byte(`
[identity]
display_name = "global-user"
`), 0600); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	// Write app-specific global overlay.
	appDir := filepath.Join(globalDir, "apps", "social")
	if err := os.MkdirAll(appDir, 0700); err != nil {
		t.Fatalf("creating app dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.toml"), []byte(`
[identity]
display_name = "social-user"
`), 0600); err != nil {
		t.Fatalf("writing app config: %v", err)
	}

	// Write project config that overrides both.
	projectDir := t.TempDir()
	projectCFDir := filepath.Join(projectDir, ".cf")
	if err := os.MkdirAll(projectCFDir, 0700); err != nil {
		t.Fatalf("creating project .cf dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectCFDir, "config.toml"), []byte(`
[identity]
display_name = "project-user"
`), 0600); err != nil {
		t.Fatalf("writing project config: %v", err)
	}

	cfg, _, _, err := loadConfigWithApp(globalDir, projectDir, "social")
	if err != nil {
		t.Fatalf("loadConfigWithApp: %v", err)
	}

	// Project config is most specific — must win.
	if cfg.Identity.DisplayName != "project-user" {
		t.Errorf("display_name = %q, want %q", cfg.Identity.DisplayName, "project-user")
	}
}

// TestPrimitivesHiddenFromCfHelp verifies that protocol primitive commands
// (send, read, etc.) do not appear in `cf --help` output.
// They should only be visible in cf-primitives or via --help-primitives.
func TestPrimitivesHiddenFromCfHelp(t *testing.T) {
	assignCommandGroups()
	// Snapshot hidden state before modification.
	savedHidden := make(map[string]bool)
	for _, sub := range rootCmd.Commands() {
		savedHidden[sub.Name()] = sub.Hidden
	}
	assignPrimitiveCommands()
	t.Cleanup(func() {
		for _, sub := range rootCmd.Commands() {
			if orig, ok := savedHidden[sub.Name()]; ok {
				sub.Hidden = orig
			}
		}
	})

	primitiveCmds := []string{"send", "read", "await", "inspect", "compact", "dm"}
	for _, name := range primitiveCmds {
		t.Run(name, func(t *testing.T) {
			sub, _, err := rootCmd.Find([]string{name})
			if err != nil || sub == nil || sub == rootCmd {
				t.Skipf("command %q not found — may have been moved to cf-primitives", name)
				return
			}
			if !sub.Hidden {
				t.Errorf("primitive command %q must be hidden in cf binary; set cmd.Hidden = true", name)
			}
		})
	}
}

// TestConventionCommandsVisibleInCfHelp verifies that convention-surface
// commands remain visible in `cf --help` output.
func TestConventionCommandsVisibleInCfHelp(t *testing.T) {
	assignCommandGroups()
	// Snapshot hidden state before modification.
	savedHidden := make(map[string]bool)
	for _, sub := range rootCmd.Commands() {
		savedHidden[sub.Name()] = sub.Hidden
	}
	assignPrimitiveCommands()
	t.Cleanup(func() {
		for _, sub := range rootCmd.Commands() {
			if orig, ok := savedHidden[sub.Name()]; ok {
				sub.Hidden = orig
			}
		}
	})

	conventionCmds := []string{"convention", "swarm", "join", "init", "config", "trust", "alias"}
	for _, name := range conventionCmds {
		t.Run(name, func(t *testing.T) {
			sub, _, err := rootCmd.Find([]string{name})
			if err != nil || sub == nil || sub == rootCmd {
				t.Fatalf("convention command %q not found in command tree: %v", name, err)
			}
			if sub.Hidden {
				t.Errorf("convention command %q must NOT be hidden in cf binary", name)
			}
		})
	}
}
