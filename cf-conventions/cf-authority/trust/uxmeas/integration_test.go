package uxmeas

// integration_test.go — End-to-end integration test for Budget B telemetry.
//
// Design: docs/design/0.30-completion/round-2/approval-flow-ux-measurement.md §1.2
// Tracked bead: campfireagent-951 (veracity follow-up to campfireagent-f00)
//
// Veracity condition (stage-6-veracity-b8a.md §campfireagent-f00):
//
//	"add a Go integration test (e.g., TestApproveCommand_TelemetryEndToEnd) that drives
//	 cf approve programmatically and asserts the uxmeas.jsonl side effect. Verification:
//	 demo runs cf approve (not Python); JSONL is produced by the binary, not by python3 -c."
//
// This test:
//
//  1. Builds the cf binary (or uses CF_BINARY env override).
//  2. Runs cf init in a temp CF_HOME to create an operator identity with an identity
//     campfire (the "~home" campfire that cf approve reads from).
//  3. Sends a delegation:request message with a real JSON payload to the identity
//     campfire via cf send.
//  4. Writes [telemetry] uxmeas = true to $CF_HOME/config.toml.
//  5. Invokes cf approve <msg-id> --accept --telemetry uxmeas.
//  6. Reads $CF_HOME/uxmeas.jsonl, parses every record, and asserts:
//     - At least one non-patch BudgetBRecord is present.
//     - Required fields are non-zero (future_id, surfaced_at, invoked_at, predicate_hash, consumer).
//     - Temporal ordering: T0 (surfaced_at) ≤ T1 (invoked_at) ≤ T2 (landed_at, if present).
//     - predicate_hash is exactly 16 hex characters.
//     - consumer is a known named value ("rd").
//  7. Verifies that a patch record (_patch=true) was appended with a non-zero landed_at
//     that satisfies landed_at ≥ invoked_at.
//
// This test is in the uxmeas package (not uxmeas_test) for direct access to the
// package-level types used for JSONL parsing. It uses os/exec exclusively for
// the cf binary invocations — no campfire protocol packages are imported, so
// there is no circular dependency.
//
// Build requirements: the host machine must have the Go toolchain on PATH (same
// requirement as all other integration tests in this module).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// cfBinaryForUxmeas caches the path to the built cf binary across test runs
// (-count>1).  The binary is placed under os.TempDir() (not t.TempDir()) so it
// outlives any individual test's cleanup.  buildOnce ensures the build runs
// exactly once per process regardless of how many times -count is set.
var (
	cfBinaryForUxmeas string
	buildOnce         sync.Once
)

// buildCFBinaryForUxmeas builds the cf binary into a stable directory under
// os.TempDir() so the path remains valid across multiple -count runs.
func buildCFBinaryForUxmeas(t *testing.T) string {
	t.Helper()
	// Allow an explicit override via env (CI cache, pre-built binary in PATH).
	if override := os.Getenv("CF_BINARY"); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override
		}
	}

	buildOnce.Do(func() {
		// Use os.TempDir() — not t.TempDir() — so the directory is not cleaned
		// up between runs when -count>1.
		dir := filepath.Join(os.TempDir(), "uxmeas-cf-bin")
		if err := os.MkdirAll(dir, 0700); err != nil {
			// Can't use t.Fatalf inside a goroutine/Once; store a sentinel and
			// let the caller detect the missing binary.
			return
		}
		bin := filepath.Join(dir, "cf")
		cmd := exec.Command("go", "build", "-o", bin, "github.com/campfire-net/campfire/cmd/cf")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// Leave cfBinaryForUxmeas empty; the caller will t.Fatalf.
			return
		}
		cfBinaryForUxmeas = bin
	})

	if cfBinaryForUxmeas == "" {
		t.Fatal("buildCFBinaryForUxmeas: go build failed (see stderr above)")
	}
	return cfBinaryForUxmeas
}

