package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// hiddenCmds lists commands that should NOT appear in cf --help output
// but must still be executable when called directly.
var hiddenCmds = []string{
	"dag",
	"discover",
	"serve",
	"root",
	"provenance",
	"connect",
}

// TestHiddenCommandsNotInHelp verifies that the 6 experimental/plumbing
// commands do not appear in the default --help output.
func TestHiddenCommandsNotInHelp(t *testing.T) {
	assignCommandGroups()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	rootCmd.SetArgs([]string{"--help"})
	_ = rootCmd.Execute()
	rootCmd.SetArgs(nil)

	output := buf.String()

	for _, name := range hiddenCmds {
		// Match "  name " or "  name\t" to avoid false positives in descriptions.
		if strings.Contains(output, "  "+name+" ") || strings.Contains(output, "  "+name+"\t") || strings.HasSuffix(output, "  "+name) {
			t.Errorf("hidden command %q must not appear in --help output\ngot:\n%s", name, output)
		}
	}
}

// TestHiddenCommandsStillExecutable verifies that each hidden command still
// has a --help that returns usage (i.e. the command was not removed).
func TestHiddenCommandsStillExecutable(t *testing.T) {
	for _, name := range hiddenCmds {
		t.Run(name, func(t *testing.T) {
			// Find the command in rootCmd's command tree.
			sub, _, err := rootCmd.Find([]string{name})
			if err != nil || sub == nil || sub == rootCmd {
				t.Errorf("hidden command %q not found in command tree: %v", name, err)
				return
			}
			if sub.Name() != name {
				t.Errorf("expected command name %q, got %q", name, sub.Name())
			}
			// Verify it is actually marked Hidden.
			if !sub.Hidden {
				t.Errorf("command %q should have Hidden: true", name)
			}
		})
	}
}
