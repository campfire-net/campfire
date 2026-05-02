//go:build uxmeas

// Package uxmeas_test — Phase 8 Gate 2 Budget A HTTP-transport harness.
//
// This file implements the missing HTTP-transport path of the Budget A harness
// (veracity finding campfireagent-b8a: campfireagent-461 silently dropped the
// HTTP path). Tracked by campfireagent-8f0c.
//
// # Budget A — Agent→inbox latency (HTTP transport)
//
// Same measurement definition as the FS harness (uxmeas_test.go §1.1), but the
// campfire uses two real cf-protocol HTTP transports bound to ephemeral loopback
// ports instead of a shared filesystem directory.
//
// Pass threshold: p95 ≤ 5000 ms, p99 ≤ 8000 ms (same as FS).
// Sample size: N = 100.
//
// The HTTP transports are local in-process servers — no prod credentials are
// required and no outbound connections leave the test host. Safe for CI.
//
// # How the HTTP harness works
//
// Two protocol.Client nodes each have their own store and HTTP transport:
//
//   - Owner (ownerClient/ownerStore/trOwner): creates the campfire with
//     P2PHTTPTransport. Its HTTP transport listens on 127.0.0.1:0.
//   - Agent (agentClient/agentStore/trAgent): joins over HTTP.
//
// Send path: agentClient.Send → sendP2PHTTP → cfhttp.DeliverToAll(ownerEndpoint)
// → trOwner.handleDeliver → stored in ownerStore.
// Subscribe path: ownerClient.Subscribe → polls ownerClient.Read → ownerStore.
//
// The critical property: trOwner and ownerClient share the SAME ownerStore.
// Messages delivered to trOwner are thus visible to ownerClient.Subscribe
// without any extra sync step.
//
// T_total = T2 − T0 captures the full send→owner-delivery RTT over HTTP.
//
// # Running
//
//	go test -tags uxmeas -v -run TestBudgetA_AgentToInboxLatency_HTTP ./cf-conventions/cf-authority/trust/uxmeas/...
//	go test -tags uxmeas -v -count=1 -timeout=10m ./cf-conventions/cf-authority/trust/uxmeas/...
package uxmeas_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
)

// TestMain is the entry point for the uxmeas-tagged test binary.
// It overrides the SSRF-safe HTTP client and endpoint validator so that
// in-process HTTP transports on 127.0.0.1 pass the loopback guard.
// Pattern mirrors cf-protocol/protocol/testmain_test.go.
func TestMain(m *testing.M) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverrideValidateJoinerEndpointForTest()
	os.Exit(m.Run())
}

