package cmd

// Tests for sanitizePayload (formatter.go).
// workspace-pm9m.5.10: SWEEP-TEST — sanitizePayload terminal injection prevention has no unit tests.

import (
	"testing"
)

// TestSanitizePayload_ESCStripped verifies that the ESC byte (0x1B) is removed.
func TestSanitizePayload_ESCStripped(t *testing.T) {
	input := []byte{0x1B, '[', '3', '1', 'm', 'H', 'i', 0x1B, '[', '0', 'm'}
	got := sanitizePayload(input)
	for _, b := range []byte(got) {
		if b == 0x1B {
			t.Errorf("sanitizePayload output contains ESC byte (0x1B); got %q", got)
		}
	}
}

// TestSanitizePayload_ControlCharsStripped verifies that control characters
// below 0x20 (other than tab, LF, CR) are stripped.
func TestSanitizePayload_ControlCharsStripped(t *testing.T) {
	// Build input with every byte from 0x00 to 0x1F.
	input := make([]byte, 0x20)
	for i := range input {
		input[i] = byte(i)
	}
	got := sanitizePayload(input)

	// Only tab (0x09), LF (0x0A), and CR (0x0D) should survive.
	for _, b := range []byte(got) {
		if b != 0x09 && b != 0x0A && b != 0x0D {
			t.Errorf("sanitizePayload kept unexpected control byte 0x%02X in output %q", b, got)
		}
	}

	// Tab, LF, and CR must be present (they were in the input).
	if !containsByte([]byte(got), 0x09) {
		t.Error("sanitizePayload stripped tab (0x09); should preserve it")
	}
	if !containsByte([]byte(got), 0x0A) {
		t.Error("sanitizePayload stripped LF (0x0A); should preserve it")
	}
	if !containsByte([]byte(got), 0x0D) {
		t.Error("sanitizePayload stripped CR (0x0D); should preserve it")
	}
}

// TestSanitizePayload_PrintableASCIIPreserved verifies printable ASCII (0x20-0x7E) passes through unchanged.
func TestSanitizePayload_PrintableASCIIPreserved(t *testing.T) {
	input := make([]byte, 0x7F-0x20)
	for i := range input {
		input[i] = byte(0x20 + i)
	}
	got := sanitizePayload(input)
	if got != string(input) {
		t.Errorf("sanitizePayload altered printable ASCII; want %q, got %q", string(input), got)
	}
}

// TestSanitizePayload_HighBytesPreserved verifies bytes 0x80 and above pass through (UTF-8 support).
func TestSanitizePayload_HighBytesPreserved(t *testing.T) {
	// A UTF-8 encoded string: "café" — contains bytes above 0x7F.
	input := []byte("café")
	got := sanitizePayload(input)
	if got != "café" {
		t.Errorf("sanitizePayload corrupted high bytes; want %q, got %q", "café", got)
	}
}

// TestSanitizePayload_MixedInput verifies a realistic mixed payload: normal text
// with an embedded ANSI escape sequence is sanitized correctly.
func TestSanitizePayload_MixedInput(t *testing.T) {
	// "\x1B[31mRED\x1B[0m normal"
	input := []byte{0x1B, '[', '3', '1', 'm', 'R', 'E', 'D', 0x1B, '[', '0', 'm', ' ', 'n', 'o', 'r', 'm', 'a', 'l'}
	got := sanitizePayload(input)

	// ESC bytes must be gone.
	for _, b := range []byte(got) {
		if b == 0x1B {
			t.Errorf("ESC byte found in output %q", got)
		}
	}
	// Printable characters should remain.
	if got != "[31mRED[0m normal" {
		t.Errorf("unexpected sanitized output; got %q", got)
	}
}

// TestSanitizePayload_EmptyInput verifies empty input returns empty string.
func TestSanitizePayload_EmptyInput(t *testing.T) {
	got := sanitizePayload([]byte{})
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// TestSanitizePayload_NilInput verifies nil input returns empty string.
func TestSanitizePayload_NilInput(t *testing.T) {
	got := sanitizePayload(nil)
	if got != "" {
		t.Errorf("expected empty string for nil input, got %q", got)
	}
}

// containsByte is a test helper that reports whether b is in s.
func containsByte(s []byte, b byte) bool {
	for _, v := range s {
		if v == b {
			return true
		}
	}
	return false
}
