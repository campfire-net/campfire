// Package uxmeas provides telemetry primitives for the Phase 8 Gate 2 UX
// measurement harness.
//
// # Budget B — Owner response time (human-time)
//
// Design: docs/design/0.30-completion/round-2/approval-flow-ux-measurement.md §1.2
// Tracked bead: campfireagent-f00
//
// Budget B measures the wall-clock time from the delegation:request future being
// surfaced on the owner's primary surface (T0 = message.Timestamp, when the agent
// posted the request) to the owner running `cf approve` and the delegation:grant
// landing in the identity campfire (T1 = time.Now() at approve invocation).
//
// Pass threshold: median ≤ 300 s, p90 ≤ 900 s during the operator's declared
// online window (08:00–18:00 local, declared away-time excluded). Measured
// empirically across N ≥ 30 runs spanning ≥ 3 working days and ≥ 2 named
// consumers. Phase 8 Gate 2 reads the log directly on the operator's workstation.
//
// # Privacy
//
// Only timing tuples and a predicate hash are logged. No predicate text, no scope
// strings, no agent identity beyond the predicate_hash, no request payload. The log
// is local and never auto-uploaded. The operator controls the data.
//
// # Opt-in
//
// Telemetry is default-off. Enable it by setting:
//
//	[telemetry]
//	uxmeas = true
//
// in ~/.cf/config.toml. When disabled, RecordApproval is a no-op.
//
// # Log format
//
// Append-only JSONL at ~/.cf/uxmeas.jsonl. Each line is a BudgetBRecord:
//
//	{"future_id":"…","surfaced_at":…,"invoked_at":…,"landed_at":…,"predicate_hash":"…","consumer":"…"}
package uxmeas

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// BudgetBRecord is a single timing observation for Phase 8 Gate 2 Budget B.
//
// Fields are snake_case to match the JSONL log format specified in the design.
// The log file is append-only; one record per `cf approve` invocation.
type BudgetBRecord struct {
	// FutureID is the future message ID from the delegation:request payload.
	// Used to correlate with the Budget A T3 timestamp.
	FutureID string `json:"future_id"`

	// SurfacedAt is Unix seconds when the delegation:request message was created
	// (protocol.Message.Timestamp divided by 1e9 if nanoseconds, or as-is if
	// already seconds). This is T0: when the request first existed in the campfire.
	// The actual surface time (when the owner saw it) is bounded by
	// SurfacedAt + subscribe poll interval, but using the message timestamp
	// gives a conservative lower bound that does not require instrumenting
	// the owner's Subscribe loop.
	SurfacedAt int64 `json:"surfaced_at"`

	// InvokedAt is Unix seconds when the operator ran `cf approve`. This is T1:
	// the moment the owner's decision was recorded. Budget B = InvokedAt - SurfacedAt.
	InvokedAt int64 `json:"invoked_at"`

	// LandedAt is Unix seconds when the delegation:grant Send returned (the grant
	// landed in the identity campfire). Optional bookkeeping — not used in the
	// median computation but useful for diagnosing grant-post latency.
	LandedAt int64 `json:"landed_at,omitempty"`

	// PredicateHash is a SHA-256 hash (hex, first 8 bytes = 16 hex chars) of the
	// delegation:request failed_predicate JSON. Used to deduplicate repeated runs
	// against the same predicate and to identify which predicate categories are slow.
	// Does NOT contain predicate text or scope strings.
	PredicateHash string `json:"predicate_hash"`

	// Consumer is the named portfolio consumer that triggered the approval flow.
	// Derived from the convention field in the failed_predicate when available
	// (e.g. "rd", "dontguess", "social"). Falls back to "unknown" if the predicate
	// carries no recognizable convention tag. Required by design §1.2 to verify
	// that N = 30 spans ≥ 2 distinct named consumers.
	Consumer string `json:"consumer"`
}

// ResponseTimeSeconds returns Budget B = InvokedAt − SurfacedAt in seconds.
func (r BudgetBRecord) ResponseTimeSeconds() float64 {
	return float64(r.InvokedAt - r.SurfacedAt)
}

// logMu serialises concurrent appends (multiple approve invocations in a swarm
// scenario; rare in practice but safe).
var logMu sync.Mutex

