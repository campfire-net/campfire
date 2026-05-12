package cmd

// send_test.go — tests for convention:operation payload validation in cf send.
//
// The four behaviors under test:
//   1. Invalid convention:operation payload → error, send blocked.
//   2. Valid convention:operation payload → sends successfully.
//   3. Convention:operation payload with warnings → warning to stderr, send proceeds.
//   4. Non-convention message (no convention:operation tag) → no validation, send proceeds.
//
// Tests 1, 3 validate the lint logic directly. Test 4 goes through the full cobra
// path and is ordered before test 2 to avoid cobra StringSlice flag accumulation
// on the shared global sendCmd. Test 2 calls rootCmd.Execute with --tag and must
// run last among the rootCmd.Execute tests.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// sendTestValidDecl is a minimal valid convention declaration for send tests.
var sendTestValidDecl = func() []byte {
	d := map[string]any{
		"convention":  "test-send-conv",
		"version":     "0.1",
		"operation":   "post",
		"description": "Test send post operation",
		"signing":     "member_key",
	}
	b, _ := json.Marshal(d)
	return b
}()

// sendTestInvalidDecl is a JSON payload missing required fields (version, operation, signing).
const sendTestInvalidDecl = `{"convention":"test"}`

// sendTestDeclWithWarnings is a valid declaration that triggers a conformance
// warning (rate_limit.max above the 100-per-window ceiling).
var sendTestDeclWithWarnings = func() []byte {
	d := map[string]any{
		"convention": "test-send-conv",
		"version":    "0.1",
		"operation":  "post",
		"signing":    "member_key",
		"rate_limit": map[string]any{
			"max":    200,
			"per":    "sender",
			"window": "2m",
		},
	}
	b, _ := json.Marshal(d)
	return b
}()

// runLintForSend mirrors the validation block in sendCmd.RunE: calls
// convention.Lint(payload), returns an error if lint errors exist, and writes
// warnings to os.Stderr (continuing if only warnings).
func runLintForSend(payload []byte) error {
	result := convention.Lint(payload)
	if len(result.Errors) > 0 {
		for _, f := range result.Errors {
			loc := ""
			if f.Field != "" {
				loc = " [" + f.Field + "]"
			}
			fmt.Fprintf(os.Stderr, "error%s: %s\n", loc, f.Message)
		}
		return fmt.Errorf("convention:operation payload is invalid (see errors above)")
	}
	for _, f := range result.Warnings {
		loc := ""
		if f.Field != "" {
			loc = " [" + f.Field + "]"
		}
		fmt.Fprintf(os.Stderr, "warning%s: %s\n", loc, f.Message)
	}
	return nil
}

// captureStderrOutput captures writes to os.Stderr during fn() and returns
// the captured bytes plus any error fn returns.
func captureStderrOutput(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating stderr pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w

	var execErr error
	func() {
		defer func() {
			w.Close()
			os.Stderr = origStderr
		}()
		execErr = fn()
	}()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("reading stderr: %v", err)
	}
	r.Close()
	return buf.String(), execErr
}

// TestSend_ConventionOpInvalidPayload verifies that the validation logic (as
// called by sendCmd.RunE when the convention:operation tag is present) returns
// an error that names missing fields when the payload is invalid.
// Done condition 1: `cf send --tag convention:operation '{"convention":"test"}'`
// fails with error naming missing fields (version, operation, signing).
func TestSend_ConventionOpInvalidPayload(t *testing.T) {
	stderrOut, err := captureStderrOutput(t, func() error {
		return runLintForSend([]byte(sendTestInvalidDecl))
	})

	if err == nil {
		t.Fatal("expected error for invalid convention:operation payload, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error to contain 'invalid', got: %v", err)
	}
	if !strings.Contains(stderrOut, "error") {
		t.Errorf("expected error lines on stderr, got: %q", stderrOut)
	}
	// At least one missing field must be named.
	namedField := false
	for _, field := range []string{"version", "operation", "signing"} {
		if strings.Contains(stderrOut, field) {
			namedField = true
			break
		}
	}
	if !namedField {
		t.Errorf("expected missing field (version/operation/signing) named in stderr, got: %q", stderrOut)
	}
}

// TestSend_ConventionOpWarningsStderrAndProceeds verifies that the validation
// logic prints warnings to stderr but does not return an error when only
// warnings are present.
// Done condition 3: declarations with warnings print warning to stderr but send.
func TestSend_ConventionOpWarningsStderrAndProceeds(t *testing.T) {
	stderrOut, err := captureStderrOutput(t, func() error {
		return runLintForSend(sendTestDeclWithWarnings)
	})

	if err != nil {
		t.Fatalf("expected success (warnings do not block send), got: %v\nstderr: %q", err, stderrOut)
	}
	if !strings.Contains(stderrOut, "warning") {
		t.Errorf("expected warning line on stderr, got: %q", stderrOut)
	}
}

// TestSend_NonConventionTagNoValidation verifies that non-convention messages
// (no convention:operation tag) bypass validation entirely — even when the
// payload would fail convention lint.
// Done condition 4: non-convention messages are unaffected.
// NOTE: This test is ordered before TestSend_ConventionOpValidPayload because
// both call rootCmd.Execute(), and cobra StringSlice flags accumulate across
// Execute() calls on shared commands. The test without --tag must run before
// the test that sets --tag convention:operation.
func TestSend_NonConventionTagNoValidation(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	// sendTestInvalidDecl would fail convention lint, but without the tag no
	// validation runs and the send must succeed.
	rootCmd.SetArgs([]string{"send", campfireID, sendTestInvalidDecl})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected success for non-convention message (no tag), got: %v", err)
	}
}

// TestSend_ConventionOpValidPayload verifies the full cobra send path: a valid
// convention:operation payload passes lint and is accepted by the transport.
// Done condition 2: a valid declaration payload sends successfully.
// NOTE: This test calls rootCmd.Execute() with --tag convention:operation, which
// leaves cobra StringSlice flag state that contaminates subsequent Execute()
// calls without that flag. It must run after TestSend_NonConventionTagNoValidation.
func TestSend_ConventionOpValidPayload(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()
	// Reset --tag and related StringSlice flags after this test so cobra's
	// flag-accumulation behaviour doesn't leak into subsequent Execute() calls.
	// pflag.SliceValue.Replace() bypasses the internal changed-gate, giving a
	// clean empty slice without appending.
	t.Cleanup(func() {
		for _, name := range []string{"tag", "reply-to", "antecedent"} {
			f := sendCmd.Flags().Lookup(name)
			if f == nil {
				continue
			}
			if sv, ok := f.Value.(interface{ Replace([]string) error }); ok {
				sv.Replace(nil) //nolint:errcheck
			}
			f.Changed = false
		}
	})

	stderrOut, err := captureStderrOutput(t, func() error {
		rootCmd.SetArgs([]string{"send", campfireID, string(sendTestValidDecl), "--tag", "convention:operation"})
		return rootCmd.Execute()
	})

	if err != nil {
		t.Fatalf("expected success for valid convention:operation payload, got: %v\nstderr: %q", err, stderrOut)
	}
	if strings.Contains(stderrOut, "error") {
		t.Errorf("expected no errors in stderr, got: %q", stderrOut)
	}
}
