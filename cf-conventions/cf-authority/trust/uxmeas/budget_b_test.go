package uxmeas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHashPredicate verifies the predicate hash function.
func TestHashPredicate(t *testing.T) {
	// Nil → sentinel.
	if got := HashPredicate(nil); got != "0000000000000000" {
		t.Errorf("nil predicate: got %q, want %q", got, "0000000000000000")
	}

	// Non-nil → 16 hex chars.
	pred := map[string]any{"kind": "grant", "convention": "rd", "op": "claim"}
	h := HashPredicate(pred)
	if len(h) != 16 {
		t.Errorf("hash length: got %d, want 16", len(h))
	}

	// Deterministic.
	if h2 := HashPredicate(pred); h != h2 {
		t.Errorf("non-deterministic: %q != %q", h, h2)
	}

	// Different predicates → different hashes (with overwhelming probability).
	pred2 := map[string]any{"kind": "grant", "convention": "dontguess", "op": "buy"}
	if h3 := HashPredicate(pred2); h == h3 {
		t.Errorf("collision on distinct predicates: %q == %q", h, h3)
	}
}

// TestInferConsumer verifies the named-consumer mapping.
func TestInferConsumer(t *testing.T) {
	cases := []struct {
		predicate map[string]any
		want      string
	}{
		{nil, "unknown"},
		{map[string]any{}, "unknown"},
		{map[string]any{"convention": "rd"}, "rd"},
		{map[string]any{"convention": "ready"}, "rd"},
		{map[string]any{"convention": "dontguess"}, "dontguess"},
		{map[string]any{"convention": "social"}, "social"},
		{map[string]any{"convention": "reach"}, "reach"},
		{map[string]any{"convention": "thereach"}, "reach"},
		{map[string]any{"convention": "freeso"}, "freeso"},
		{map[string]any{"convention": "unknown-app"}, "unknown"},
		{map[string]any{"kind": "grant"}, "unknown"},
	}
	for _, tc := range cases {
		if got := InferConsumer(tc.predicate); got != tc.want {
			t.Errorf("InferConsumer(%v) = %q, want %q", tc.predicate, got, tc.want)
		}
	}
}

