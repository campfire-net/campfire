package protocol

// coverage_gap_f22_test.go — Internal tests for two unexported config functions
// (campfire-f22: items 68d and 95b).
//
// These are internal package tests (package protocol, not protocol_test) so they
// can call the unexported resolveRelativePath and decodeAnchor directly.
//
// Covered:
//   5d (68d) — resolveRelativePath returns absolute path unchanged
//   5e (95b) — decodeAnchor rejects odd-length hex string

import (
	"testing"
)

// TestResolveRelativePath_AbsolutePathPassthrough verifies that resolveRelativePath
// returns an absolute path unchanged, regardless of the configDir argument.
// The absolute-path branch is: `if filepath.IsAbs(value) { return value }`.
func TestResolveRelativePath_AbsolutePathPassthrough(t *testing.T) {
	absPath := "/absolute/path/to/file"
	configDir := "/some/config/dir"

	got := resolveRelativePath(absPath, configDir)
	if got != absPath {
		t.Errorf("resolveRelativePath(%q, %q) = %q, want %q", absPath, configDir, got, absPath)
	}
}

// TestDecodeAnchor_OddLengthHex verifies that decodeAnchor returns an error when
// given an odd-length hex string (e.g. "abc"). hex.DecodeString requires an even
// number of characters; odd-length input is rejected at the hex-decode step.
func TestDecodeAnchor_OddLengthHex(t *testing.T) {
	oddHex := "abc" // 3 chars — all hex chars, so isHexString returns true, but hex.DecodeString fails
	_, err := decodeAnchor(oddHex)
	if err == nil {
		t.Errorf("decodeAnchor(%q): expected error for odd-length hex, got nil", oddHex)
	}
}
