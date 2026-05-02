//go:build uxmeas

// Package uxmeas_test is the Phase 8 Gate 2 Budget A measurement harness.
//
// Design: docs/design/0.30-completion/round-2/approval-flow-ux-measurement.md §1.1
// Tracked bead: campfireagent-461
//
// # Budget A — Agent→inbox latency (machine-time)
//
// Wall-clock elapsed from an agent synthesizing a delegation:request to that
// message being readable on the owner's primary surface (Subscribe delivery on
// the identity campfire).
//
// Pass threshold: p95 ≤ 5000 ms, p99 ≤ 8000 ms.
// Sample size: N = 100 runs (FS transport). The HTTP transport path is
// TestBudgetA_AgentToInboxLatency_HTTP in budget_a_http_test.go
// (campfireagent-8f0c). Both paths pass the same thresholds.
//
// Four timestamps per run:
//
//	T0 — immediately before client.Send(delegation:request)
//	T1 — immediately after client.Send returns (send-ack)
//	T2 — Subscribe delivers the message to the owner's surface
//	T3 — (FS harness only) same as T2; MCP surface not present in unit harness
//
// T_total = T2 − T0 (the gate-relevant round-trip for the FS surface).
//
// Sub-budgets (diagnostic, not pass/fail gates):
//   - Synthesis: T1−T0 ≤ 100 ms p95
//   - Transport propagation: T2−T1 ≤ 2500 ms p95 (FS)
//
// CI: 95% bootstrap CI on p95. If CI half-width > 500 ms, MORE_SAMPLES is set.
//
// # Running
//
//	go test -tags uxmeas -v ./cf-conventions/cf-authority/trust/uxmeas/...
//	go test -tags uxmeas -v -count=1 -timeout=10m ./cf-conventions/cf-authority/trust/uxmeas/...
package uxmeas_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// N is the sample size per Budget A run.
const N = 100

// budgetAP95Threshold is the pass threshold for p95 total latency (ms).
const budgetAP95Threshold = 5000

// budgetAP99Threshold is the pass threshold for p99 total latency (ms).
const budgetAP99Threshold = 8000

// budgetACIHalfWidth is the maximum acceptable 95% CI half-width on p95 (ms).
const budgetACIHalfWidth = 500

// synthesisP95Budget is the diagnostic sub-budget for T1−T0 synthesis time (ms).
const synthesisP95Budget = 100

// propagationP95Budget is the diagnostic sub-budget for T2−T1 FS propagation (ms).
const propagationP95Budget = 2500

// bootstrapResamples is the number of bootstrap resamples for CI estimation.
const bootstrapResamples = 10_000

// timingRecord captures the four timestamps for one approval flow run.
type timingRecord struct {
	T0          time.Time // before Send(delegation:request)
	T1          time.Time // after Send returns (send-ack)
	T2          time.Time // Subscribe delivers the message to owner surface
	TotalMS     float64   // T2 − T0 in milliseconds
	SynthesisMS float64   // T1 − T0 in milliseconds
	PropagateMS float64   // T2 − T1 in milliseconds
}

// delegationRequestPayload mirrors the wire format in cmd/cf/cmd/approve.go.
// Duplicated here to keep the harness self-contained (no import of cmd package).
type delegationRequestPayload struct {
	AgentPubkey     string         `json:"agent_pubkey"`
	FutureID        string         `json:"future_id"`
	FailedPredicate map[string]any `json:"failed_predicate"`
	RequestedScope  string         `json:"requested_scope"`
	CampfireID      string         `json:"campfire_id"`
}