// UxmeasLogFile is the default log path: ~/.cf/uxmeas.jsonl.
// Override in tests by passing an explicit path to AppendRecord.
const UxmeasLogFile = "uxmeas.jsonl"

// AppendRecord appends a BudgetBRecord to the telemetry log at logPath.
// logPath is typically filepath.Join(cfHome, UxmeasLogFile).
//
// The file is created with mode 0600 if it does not exist. Writes are atomic
// within a process via logMu; concurrent processes may interleave lines but each
// line is a complete JSON record so readers can handle interleaved writes.
func AppendRecord(logPath string, rec BudgetBRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("uxmeas: marshal record: %w", err)
	}
	line = append(line, '\n')

	logMu.Lock()
	defer logMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return fmt.Errorf("uxmeas: create log dir: %w", err)
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("uxmeas: open log: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("uxmeas: write record: %w", err)
	}
	return nil
}

// RecordApproval is the high-level hook called from cmd/cf/cmd/approve.go
// immediately before the delegation:grant Send call. It builds a BudgetBRecord
// from the approve-command inputs and appends it to the telemetry log.
//
// Parameters:
//   - cfHome: path to the campfire home directory (from CFHome())
//   - futureID: req.FutureID from the delegation:request payload
//   - surfacedAtNano: reqMsg.Timestamp (nanoseconds since epoch) from the
//     delegation:request message — the message creation time, used as T0
//   - invokedAt: time.Now() captured immediately when `cf approve` fires,
//     before any blocking I/O — this is T1
//   - failedPredicate: req.FailedPredicate map for predicate_hash derivation
//     and consumer inference; may be nil
//   - forceEnabled: true when the --telemetry uxmeas CLI flag was passed;
//     bypasses the config-file enable check for a single invocation
//
// When uxmeas telemetry is disabled (the default), RecordApproval is a no-op
// and returns nil. Telemetry errors are non-fatal: RecordApproval logs to stderr
// but does not abort the approve flow.
//
// LandedAt is set to zero here; the caller should call UpdateLandedAt after Send
// succeeds to fill in the final timestamp, or use AppendRecord directly if
// post-Send timing is important.
func RecordApproval(cfHome, futureID string, surfacedAtNano int64, invokedAt time.Time, failedPredicate map[string]any, forceEnabled bool) {
	if !forceEnabled && !IsEnabled(cfHome) {
		return
	}

	// Convert nanosecond message timestamp to seconds. The protocol stores
	// Timestamp as Unix nanoseconds (cf-protocol/protocol/message.go).
	var surfacedAt int64
	if surfacedAtNano > 1e12 {
		// Nanoseconds — divide to get seconds.
		surfacedAt = surfacedAtNano / int64(time.Second)
	} else {
		// Already seconds (legacy or test value).
		surfacedAt = surfacedAtNano
	}

	rec := BudgetBRecord{
		FutureID:      futureID,
		SurfacedAt:    surfacedAt,
		InvokedAt:     invokedAt.Unix(),
		PredicateHash: HashPredicate(failedPredicate),
		Consumer:      InferConsumer(failedPredicate),
	}

	logPath := filepath.Join(cfHome, UxmeasLogFile)
	if err := AppendRecord(logPath, rec); err != nil {
		// Non-fatal: print to stderr and continue. The approve flow must not
		// block on telemetry write failure.
		fmt.Fprintf(os.Stderr, "uxmeas: telemetry write failed (non-fatal): %v\n", err)
	}
}

// UpdateLandedAt appends a patch record to the telemetry log with the LandedAt
// timestamp after the delegation:grant Send returns. This is an optional
// bookkeeping field; the pass/fail gate only uses InvokedAt.
//
// Because the log is append-only, the update is implemented by appending a
// patch record with a _patch sentinel. Phase 7 report generation coalesces
// initial + patch by futureID.
//
// forceEnabled bypasses the config-file enable check for one invocation (used
// when the --telemetry uxmeas CLI flag was passed).
func UpdateLandedAt(cfHome, futureID string, landedAt time.Time, forceEnabled bool) {
	if !forceEnabled && !IsEnabled(cfHome) {
		return
	}

	type patchRecord struct {
		FutureID string `json:"future_id"`
		LandedAt int64  `json:"landed_at"`
		Patch    bool   `json:"_patch"`
	}
	rec := patchRecord{
		FutureID: futureID,
		LandedAt: landedAt.Unix(),
		Patch:    true,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "uxmeas: marshal patch: %v\n", err)
		return
	}
	line = append(line, '\n')

	logMu.Lock()
	defer logMu.Unlock()

	logPath := filepath.Join(cfHome, UxmeasLogFile)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "uxmeas: open log for patch: %v\n", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		fmt.Fprintf(os.Stderr, "uxmeas: write patch: %v\n", err)
	}
}