// runCFUxmeas executes the cf binary with the given env and args.
// Returns (stdout, stderr, error).
func runCFUxmeas(t *testing.T, cfHome string, args ...string) (string, string, error) {
	t.Helper()
	bin := buildCFBinaryForUxmeas(t)

	cmd := exec.Command(bin, args...)
	cmd.Env = cfEnvForUxmeas(cfHome)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// cfEnvForUxmeas builds a minimal environment that points cf at the given cfHome.
func cfEnvForUxmeas(cfHome string) []string {
	base := os.Environ()
	filtered := make([]string, 0, len(base))
	for _, kv := range base {
		if strings.HasPrefix(kv, "CF_HOME=") ||
			strings.HasPrefix(kv, "CF_BEACON_DIR=") ||
			strings.HasPrefix(kv, "CF_TRANSPORT_DIR=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	return append(filtered, "CF_HOME="+cfHome)
}

// writeUxmeasConfig writes [telemetry] uxmeas = true to $cfHome/config.toml.
// If config.toml already exists its content is replaced to avoid duplicate sections.
func writeUxmeasConfig(t *testing.T, cfHome string) {
	t.Helper()
	configPath := filepath.Join(cfHome, "config.toml")

	// Read existing config (may contain [identity] section from cf init).
	existing, _ := os.ReadFile(configPath)

	// Remove any pre-existing [telemetry] section.
	cleaned := removeTelemetrySection(string(existing))

	// Append the uxmeas enable block.
	content := cleaned + "\n[telemetry]\nuxmeas = true\n"
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatalf("writeUxmeasConfig: %v", err)
	}
}

// removeTelemetrySection strips any [telemetry] ... block from a TOML string.
func removeTelemetrySection(toml string) string {
	var out []string
	inTelemetry := false
	for _, line := range strings.Split(toml, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[telemetry]" {
			inTelemetry = true
			continue
		}
		if inTelemetry && len(trimmed) > 0 && trimmed[0] == '[' {
			inTelemetry = false
		}
		if !inTelemetry {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// TestApproveCommand_TelemetryEndToEnd is the end-to-end veracity test.
//
// It exercises the full path:
//
//	cf init → cf send (delegation:request) → cf approve --accept --telemetry uxmeas
//	→ $CF_HOME/uxmeas.jsonl contains a real BudgetBRecord with correct fields.
func TestApproveCommand_TelemetryEndToEnd(t *testing.T) {
	cfHome := t.TempDir()

	// ── Step 1: initialise operator identity ────────────────────────────────────
	//
	// cf init creates an Ed25519 identity and an identity campfire registered under
	// the "home" alias. The "~home" alias is what cf approve reads to locate the
	// identity campfire.
	t.Log("step 1: cf init — create operator identity")
	initOut, initErr, err := runCFUxmeas(t, cfHome, "init")
	if err != nil {
		t.Fatalf("cf init: %v\nstderr: %s", err, initErr)
	}
	t.Logf("cf init output: %s", strings.TrimSpace(initOut))

	// ── Step 2: locate the identity campfire ID ──────────────────────────────────
	//
	// cf alias list --json returns the alias table; "home" maps to the identity
	// campfire created by cf init.
	t.Log("step 2: resolve ~home alias")
	aliasOut, aliasErr, err := runCFUxmeas(t, cfHome, "alias", "list", "--json")
	if err != nil {
		t.Fatalf("cf alias list: %v\nstderr: %s", err, aliasErr)
	}
	var aliases map[string]string
	if err := json.Unmarshal([]byte(aliasOut), &aliases); err != nil {
		t.Fatalf("parsing alias JSON: %v\nraw: %s", err, aliasOut)
	}
	identityCampfireID, ok := aliases["home"]
	if !ok || identityCampfireID == "" {
		t.Fatalf("home alias not found in alias table: %v", aliases)
	}
	t.Logf("identity campfire: %s", identityCampfireID[:16]+"...")

	// ── Step 3: send a delegation:request message ────────────────────────────────
	//
	// cf approve scans the identity campfire for messages with the "delegation:request"
	// tag and matches by message ID. We send a well-formed delegation:request payload
	// so that approve can parse the failed_predicate and infer the consumer.
	t.Log("step 3: send delegation:request to identity campfire")

	// Fabricate a synthetic future ID for the delegation:request.
	// This is the future that cf approve will "fulfill" by sending a delegation:grant.
	syntheticFutureID := fmt.Sprintf("test-future-%d", time.Now().UnixNano())

	delegReqPayload, err := json.Marshal(map[string]any{
		"agent_pubkey": strings.Repeat("a", 64), // hex ed25519 pubkey (test fixture)
		"future_id":    syntheticFutureID,
		// failed_predicate must have a valid "kind" field for ParsePredicateAST.
		// "grant" is the simplest leaf kind: it requires one convention:op pair.
		// InferConsumer reads the "convention" field at the top level of the map
		// (not from inside the predicate AST), so we include it at root level too.
		"failed_predicate": map[string]any{
			"kind":       "grant",
			"convention": "rd",
			"op":         "claim",
		},
		"requested_scope": "rd:claim",
		"campfire_id":     identityCampfireID,
	})
	if err != nil {
		t.Fatalf("marshaling delegation:request payload: %v", err)
	}

	// Record the wall-clock time just before sending so we can bound surfaced_at.
	beforeSend := time.Now()

	sendOut, sendErr, err := runCFUxmeas(t, cfHome,
		"send", identityCampfireID, string(delegReqPayload),
		"--tag", "delegation:request",
	)
	if err != nil {
		t.Fatalf("cf send (delegation:request): %v\nstderr: %s", err, sendErr)
	}

	msgID := strings.TrimSpace(sendOut)
	if len(msgID) == 0 {
		t.Fatal("cf send returned empty message ID")
	}
	t.Logf("delegation:request message ID: %s", msgID[:min12(msgID)]+"...")

	// ── Step 4: enable uxmeas telemetry in config ────────────────────────────────
	t.Log("step 4: enable telemetry in config.toml")
	writeUxmeasConfig(t, cfHome)

	// ── Step 5: invoke cf approve --accept --telemetry uxmeas ───────────────────
	//
	// --accept suppresses the interactive prompt (non-interactive approval).
	// --telemetry uxmeas forces Budget B recording even if config read fails.
	//
	// We also pass the flag to be extra explicit — this exercises the forceEnabled
	// path in RecordApproval and UpdateLandedAt.
	t.Log("step 5: cf approve --accept --telemetry uxmeas")
	approveOut, approveErr, err := runCFUxmeas(t, cfHome,
		"approve", msgID,
		"--accept",
		"--telemetry", "uxmeas",
	)
	afterApprove := time.Now()
	if err != nil {
		t.Fatalf("cf approve: %v\nstdout: %s\nstderr: %s", err, approveOut, approveErr)
	}
	t.Logf("cf approve output: %s", strings.TrimSpace(approveOut))

	// ── Step 6: parse and assert uxmeas.jsonl ───────────────────────────────────
	t.Log("step 6: verify uxmeas.jsonl")

	logPath := filepath.Join(cfHome, UxmeasLogFile)
	records, patches, err := readUxmeasLog(t, logPath)
	if err != nil {
		t.Fatalf("reading uxmeas.jsonl: %v", err)
	}

	if len(records) == 0 {
		t.Fatalf("uxmeas.jsonl is empty — RecordApproval did not write a record\n"+
			"log path: %s\napprove stdout: %s\napprove stderr: %s",
			logPath, approveOut, approveErr)
	}

	// Find the record matching our synthetic future ID.
	var rec *BudgetBRecord
	for i := range records {
		if records[i].FutureID == syntheticFutureID {
			rec = &records[i]
			break
		}
	}
	if rec == nil {
		t.Fatalf("no BudgetBRecord found with future_id=%q in %s (found %d records)",
			syntheticFutureID, logPath, len(records))
	}

	// ── Assertion A: required fields present and non-zero ──────────────────────
	if rec.FutureID == "" {
		t.Error("future_id: empty")
	}
	if rec.SurfacedAt == 0 {
		t.Error("surfaced_at: zero (not set)")
	}
	if rec.InvokedAt == 0 {
		t.Error("invoked_at: zero (not set)")
	}
	if rec.PredicateHash == "" {
		t.Error("predicate_hash: empty")
	}
	if rec.Consumer == "" {
		t.Error("consumer: empty")
	}

	// ── Assertion B: predicate_hash is exactly 16 hex characters ───────────────
	hexRE := regexp.MustCompile(`^[0-9a-f]{16}$`)
	if !hexRE.MatchString(rec.PredicateHash) {
		t.Errorf("predicate_hash: got %q, want exactly 16 lowercase hex chars", rec.PredicateHash)
	}

	// ── Assertion C: consumer inferred correctly ────────────────────────────────
	if rec.Consumer != "rd" {
		t.Errorf("consumer: got %q, want %q (from failed_predicate convention=rd)", rec.Consumer, "rd")
	}

	// ── Assertion D: temporal ordering T0 ≤ T1 ─────────────────────────────────
	//
	// surfaced_at (T0) ≤ invoked_at (T1).
	// T0 is the message creation time (set by the protocol when cf send ran), so
	// it must be ≥ beforeSend.Unix() - 1 (allow 1s for clock resolution).
	// T1 must be ≤ afterApprove.Unix() + 1.
	if rec.SurfacedAt > rec.InvokedAt {
		t.Errorf("temporal ordering violated: surfaced_at=%d > invoked_at=%d (Budget B would be negative)",
			rec.SurfacedAt, rec.InvokedAt)
	}
	if rec.SurfacedAt < beforeSend.Unix()-2 {
		t.Errorf("surfaced_at=%d is before the send wall-clock %d (by >2s); message timestamp looks stale",
			rec.SurfacedAt, beforeSend.Unix())
	}
	if rec.InvokedAt > afterApprove.Unix()+2 {
		t.Errorf("invoked_at=%d is after the approval returned %d (by >2s); clock skew or wrong timestamp",
			rec.InvokedAt, afterApprove.Unix())
	}

	t.Logf("BudgetBRecord: future_id=%s surfaced_at=%d invoked_at=%d predicate_hash=%s consumer=%s response_time=%.1fs",
		rec.FutureID[:min12(rec.FutureID)],
		rec.SurfacedAt, rec.InvokedAt,
		rec.PredicateHash, rec.Consumer,
		rec.ResponseTimeSeconds(),
	)

	// ── Assertion E: patch record (landed_at) ──────────────────────────────────
	//
	// UpdateLandedAt appends a patch record after Send returns. Verify it exists
	// for this future_id and that landed_at ≥ invoked_at.
	var patch *patchRecord
	for i := range patches {
		if patches[i].FutureID == syntheticFutureID {
			patch = &patches[i]
			break
		}
	}
	if patch == nil {
		t.Errorf("no patch record (_patch=true) found for future_id=%q; UpdateLandedAt may not have fired", syntheticFutureID)
	} else {
		if patch.LandedAt == 0 {
			t.Error("patch record: landed_at is zero")
		}
		if patch.LandedAt < rec.InvokedAt {
			t.Errorf("patch record: landed_at=%d < invoked_at=%d (grant cannot land before approve was invoked)",
				patch.LandedAt, rec.InvokedAt)
		}
		t.Logf("patch record: landed_at=%d (Δ=%ds after invoked_at)",
			patch.LandedAt, patch.LandedAt-rec.InvokedAt)
	}

	t.Log("TestApproveCommand_TelemetryEndToEnd: PASS")
	t.Logf("  uxmeas.jsonl path : %s", logPath)
	t.Logf("  Records written   : %d (+ %d patch records)", len(records), len(patches))
	t.Logf("  Budget B          : %.1fs (threshold: median ≤ 300s, p90 ≤ 900s)",
		rec.ResponseTimeSeconds())
}

// ── helpers ──────────────────────────────────────────────────────────────────

// patchRecord is the internal shape of a _patch=true record in uxmeas.jsonl.
type patchRecord struct {
	FutureID string `json:"future_id"`
	LandedAt int64  `json:"landed_at"`
	Patch    bool   `json:"_patch"`
}

// readUxmeasLog parses the JSONL at logPath and separates full BudgetBRecords
// from patch records. Lines that cannot be parsed as either are reported as
// test errors.
func readUxmeasLog(t *testing.T, logPath string) ([]BudgetBRecord, []patchRecord, error) {
	t.Helper()
	f, err := os.Open(logPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", logPath, err)
	}
	defer f.Close()

	var records []BudgetBRecord
	var patches []patchRecord

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Detect patch records by the _patch sentinel before full decode.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Errorf("uxmeas.jsonl line %d: invalid JSON: %v\nline: %s", lineNum, err, line)
			continue
		}

		if patchFlag, ok := raw["_patch"]; ok {
			var isPatch bool
			if err := json.Unmarshal(patchFlag, &isPatch); err == nil && isPatch {
				var p patchRecord
				if err := json.Unmarshal([]byte(line), &p); err != nil {
					t.Errorf("uxmeas.jsonl line %d: failed to decode patch record: %v", lineNum, err)
					continue
				}
				patches = append(patches, p)
				continue
			}
		}

		var rec BudgetBRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("uxmeas.jsonl line %d: failed to decode BudgetBRecord: %v", lineNum, err)
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scanning %s: %w", logPath, err)
	}
	return records, patches, nil
}

// min12 returns the minimum of 12 and len(s) — used for safe short-ID display.
func min12(s string) int {
	if len(s) < 12 {
		return len(s)
	}
	return 12
}