// TestBudgetA_AgentToInboxLatency_FS runs N=100 approval-flow timing samples
// against an ephemeral FS-transport campfire and asserts Budget A thresholds.
//
// This is the Phase 8 Gate 2 automated instrument for the FS transport path.
// The HTTP transport path is TestBudgetA_AgentToInboxLatency_HTTP in
// budget_a_http_test.go (campfireagent-8f0c), which runs against a local
// in-process cf-mcp HTTP server (no prod credentials required).
func TestBudgetA_AgentToInboxLatency_FS(t *testing.T) {
	transportDir := t.TempDir()

	// ── Agent identity (the side that posts delegation:request) ──
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}
	agentStore, err := openTempStore(t)
	if err != nil {
		t.Fatalf("opening agent store: %v", err)
	}

	// ── Owner identity (the side that receives via Subscribe) ──
	ownerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating owner identity: %v", err)
	}
	ownerStore, err := openTempStore(t)
	if err != nil {
		t.Fatalf("opening owner store: %v", err)
	}

	// ── Identity campfire (the owner's inbox for delegation:request) ──
	// Both identities are members so the agent can post and the owner can read.
	identityCampfireID := setupSharedCampfire(t, transportDir,
		agentID, agentStore,
		ownerID, ownerStore,
	)

	agentClient := protocol.New(agentStore, agentID)
	ownerClient := protocol.New(ownerStore, ownerID)

	// ── Run N samples ──
	samples := make([]timingRecord, 0, N)

	for i := 0; i < N; i++ {
		rec, err := runOneApprovalFlow(t, agentClient, ownerClient, agentID, identityCampfireID, i)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		samples = append(samples, rec)
	}

	// ── Compute statistics ──
	totalMS := extractField(samples, func(r timingRecord) float64 { return r.TotalMS })
	synthMS := extractField(samples, func(r timingRecord) float64 { return r.SynthesisMS })
	propMS := extractField(samples, func(r timingRecord) float64 { return r.PropagateMS })

	p50total := percentile(totalMS, 50)
	p95total := percentile(totalMS, 95)
	p99total := percentile(totalMS, 99)
	p95synth := percentile(synthMS, 95)
	p95prop := percentile(propMS, 95)

	ciLow, ciHigh := bootstrapP95CI(totalMS, bootstrapResamples)
	ciHalfWidth := (ciHigh - ciLow) / 2.0

	// ── Report ──
	t.Logf("Budget A (FS transport) — N=%d", N)
	t.Logf("  Total latency (T2−T0):")
	t.Logf("    p50  = %.1f ms", p50total)
	t.Logf("    p95  = %.1f ms  (threshold: ≤%d ms)", p95total, budgetAP95Threshold)
	t.Logf("    p99  = %.1f ms  (threshold: ≤%d ms)", p99total, budgetAP99Threshold)
	t.Logf("    95%% CI on p95: [%.1f, %.1f] (half-width %.1f ms)", ciLow, ciHigh, ciHalfWidth)
	t.Logf("  Sub-budgets (diagnostic only):")
	t.Logf("    Synthesis (T1−T0) p95  = %.1f ms  (budget: ≤%d ms)", p95synth, synthesisP95Budget)
	t.Logf("    Propagation (T2−T1) p95 = %.1f ms  (budget: ≤%d ms)", p95prop, propagationP95Budget)

	// Emit machine-readable JSON to stdout for Phase 7 report generation.
	emitBudgetAJSON(t, N, samples, p50total, p95total, p99total, ciLow, ciHigh, ciHalfWidth)

	// ── Pass/fail assertions ──
	if ciHalfWidth > budgetACIHalfWidth {
		t.Logf("WARNING: 95%% CI half-width %.1f ms > %d ms — consider running more samples", ciHalfWidth, budgetACIHalfWidth)
		// Not a hard failure — the harness signals MORE_SAMPLES needed.
	}

	if p95total > budgetAP95Threshold {
		t.Errorf("FAIL: p95 total latency %.1f ms exceeds Budget A threshold %d ms", p95total, budgetAP95Threshold)
	}

	if p99total > budgetAP99Threshold {
		t.Errorf("FAIL: p99 total latency %.1f ms exceeds Budget A threshold %d ms", p99total, budgetAP99Threshold)
	}

	if t.Failed() {
		t.Log("Budget A FAILED — halt Phase 8 per design §1.3")
	} else {
		t.Log("Budget A PASSED — FS transport within latency envelope")
	}
}