// TestBudgetA_AgentToInboxLatency_HTTP runs N=100 approval-flow timing samples
// against two ephemeral HTTP-transport campfire nodes and asserts Budget A
// thresholds (p95 ≤ 5000 ms, p99 ≤ 8000 ms).
//
// This is the Phase 8 Gate 2 automated instrument for the HTTP transport path,
// completing the two-transport requirement of campfireagent-461.
func TestBudgetA_AgentToInboxLatency_HTTP(t *testing.T) {
	// ── Owner node: creates the campfire; receives delegation:request ───────
	ownerID, ownerStore, trOwner := newHTTPBudgetANode(t)
	ownerEndpoint := fmt.Sprintf("http://%s", trOwner.Addr())
	ownerTransportDir := t.TempDir()

	// ── Agent node: sends delegation:request ────────────────────────────────
	agentID, agentStore, trAgent := newHTTPBudgetANode(t)
	agentEndpoint := fmt.Sprintf("http://%s", trAgent.Addr())
	agentTransportDir := t.TempDir()

	ownerClient := protocol.New(ownerStore, ownerID)
	agentClient := protocol.New(agentStore, agentID)

	// ── Create the identity campfire on the owner node ──────────────────────
	createResult, err := ownerClient.Create(protocol.CreateRequest{
		Transport: &protocol.P2PHTTPTransport{
			Transport:  trOwner,
			MyEndpoint: ownerEndpoint,
			Dir:        ownerTransportDir,
		},
		JoinProtocol: "open",
	})
	if err != nil {
		t.Fatalf("owner.Create: %v", err)
	}
	identityCampfireID := createResult.CampfireID

	// ── Agent joins the campfire over HTTP ───────────────────────────────────
	_, err = agentClient.Join(protocol.JoinRequest{
		CampfireID: identityCampfireID,
		Transport: &protocol.P2PHTTPTransport{
			Transport:    trAgent,
			MyEndpoint:   agentEndpoint,
			PeerEndpoint: ownerEndpoint,
			Dir:          agentTransportDir,
		},
	})
	if err != nil {
		t.Fatalf("agent.Join: %v", err)
	}

	// ── Run N samples ────────────────────────────────────────────────────────
	samples := make([]timingRecord, 0, N)
	for i := 0; i < N; i++ {
		rec, err := runOneHTTPApprovalFlow(t, agentClient, ownerClient, agentID, identityCampfireID, i)
		if err != nil {
			t.Fatalf("HTTP run %d: %v", i, err)
		}
		samples = append(samples, rec)
	}

	// ── Compute statistics ───────────────────────────────────────────────────
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

	// ── Report ───────────────────────────────────────────────────────────────
	t.Logf("Budget A (HTTP transport) — N=%d", N)
	t.Logf("  Total latency (T2−T0):")
	t.Logf("    p50  = %.1f ms", p50total)
	t.Logf("    p95  = %.1f ms  (threshold: ≤%d ms)", p95total, budgetAP95Threshold)
	t.Logf("    p99  = %.1f ms  (threshold: ≤%d ms)", p99total, budgetAP99Threshold)
	t.Logf("    95%% CI on p95: [%.1f, %.1f] (half-width %.1f ms)", ciLow, ciHigh, ciHalfWidth)
	t.Logf("  Sub-budgets (diagnostic only):")
	t.Logf("    Synthesis (T1−T0) p95  = %.1f ms  (budget: ≤%d ms)", p95synth, synthesisP95Budget)
	t.Logf("    Propagation (T2−T1) p95 = %.1f ms  (budget: ≤%d ms)", p95prop, propagationP95Budget)

	emitBudgetAHTTPJSON(t, N, samples, p50total, p95total, p99total, ciLow, ciHigh, ciHalfWidth)

	// ── Pass/fail assertions ─────────────────────────────────────────────────
	if ciHalfWidth > budgetACIHalfWidth {
		t.Logf("WARNING: 95%% CI half-width %.1f ms > %d ms — consider running more samples",
			ciHalfWidth, budgetACIHalfWidth)
	}
	if p95total > budgetAP95Threshold {
		t.Errorf("FAIL: p95 total latency %.1f ms exceeds Budget A threshold %d ms",
			p95total, budgetAP95Threshold)
	}
	if p99total > budgetAP99Threshold {
		t.Errorf("FAIL: p99 total latency %.1f ms exceeds Budget A threshold %d ms",
			p99total, budgetAP99Threshold)
	}

	if t.Failed() {
		t.Log("Budget A HTTP FAILED — halt Phase 8 per design §1.3")
	} else {
		t.Log("Budget A HTTP PASSED — HTTP transport within latency envelope")
	}
}