// IsEnabled reports whether Budget B telemetry is enabled for the given cfHome.
// Reads the [telemetry] uxmeas field from ~/.cf/config.toml (or
// cfHome/config.toml). Default is false (opt-in required).
//
// The TOML config.go cascade does not yet surface a [telemetry] section, so we
// parse only the global config file here with a minimal one-field struct. This
// avoids a dependency on the protocol package's LoadConfig (which would create
// a circular import from uxmeas → protocol → uxmeas in tests). The uxmeas
// package is a leaf: no imports from cf-protocol or cmd.
func IsEnabled(cfHome string) bool {
	type telemetryConfig struct {
		Uxmeas bool `toml:"uxmeas"`
	}
	type rawTelemetry struct {
		Telemetry telemetryConfig `toml:"telemetry"`
	}

	configPath := filepath.Join(cfHome, "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		// File not found or unreadable — telemetry is off.
		return false
	}

	// Minimal TOML parse without importing the full toml package to keep the
	// leaf package dependency-free. We scan for the pattern manually.
	// This is intentionally simple: only "[telemetry]" sections with
	// "uxmeas = true" on the same or next line are recognised.
	//
	// A full toml.Decode would be cleaner but requires BurntSushi/toml as a
	// dependency of the uxmeas package, which is a leaf used in test harnesses.
	// The manual scan is ~20 lines and handles the exact config format we ship.
	return scanUxmeasEnabled(string(data))
}

// scanUxmeasEnabled scans a TOML document for [telemetry] uxmeas = true.
// Returns true iff the line "uxmeas = true" appears after a "[telemetry]" header
// and before the next section header.
func scanUxmeasEnabled(tomlContent string) bool {
	inTelemetry := false
	for _, line := range splitLines(tomlContent) {
		trimmed := trimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		if trimmed == "[telemetry]" {
			inTelemetry = true
			continue
		}
		if len(trimmed) > 0 && trimmed[0] == '[' {
			inTelemetry = false
			continue
		}
		if inTelemetry && trimmed == "uxmeas = true" {
			return true
		}
	}
	return false
}

// HashPredicate returns a 16-hex-char (8-byte) SHA-256 prefix of the
// JSON-serialised failedPredicate map. Returns "0000000000000000" for nil input.
//
// This is the anonymised predicate_hash field in BudgetBRecord. It is used to:
//   - Deduplicate repeated runs against the same predicate gate.
//   - Identify which predicate categories have high human-response latency.
//
// It contains NO predicate text, scope strings, or identity information.
func HashPredicate(failedPredicate map[string]any) string {
	if failedPredicate == nil {
		return "0000000000000000"
	}
	b, err := json.Marshal(failedPredicate)
	if err != nil {
		return "0000000000000000"
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:8]) // 16 hex chars = 8 bytes
}

// InferConsumer derives the named portfolio consumer from the failed_predicate.
// The convention field (if present) maps to a consumer name. Falls back to
// "unknown" when no convention tag is recognizable.
//
// Named consumers (design §1.2 §5): rd, dontguess, social, reach, freeso.
// The mapping is deliberately narrow: unrecognised convention names return
// "unknown" rather than leaking arbitrary strings into the log.
func InferConsumer(failedPredicate map[string]any) string {
	if failedPredicate == nil {
		return "unknown"
	}
	convention, ok := failedPredicate["convention"]
	if !ok {
		return "unknown"
	}
	name, ok := convention.(string)
	if !ok {
		return "unknown"
	}
	switch name {
	case "rd", "ready":
		return "rd"
	case "dontguess":
		return "dontguess"
	case "social":
		return "social"
	case "reach", "thereach":
		return "reach"
	case "freeso":
		return "freeso"
	default:
		return "unknown"
	}
}

// ── stdlib-only helpers to keep uxmeas dependency-free ────────────────────────

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