// runOneApprovalFlow executes a single delegation:request → owner-Subscribe-delivery
// cycle and returns the timing record.
//
// The flow:
//  1. Construct delegation:request JSON payload (simulating gate executor synthesis).
//  2. T0 = time.Now() — start of synthesis (before Send).
//  3. Send the delegation:request message to the identity campfire.
//  4. T1 = time.Now() — send-ack (Send returned).
//  5. Owner's Subscribe detects the new message.
//  6. T2 = time.Now() at delivery — T2 is set inside the subscribe loop.
//
// The Subscribe context has a 10s per-run timeout. A timeout is a hard test failure.
func runOneApprovalFlow(
	t *testing.T,
	agentClient *protocol.Client,
	ownerClient *protocol.Client,
	agentID *identity.Identity,
	identityCampfireID string,
	runIdx int,
) (timingRecord, error) {
	t.Helper()

	// Build delegation:request payload (matches wire format in approve.go).
	reqPayload := delegationRequestPayload{
		AgentPubkey:     agentID.PublicKeyHex(),
		FutureID:        fmt.Sprintf("future-%d-%d", runIdx, time.Now().UnixNano()),
		FailedPredicate: map[string]any{"kind": "grant", "convention": "rd", "op": "claim"},
		RequestedScope:  "rd:*",
		CampfireID:      identityCampfireID,
	}
	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return timingRecord{}, fmt.Errorf("marshal payload: %w", err)
	}

	// Start Subscribe on the owner side BEFORE sending (to catch the message
	// as it arrives rather than polling after the fact).
	subCtx, subCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer subCancel()

	// We filter only delegation:request tags so the owner's Subscribe isn't
	// flooded by other messages from prior runs.
	ownerSub := ownerClient.Subscribe(subCtx, protocol.SubscribeRequest{
		CampfireID:  identityCampfireID,
		Tags:        []string{"delegation:request"},
		PollInterval: 50 * time.Millisecond, // tight poll for timing accuracy
	})
	// Drain any pre-existing messages (from earlier runs) before we record T0.
	// Strategy: mark T0, send, then only count the message with our FutureID.

	// T0: start of synthesis (immediately before Send).
	t0 := time.Now()

	// Send the delegation:request.
	_, err = agentClient.Send(protocol.SendRequest{
		CampfireID: identityCampfireID,
		Payload:    payloadBytes,
		Tags:       []string{"delegation:request"},
	})
	if err != nil {
		return timingRecord{}, fmt.Errorf("agent Send: %w", err)
	}

	// T1: send-ack (Send returned).
	t1 := time.Now()

	// Wait for the owner's Subscribe to deliver THIS specific message.
	var t2 time.Time
	futureID := reqPayload.FutureID
found:
	for {
		select {
		case msg, ok := <-ownerSub.Messages():
			if !ok {
				// Subscription closed — check for error.
				if err := ownerSub.Err(); err != nil {
					return timingRecord{}, fmt.Errorf("owner Subscribe error: %w", err)
				}
				return timingRecord{}, fmt.Errorf("owner Subscribe closed unexpectedly")
			}
			// Parse the payload to match on future_id (unique per run).
			var delivered delegationRequestPayload
			if jsonErr := json.Unmarshal(msg.Payload, &delivered); jsonErr != nil {
				// Not our message format — skip.
				continue
			}
			if delivered.FutureID == futureID {
				t2 = time.Now()
				break found
			}
		case <-subCtx.Done():
			return timingRecord{}, fmt.Errorf("timeout waiting for message delivery (run %d, futureID %s)", runIdx, futureID)
		}
	}

	rec := timingRecord{
		T0:          t0,
		T1:          t1,
		T2:          t2,
		TotalMS:     float64(t2.Sub(t0).Microseconds()) / 1000.0,
		SynthesisMS: float64(t1.Sub(t0).Microseconds()) / 1000.0,
		PropagateMS: float64(t2.Sub(t1).Microseconds()) / 1000.0,
	}
	return rec, nil
}

// ── Setup helpers ──────────────────────────────────────────────────────────

