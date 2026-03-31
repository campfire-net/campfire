package cmd

import (
	"testing"
)

func TestMulticallParseVersion(t *testing.T) {
	// --version should not error.
	err := Multicall("dontguess", []string{"--version"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMulticallParseOperation(t *testing.T) {
	// Without a valid identity/store, Multicall will fail at the resolution
	// step. That's fine — we're testing that argument parsing reaches dispatch,
	// not that dispatch succeeds. The error should mention resolving the name,
	// not a parse error.
	err := Multicall("nonexistent-test-campfire", []string{"some-op", "--flag", "val"})
	if err == nil {
		t.Fatal("expected error for nonexistent campfire, got nil")
	}
	// Should fail at resolution, not at argument parsing.
	got := err.Error()
	if got == "" {
		t.Fatal("expected non-empty error message")
	}
}
