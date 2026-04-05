package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCFHome_NoDirectories verifies that CFHome returns ~/.cf when neither
// ~/.cf nor ~/.campfire exists.
func TestCFHome_NoDirectories(t *testing.T) {
	// Use a temp dir as fake home so neither .cf nor .campfire exist.
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_HOME", "")
	cfHome = "" // reset the flag variable

	var stderr bytes.Buffer
	result := cfHomeWithWriter(&stderr)

	want := filepath.Join(fakeHome, ".cf")
	if result != want {
		t.Errorf("CFHome() = %q, want %q", result, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output, got: %s", stderr.String())
	}
}

// TestCFHome_OnlyCampfireExists verifies that CFHome returns ~/.campfire with
// a deprecation notice on stderr when ~/.campfire exists but ~/.cf does not.
func TestCFHome_OnlyCampfireExists(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_HOME", "")
	cfHome = ""

	// Create ~/.campfire but not ~/.cf.
	campfireDir := filepath.Join(fakeHome, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		t.Fatalf("creating .campfire dir: %v", err)
	}

	var stderr bytes.Buffer
	result := cfHomeWithWriter(&stderr)

	if result != campfireDir {
		t.Errorf("CFHome() = %q, want %q", result, campfireDir)
	}
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "deprecated") && !strings.Contains(stderrStr, "~/.cf") {
		t.Errorf("expected deprecation notice on stderr mentioning ~/.cf, got: %q", stderrStr)
	}
}

// TestCFHome_BothExist verifies that CFHome returns ~/.cf (no deprecation)
// when both ~/.cf and ~/.campfire exist.
func TestCFHome_BothExist(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_HOME", "")
	cfHome = ""

	// Create both directories.
	cfDir := filepath.Join(fakeHome, ".cf")
	campfireDir := filepath.Join(fakeHome, ".campfire")
	for _, d := range []string{cfDir, campfireDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatalf("creating dir %s: %v", d, err)
		}
	}

	var stderr bytes.Buffer
	result := cfHomeWithWriter(&stderr)

	if result != cfDir {
		t.Errorf("CFHome() = %q, want %q", result, cfDir)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output, got: %s", stderr.String())
	}
}

// TestCFHome_EnvOverride verifies that CF_HOME env var takes precedence over
// both ~/.cf and ~/.campfire.
func TestCFHome_EnvOverride(t *testing.T) {
	fakeHome := t.TempDir()
	customHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("CF_HOME", customHome)
	cfHome = ""

	// Create both default directories — env should still win.
	for _, d := range []string{
		filepath.Join(fakeHome, ".cf"),
		filepath.Join(fakeHome, ".campfire"),
	} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatalf("creating dir %s: %v", d, err)
		}
	}

	var stderr bytes.Buffer
	result := cfHomeWithWriter(&stderr)

	if result != customHome {
		t.Errorf("CFHome() = %q, want %q (CF_HOME env)", result, customHome)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output with CF_HOME override, got: %s", stderr.String())
	}
}
