package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestHelpGroupsPresent verifies that cf --help renders the convention-surface
// group headers and that key commands appear under the correct groups.
// Protocol-primitive commands (send, read, compact, bridge, etc.) are hidden
// in the cf binary (they live in cf-primitives) so the Messages group header
// does not appear.
func TestHelpGroupsPresent(t *testing.T) {
	assignCommandGroups()
	// Snapshot the hidden state before calling assignPrimitiveCommands so we
	// can restore it precisely in Cleanup — avoiding contamination of tests that
	// rely on the natively-hidden commands (dag, serve, provenance, …) remaining
	// hidden after this test runs.
	savedHidden := make(map[string]bool)
	for _, sub := range rootCmd.Commands() {
		savedHidden[sub.Name()] = sub.Hidden
	}
	// Hide primitive commands as Execute() does — this mirrors the real runtime.
	assignPrimitiveCommands()
	// Restore exact hidden state after test.
	t.Cleanup(func() {
		for _, sub := range rootCmd.Commands() {
			if orig, ok := savedHidden[sub.Name()]; ok {
				sub.Hidden = orig
			}
		}
	})
	rootCmd.SetHelpCommandGroupID(groupAdvanced)
	rootCmd.SetCompletionCommandGroupID(groupAdvanced)

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	rootCmd.SetArgs([]string{"--help"})
	// Help exits via ErrHelp; ignore the error.
	_ = rootCmd.Execute()
	rootCmd.SetArgs(nil)

	output := buf.String()

	// Convention-surface group headers must appear.
	// Note: Messages group header is absent because all message commands
	// (send, read, compact, etc.) are protocol primitives, hidden in cf.
	wantHeaders := []string{
		"Convention Operations:",
		"Campfire Management:",
		"Advanced:",
	}
	for _, h := range wantHeaders {
		if !strings.Contains(output, h) {
			t.Errorf("help output missing group header %q\ngot:\n%s", h, output)
		}
	}

	// Convention group must appear before Campfire Management.
	convIdx := strings.Index(output, "Convention Operations:")
	campfireIdx := strings.Index(output, "Campfire Management:")
	if convIdx >= campfireIdx {
		t.Errorf("expected Convention Operations to appear before Campfire Management in help output")
	}

	// Key commands must appear under the right groups.
	// Note: protocol-primitive commands (send, read, compact, bridge, await,
	// inspect, dm) are hidden from cf's help — they live in cf-primitives.
	checks := []struct {
		group   string
		command string
	}{
		{"Convention Operations:", "convention"},
		{"Convention Operations:", "swarm"},
		// "discover" is hidden — not checked in group listing
		{"Campfire Management:", "join"},
		{"Campfire Management:", "init"},
		{"Campfire Management:", "create"},
		// "Advanced:" — bridge, serve are hidden (primitives or experimental)
		{"Advanced:", "verify"},
		{"Advanced:", "completion"},
	}
	for _, c := range checks {
		groupPos := strings.Index(output, c.group)
		if groupPos < 0 {
			t.Errorf("group header %q not found in output", c.group)
			continue
		}
		// Find the next group header after this one to bound the section.
		sectionEnd := len(output)
		for _, h := range wantHeaders {
			if h == c.group {
				continue
			}
			if idx := strings.Index(output[groupPos+len(c.group):], h); idx >= 0 {
				candidate := groupPos + len(c.group) + idx
				if candidate < sectionEnd {
					sectionEnd = candidate
				}
			}
		}
		section := output[groupPos:sectionEnd]
		if !strings.Contains(section, "  "+c.command) {
			t.Errorf("command %q not found under group %q\nsection:\n%s", c.command, c.group, section)
		}
	}

	// "Additional Commands:" must NOT appear (everything should be grouped).
	if strings.Contains(output, "Additional Commands:") {
		t.Errorf("help output should not have ungrouped 'Additional Commands' section\ngot:\n%s", output)
	}
}
