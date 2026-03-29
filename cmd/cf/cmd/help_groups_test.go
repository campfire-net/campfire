package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestHelpGroupsPresent verifies that cf --help renders all four group headers
// and that key commands appear under the correct groups.
func TestHelpGroupsPresent(t *testing.T) {
	assignCommandGroups()
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

	// All four group headers must appear.
	wantHeaders := []string{
		"Convention Operations:",
		"Campfire Management:",
		"Messages:",
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
	checks := []struct {
		group   string
		command string
	}{
		{"Convention Operations:", "convention"},
		{"Convention Operations:", "swarm"},
		{"Convention Operations:", "discover"},
		{"Campfire Management:", "join"},
		{"Campfire Management:", "init"},
		{"Campfire Management:", "create"},
		{"Messages:", "send"},
		{"Messages:", "read"},
		{"Messages:", "compact"},
		{"Advanced:", "bridge"},
		{"Advanced:", "serve"},
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
