package cmd

// Tests for the convention lint CLI output surface (campfire-agent-6v8).
// These tests verify that deprecation warnings for payload_required and
// payload_schema appear in the printed output — not just in the in-process
// LintResult.Warnings struct.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
)

// minimalLintDecl returns a minimal valid declaration payload with the given
// extra fields merged in.
func minimalLintDecl(extras map[string]any) []byte {
	base := map[string]any{
		"convention":  "test-lint",
		"version":     "0.1",
		"operation":   "publish",
		"description": "Test lint operation",
		"signing":     "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "test:publish", "cardinality": "exactly_one"},
		},
	}
	for k, v := range extras {
		base[k] = v
	}
	b, _ := json.Marshal(base)
	return b
}

// TestConventionLint_PayloadRequired_PrintsDeprecationWarning verifies that a
// declaration with payload_required: true causes the lint CLI output surface
// (printLintResult) to emit a "warning" line containing "PayloadRequired" and
// "deprecated". This tests the formatted stdout output, not the struct field.
func TestConventionLint_PayloadRequired_PrintsDeprecationWarning(t *testing.T) {
	payload := minimalLintDecl(map[string]any{"payload_required": true})

	result := convention.Lint(payload)
	if !result.Valid {
		t.Fatalf("expected valid declaration (warnings only), got errors: %v", result.Errors)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected at least one warning for payload_required, got none in LintResult")
	}

	// Capture the CLI-formatted output from printLintResult.
	out := captureStdout(t, func() {
		printLintResult(result)
	})

	if !strings.Contains(out, "warning") {
		t.Errorf("expected 'warning' prefix in output, got:\n%s", out)
	}
	if !strings.Contains(out, "PayloadRequired") {
		t.Errorf("expected 'PayloadRequired' in warning output, got:\n%s", out)
	}
	if !strings.Contains(out, "deprecated") {
		t.Errorf("expected 'deprecated' in warning output, got:\n%s", out)
	}
}

// TestConventionLint_PayloadSchema_PrintsDeprecationWarning verifies that a
// declaration with payload_schema set causes the lint CLI output surface
// (printLintResult) to emit a "warning" line containing "PayloadSchema" and
// "deprecated". This tests the formatted stdout output, not the struct field.
func TestConventionLint_PayloadSchema_PrintsDeprecationWarning(t *testing.T) {
	payload := minimalLintDecl(map[string]any{"payload_schema": "https://example.com/schema.json"})

	result := convention.Lint(payload)
	if !result.Valid {
		t.Fatalf("expected valid declaration (warnings only), got errors: %v", result.Errors)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected at least one warning for payload_schema, got none in LintResult")
	}

	out := captureStdout(t, func() {
		printLintResult(result)
	})

	if !strings.Contains(out, "warning") {
		t.Errorf("expected 'warning' prefix in output, got:\n%s", out)
	}
	if !strings.Contains(out, "PayloadSchema") {
		t.Errorf("expected 'PayloadSchema' in warning output, got:\n%s", out)
	}
	if !strings.Contains(out, "deprecated") {
		t.Errorf("expected 'deprecated' in warning output, got:\n%s", out)
	}
}

// TestConventionLint_PayloadRequired_ViaFile verifies that when the lint
// command reads a declaration file containing payload_required: true, the
// warning appears in stdout. This exercises runConventionLint end-to-end
// (file I/O → Lint → printLintResult) without calling os.Exit by invoking
// the output-formatting path directly after reading the file, matching how
// the CLI surfaces the warning to the user.
func TestConventionLint_PayloadRequired_ViaFile(t *testing.T) {
	payload := minimalLintDecl(map[string]any{"payload_required": true})

	// Write declaration to a temp file, simulating the file-read path.
	dir := t.TempDir()
	declFile := filepath.Join(dir, "decl.json")
	if err := os.WriteFile(declFile, payload, 0644); err != nil {
		t.Fatalf("writing decl file: %v", err)
	}

	// Read it back via readDeclarationInput (the same function the CLI uses).
	read, err := readDeclarationInput(declFile)
	if err != nil {
		t.Fatalf("readDeclarationInput: %v", err)
	}

	result := convention.Lint(read)
	if !result.Valid {
		t.Fatalf("expected valid (warnings only), got errors: %v", result.Errors)
	}

	out := captureStdout(t, func() {
		printLintResult(result)
	})

	if !strings.Contains(out, "warning") {
		t.Errorf("expected 'warning' in file-path output, got:\n%s", out)
	}
	if !strings.Contains(out, "PayloadRequired") {
		t.Errorf("expected 'PayloadRequired' in file-path output, got:\n%s", out)
	}
}