// openTempStore opens a SQLite store in a fresh temp dir.
func openTempStore(t *testing.T) (store.Store, error) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { s.Close() })
	return s, nil
}

// setupSharedCampfire creates a filesystem campfire that both agent and owner
// are members of, wiring it into both stores. Returns the campfire ID.
func setupSharedCampfire(
	t *testing.T,
	transportBaseDir string,
	agentID *identity.Identity, agentStore store.Store,
	ownerID *identity.Identity, ownerStore store.Store,
) string {
	t.Helper()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	tr := fs.New(transportBaseDir)

	// Add agent as full member.
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing agent member: %v", err)
	}
	if err := agentStore.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding agent membership: %v", err)
	}

	// Add owner as full member.
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: ownerID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing owner member: %v", err)
	}
	if err := ownerStore.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding owner membership: %v", err)
	}

	return campfireID
}

// ── Statistics helpers ─────────────────────────────────────────────────────

// extractField pulls a scalar field out of each timing record.
func extractField(samples []timingRecord, fn func(timingRecord) float64) []float64 {
	out := make([]float64, len(samples))
	for i, s := range samples {
		out[i] = fn(s)
	}
	return out
}

// percentile returns the p-th percentile (0–100) of the sorted slice.
func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)

	idx := (p / 100.0) * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// bootstrapP95CI computes a 95% bootstrap confidence interval on the p95 of vals.
// Returns (low, high) — the 2.5th and 97.5th percentiles of the bootstrap distribution.
func bootstrapP95CI(vals []float64, resamples int) (low, high float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	n := len(vals)
	p95s := make([]float64, resamples)
	rng := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec
	for i := 0; i < resamples; i++ {
		sample := make([]float64, n)
		for j := 0; j < n; j++ {
			sample[j] = vals[rng.Intn(n)]
		}
		p95s[i] = percentile(sample, 95)
	}
	sort.Float64s(p95s)
	low = percentile(p95s, 2.5)
	high = percentile(p95s, 97.5)
	return low, high
}

// ── JSON emission ──────────────────────────────────────────────────────────

// budgetAReport is the machine-readable output format for Phase 7 report generation.
type budgetAReport struct {
	Transport    string            `json:"transport"`
	N            int               `json:"n"`
	P50MS        float64           `json:"p50_ms"`
	P95MS        float64           `json:"p95_ms"`
	P99MS        float64           `json:"p99_ms"`
	CILowMS      float64           `json:"ci_low_ms"`
	CIHighMS     float64           `json:"ci_high_ms"`
	CIHalfWidth  float64           `json:"ci_half_width_ms"`
	PassThreshold int              `json:"pass_threshold_ms"`
	Passed       bool              `json:"passed"`
	Samples      []sampleEntry     `json:"samples"`
}

type sampleEntry struct {
	TotalMS     float64 `json:"total_ms"`
	SynthesisMS float64 `json:"synthesis_ms"`
	PropagateMS float64 `json:"propagate_ms"`
}

func emitBudgetAJSON(t *testing.T, n int, samples []timingRecord, p50, p95, p99, ciLow, ciHigh, ciHalfWidth float64) {
	t.Helper()
	entries := make([]sampleEntry, len(samples))
	for i, s := range samples {
		entries[i] = sampleEntry{
			TotalMS:     s.TotalMS,
			SynthesisMS: s.SynthesisMS,
			PropagateMS: s.PropagateMS,
		}
	}
	report := budgetAReport{
		Transport:     "fs",
		N:             n,
		P50MS:         p50,
		P95MS:         p95,
		P99MS:         p99,
		CILowMS:       ciLow,
		CIHighMS:      ciHigh,
		CIHalfWidth:   ciHalfWidth,
		PassThreshold: budgetAP95Threshold,
		Passed:        p95 <= budgetAP95Threshold && p99 <= budgetAP99Threshold,
		Samples:       entries,
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Logf("WARNING: could not marshal budget-a report: %v", err)
		return
	}
	t.Logf("BUDGET_A_REPORT_JSON: %s", string(b))
}