// runOneHTTPApprovalFlow executes a single delegation:request → owner-Read-detection
// cycle over HTTP transport and returns the timing record.
//
// Uses a direct polling loop (ownerClient.Read with AfterTimestamp cursor) rather
// than ownerClient.Subscribe. Subscribe spawns a persistent goroutine per call;
// across N=100 runs, those goroutines can accumulate and contend on the SQLite
// store, causing sporadic timeouts. The direct poll loop is equivalent to
// Subscribe's inner loop but scoped to this single run — no goroutine leak.
//
// PollInterval: 50ms (same as the FS harness's Subscribe PollInterval).
// Per-run timeout: 15s.
func runOneHTTPApprovalFlow(
	t *testing.T,
	agentClient *protocol.Client,
	ownerClient *protocol.Client,
	agentID *identity.Identity,
	identityCampfireID string,
	runIdx int,
) (timingRecord, error) {
	t.Helper()

	reqPayload := delegationRequestPayload{
		AgentPubkey:     agentID.PublicKeyHex(),
		FutureID:        fmt.Sprintf("http-future-%d-%d", runIdx, time.Now().UnixNano()),
		FailedPredicate: map[string]any{"kind": "grant", "convention": "rd", "op": "claim"},
		RequestedScope:  "rd:*",
		CampfireID:      identityCampfireID,
	}
	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return timingRecord{}, fmt.Errorf("marshal payload: %w", err)
	}

	// Capture the cursor immediately before T0. We start the poll after Send
	// returns (T1), using AfterTimestamp = cursorNano so we only see messages
	// written at or after T0. Subtract 1ms to ensure the new message's
	// timestamp (set inside message.NewMessage ≈ T0) falls in the query window.
	cursorNano := time.Now().UnixNano() - int64(time.Millisecond)

	// T0: immediately before Send.
	t0 := time.Now()

	_, err = agentClient.Send(protocol.SendRequest{
		CampfireID: identityCampfireID,
		Payload:    payloadBytes,
		Tags:       []string{"delegation:request"},
	})
	if err != nil {
		return timingRecord{}, fmt.Errorf("agent Send (HTTP): %w", err)
	}

	// T1: send-ack (Send returned).
	t1 := time.Now()

	// Poll ownerClient.Read until the message with our futureID appears or timeout.
	// Each poll reads from ownerStore (where trOwner.handleDeliver stores messages).
	// No goroutine leak: this is a simple loop in the caller's goroutine.
	const pollInterval = 50 * time.Millisecond
	const perRunTimeout = 15 * time.Second
	deadline := time.Now().Add(perRunTimeout)
	futureID := reqPayload.FutureID
	var t2 time.Time

	for time.Now().Before(deadline) {
		result, readErr := ownerClient.Read(protocol.ReadRequest{
			CampfireID:     identityCampfireID,
			AfterTimestamp: cursorNano,
			Tags:           []string{"delegation:request"},
		})
		if readErr != nil {
			return timingRecord{}, fmt.Errorf("owner Read (run %d): %w", runIdx, readErr)
		}

		for _, msg := range result.Messages {
			var delivered delegationRequestPayload
			if jsonErr := json.Unmarshal(msg.Payload, &delivered); jsonErr != nil {
				continue
			}
			if delivered.FutureID == futureID {
				t2 = time.Now()
				goto httpFound
			}
		}

		time.Sleep(pollInterval)
	}
	return timingRecord{}, fmt.Errorf(
		"timeout waiting for HTTP message delivery (run %d, futureID %s)",
		runIdx, futureID)

httpFound:
	return timingRecord{
		T0:          t0,
		T1:          t1,
		T2:          t2,
		TotalMS:     float64(t2.Sub(t0).Microseconds()) / 1000.0,
		SynthesisMS: float64(t1.Sub(t0).Microseconds()) / 1000.0,
		PropagateMS: float64(t2.Sub(t1).Microseconds()) / 1000.0,
	}, nil
}

// ── HTTP node setup ────────────────────────────────────────────────────────

// newHTTPBudgetANode creates a fresh identity, a SQLite store, and a real
// HTTP transport bound to 127.0.0.1:0. The transport and the returned store
// share the same underlying SQLite database, so messages delivered to the
// transport are immediately readable by a protocol.Client backed by that store.
func newHTTPBudgetANode(t *testing.T) (*identity.Identity, store.Store, *cfhttp.Transport) {
	t.Helper()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	tr := cfhttp.New("127.0.0.1:0", s)
	if err := tr.Start(); err != nil {
		t.Fatalf("starting HTTP transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck

	// Give the listener a moment to bind before returning.
	time.Sleep(20 * time.Millisecond)

	return id, s, tr
}

// ── JSON emission ──────────────────────────────────────────────────────────

// emitBudgetAHTTPJSON emits a machine-readable JSON summary of the HTTP Budget A
// run. Identical format to emitBudgetAJSON but with Transport="http".
func emitBudgetAHTTPJSON(t *testing.T, n int, samples []timingRecord, p50, p95, p99, ciLow, ciHigh, ciHalfWidth float64) {
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
		Transport:     "http",
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
		t.Logf("WARNING: could not marshal budget-a HTTP report: %v", err)
		return
	}
	t.Logf("BUDGET_A_HTTP_REPORT_JSON: %s", string(b))
}