// TestScanUxmeasEnabled verifies the TOML scanner.
func TestScanUxmeasEnabled(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"no telemetry section", "[identity]\ndisplay_name = \"Baron\"\n", false},
		{"telemetry off", "[telemetry]\nuxmeas = false\n", false},
		{"telemetry on", "[telemetry]\nuxmeas = true\n", true},
		{"telemetry on with comment", "[telemetry]\n# enable uxmeas\nuxmeas = true\n", true},
		{"telemetry on with preceding section", "[identity]\ndisplay_name = \"x\"\n[telemetry]\nuxmeas = true\n", true},
		{"telemetry followed by section", "[telemetry]\nuxmeas = true\n[identity]\n", true},
		{"uxmeas after next section", "[telemetry]\n[identity]\nuxmeas = true\n", false},
	}
	for _, tc := range cases {
		if got := scanUxmeasEnabled(tc.content); got != tc.want {
			t.Errorf("%s: scanUxmeasEnabled = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIsEnabled verifies that IsEnabled reads the config file correctly.
func TestIsEnabled(t *testing.T) {
	cfHome := t.TempDir()

	// No config file → disabled.
	if IsEnabled(cfHome) {
		t.Error("no config: expected disabled")
	}

	// Config with telemetry off.
	writeConfig(t, cfHome, "[telemetry]\nuxmeas = false\n")
	if IsEnabled(cfHome) {
		t.Error("uxmeas=false: expected disabled")
	}

	// Config with telemetry on.
	writeConfig(t, cfHome, "[telemetry]\nuxmeas = true\n")
	if !IsEnabled(cfHome) {
		t.Error("uxmeas=true: expected enabled")
	}
}

// TestAppendRecord verifies the log file creation and append semantics.
func TestAppendRecord(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "uxmeas.jsonl")

	rec1 := BudgetBRecord{
		FutureID:      "future-001",
		SurfacedAt:    1000,
		InvokedAt:     1120,
		PredicateHash: "abcd1234abcd1234",
		Consumer:      "rd",
	}
	rec2 := BudgetBRecord{
		FutureID:      "future-002",
		SurfacedAt:    2000,
		InvokedAt:     2450,
		PredicateHash: "efgh5678efgh5678",
		Consumer:      "dontguess",
	}

	if err := AppendRecord(logPath, rec1); err != nil {
		t.Fatalf("append rec1: %v", err)
	}
	if err := AppendRecord(logPath, rec2); err != nil {
		t.Fatalf("append rec2: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	lines := splitNonEmpty(data)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var r1 BudgetBRecord
	if err := json.Unmarshal(lines[0], &r1); err != nil {
		t.Fatalf("parse line 1: %v", err)
	}
	if r1.FutureID != rec1.FutureID {
		t.Errorf("line 1 future_id: got %q, want %q", r1.FutureID, rec1.FutureID)
	}

	var r2 BudgetBRecord
	if err := json.Unmarshal(lines[1], &r2); err != nil {
		t.Fatalf("parse line 2: %v", err)
	}
	if r2.Consumer != "dontguess" {
		t.Errorf("line 2 consumer: got %q, want %q", r2.Consumer, "dontguess")
	}
}

// TestResponseTimeSeconds verifies Budget B computation.
func TestResponseTimeSeconds(t *testing.T) {
	rec := BudgetBRecord{SurfacedAt: 1000, InvokedAt: 1120}
	if got := rec.ResponseTimeSeconds(); got != 120.0 {
		t.Errorf("ResponseTimeSeconds: got %.1f, want 120.0", got)
	}
}

// TestRecordApproval_Disabled verifies no-op when telemetry is off.
func TestRecordApproval_Disabled(t *testing.T) {
	cfHome := t.TempDir()
	// No config → disabled.
	logPath := filepath.Join(cfHome, UxmeasLogFile)
	RecordApproval(cfHome, "future-x", time.Now().UnixNano(), time.Now(), nil, false)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Error("log file should not be created when telemetry is disabled")
	}
}

// TestRecordApproval_Enabled verifies that an enabled config causes a record to land.
func TestRecordApproval_Enabled(t *testing.T) {
	cfHome := t.TempDir()
	writeConfig(t, cfHome, "[telemetry]\nuxmeas = true\n")

	invokedAt := time.Unix(1_700_000_200, 0)
	surfacedNano := int64(1_700_000_000) * int64(time.Second) // nanoseconds
	pred := map[string]any{"convention": "rd", "op": "claim"}

	RecordApproval(cfHome, "future-abc", surfacedNano, invokedAt, pred, false)

	logPath := filepath.Join(cfHome, UxmeasLogFile)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	lines := splitNonEmpty(data)
	if len(lines) != 1 {
		t.Fatalf("expected 1 record, got %d", len(lines))
	}

	var rec BudgetBRecord
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("parse record: %v", err)
	}

	if rec.FutureID != "future-abc" {
		t.Errorf("future_id: got %q, want %q", rec.FutureID, "future-abc")
	}
	if rec.Consumer != "rd" {
		t.Errorf("consumer: got %q, want %q", rec.Consumer, "rd")
	}
	if rec.SurfacedAt != 1_700_000_000 {
		t.Errorf("surfaced_at: got %d, want %d", rec.SurfacedAt, 1_700_000_000)
	}
	if rec.InvokedAt != invokedAt.Unix() {
		t.Errorf("invoked_at: got %d, want %d", rec.InvokedAt, invokedAt.Unix())
	}
	// Budget B = 200s — within the ≤300s median threshold.
	if got := rec.ResponseTimeSeconds(); got != 200.0 {
		t.Errorf("response time: got %.1f, want 200.0", got)
	}
}

// TestRecordApproval_ForceEnabled verifies that forceEnabled=true records even with no config.
func TestRecordApproval_ForceEnabled(t *testing.T) {
	cfHome := t.TempDir()
	// No config file — but forceEnabled=true should still record.
	invokedAt := time.Unix(1_700_000_300, 0)
	surfacedNano := int64(1_700_000_000) * int64(time.Second)
	pred := map[string]any{"convention": "dontguess"}

	RecordApproval(cfHome, "future-force", surfacedNano, invokedAt, pred, true)

	logPath := filepath.Join(cfHome, UxmeasLogFile)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("log not written: %v", err)
	}
	lines := splitNonEmpty(data)
	if len(lines) != 1 {
		t.Fatalf("expected 1 record, got %d", len(lines))
	}
	var rec BudgetBRecord
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rec.Consumer != "dontguess" {
		t.Errorf("consumer: got %q, want dontguess", rec.Consumer)
	}
	if rec.FutureID != "future-force" {
		t.Errorf("future_id: got %q, want future-force", rec.FutureID)
	}
}

// TestUpdateLandedAt_Disabled verifies no-op when telemetry is off.
func TestUpdateLandedAt_Disabled(t *testing.T) {
	cfHome := t.TempDir()
	logPath := filepath.Join(cfHome, UxmeasLogFile)
	UpdateLandedAt(cfHome, "future-x", time.Now(), false)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Error("log file should not be created when telemetry is disabled")
	}
}

// TestUpdateLandedAt_Enabled verifies that a patch record is appended.
func TestUpdateLandedAt_Enabled(t *testing.T) {
	cfHome := t.TempDir()
	writeConfig(t, cfHome, "[telemetry]\nuxmeas = true\n")

	// Write an initial record first (simulating RecordApproval).
	logPath := filepath.Join(cfHome, UxmeasLogFile)
	rec := BudgetBRecord{FutureID: "future-xyz", SurfacedAt: 100, InvokedAt: 200, Consumer: "rd"}
	if err := AppendRecord(logPath, rec); err != nil {
		t.Fatalf("initial append: %v", err)
	}

	landedAt := time.Unix(210, 0)
	UpdateLandedAt(cfHome, "future-xyz", landedAt, false)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	lines := splitNonEmpty(data)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (initial + patch), got %d", len(lines))
	}

	// Second line should be the patch record.
	type patchRec struct {
		FutureID string `json:"future_id"`
		LandedAt int64  `json:"landed_at"`
		Patch    bool   `json:"_patch"`
	}
	var patch patchRec
	if err := json.Unmarshal(lines[1], &patch); err != nil {
		t.Fatalf("parse patch: %v", err)
	}
	if !patch.Patch {
		t.Error("expected _patch=true on patch record")
	}
	if patch.LandedAt != landedAt.Unix() {
		t.Errorf("landed_at: got %d, want %d", patch.LandedAt, landedAt.Unix())
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeConfig(t *testing.T, cfHome, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cfHome, "config.toml"), []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func splitNonEmpty(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
