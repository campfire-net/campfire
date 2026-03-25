// cmd/cf-ui/metrics_test.go — tests for the Prometheus metrics endpoint and instrumentation.
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- Unit tests for MetricsRegistry ---

func TestMessageCounter_IncrementAndOutput(t *testing.T) {
	c := newMessageCounter()

	c.Inc("campfire-abc")
	c.Inc("campfire-abc")
	c.Inc("campfire-xyz")

	var sb strings.Builder
	c.writeTo(&sb)
	out := sb.String()

	if !strings.Contains(out, `cfui_messages_total{campfire_id="campfire-abc"} 2`) {
		t.Errorf("expected counter=2 for campfire-abc in:\n%s", out)
	}
	if !strings.Contains(out, `cfui_messages_total{campfire_id="campfire-xyz"} 1`) {
		t.Errorf("expected counter=1 for campfire-xyz in:\n%s", out)
	}
}

func TestMessageCounter_TypeHelp(t *testing.T) {
	c := newMessageCounter()
	var sb strings.Builder
	c.writeTo(&sb)
	out := sb.String()

	if !strings.Contains(out, "# TYPE cfui_messages_total counter") {
		t.Errorf("missing TYPE line in:\n%s", out)
	}
	if !strings.Contains(out, "# HELP cfui_messages_total") {
		t.Errorf("missing HELP line in:\n%s", out)
	}
}

func TestSSEGauge_IncDec(t *testing.T) {
	g := newSSEGauge()

	g.Inc("alice@example.com")
	g.Inc("alice@example.com")
	g.Inc("bob@example.com")

	var sb strings.Builder
	g.writeTo(&sb)
	out := sb.String()

	if !strings.Contains(out, `cfui_sse_connections{operator="alice@example.com"} 2`) {
		t.Errorf("expected gauge=2 for alice in:\n%s", out)
	}
	if !strings.Contains(out, `cfui_sse_connections{operator="bob@example.com"} 1`) {
		t.Errorf("expected gauge=1 for bob in:\n%s", out)
	}

	// Decrement alice to 1.
	g.Dec("alice@example.com")
	sb.Reset()
	g.writeTo(&sb)
	out = sb.String()

	if !strings.Contains(out, `cfui_sse_connections{operator="alice@example.com"} 1`) {
		t.Errorf("expected gauge=1 after dec for alice in:\n%s", out)
	}

	// Decrement bob to 0 — key should be removed.
	g.Dec("bob@example.com")
	sb.Reset()
	g.writeTo(&sb)
	out = sb.String()

	if strings.Contains(out, "bob@example.com") {
		t.Errorf("expected bob to be removed from gauge output after reaching zero:\n%s", out)
	}
}

func TestSSEGauge_TypeHelp(t *testing.T) {
	g := newSSEGauge()
	var sb strings.Builder
	g.writeTo(&sb)
	out := sb.String()

	if !strings.Contains(out, "# TYPE cfui_sse_connections gauge") {
		t.Errorf("missing TYPE line in:\n%s", out)
	}
}

func TestLatencyHistogram_ObserveAndOutput(t *testing.T) {
	h := newLatencyHistogram()

	h.Observe("GET", "/healthz", 5*time.Millisecond)
	h.Observe("GET", "/healthz", 15*time.Millisecond)
	h.Observe("POST", "/c/abc/send", 50*time.Millisecond)

	var sb strings.Builder
	h.writeTo(&sb)
	out := sb.String()

	// Both series should be present.
	if !strings.Contains(out, `method="GET"`) {
		t.Errorf("missing GET method in histogram output:\n%s", out)
	}
	if !strings.Contains(out, `path="/healthz"`) {
		t.Errorf("missing /healthz path in histogram output:\n%s", out)
	}
	if !strings.Contains(out, `path="/c/abc/send"`) {
		t.Errorf("missing /c/abc/send path in histogram output:\n%s", out)
	}

	// Count for GET /healthz should be 2.
	if !strings.Contains(out, fmt.Sprintf(`cfui_http_request_duration_seconds_count{method="GET",path="/healthz"} 2`)) {
		t.Errorf("expected count=2 for GET /healthz in:\n%s", out)
	}

	// +Inf bucket for GET /healthz should be 2.
	if !strings.Contains(out, fmt.Sprintf(`cfui_http_request_duration_seconds_bucket{method="GET",path="/healthz",le="+Inf"} 2`)) {
		t.Errorf("expected +Inf bucket=2 for GET /healthz in:\n%s", out)
	}

	// The 0.005 bucket should have 1 hit (5ms <= 0.005s).
	if !strings.Contains(out, `cfui_http_request_duration_seconds_bucket{method="GET",path="/healthz",le="0.005"} 1`) {
		t.Errorf("expected 0.005 bucket=1 for GET /healthz in:\n%s", out)
	}
}

func TestLatencyHistogram_TypeHelp(t *testing.T) {
	h := newLatencyHistogram()
	var sb strings.Builder
	h.writeTo(&sb)
	out := sb.String()

	if !strings.Contains(out, "# TYPE cfui_http_request_duration_seconds histogram") {
		t.Errorf("missing TYPE line in:\n%s", out)
	}
}

// --- Integration tests via HTTP endpoint ---

// newTestServerWithBundle returns a test server plus the full muxBundle (for metrics access).
func newTestServerWithBundle(t *testing.T) (*httptest.Server, muxBundle) {
	t.Helper()
	logger := newDiscardLogger()
	authCfg := newAuthConfig(logger, func(string) string { return "" }, "http://localhost", NewMemSessionStore(), noopAuthProvider{})
	bundle := buildMuxWithAuth(logger, authCfg)
	srv := httptest.NewServer(bundle.handler)
	t.Cleanup(srv.Close)
	return srv, bundle
}

func TestMetricsEndpoint_ReturnsValidText(t *testing.T) {
	srv, _ := newTestServerWithBundle(t)

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("expected text/plain content-type, got %q", ct)
	}

	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	// Must contain TYPE lines for all three metric families.
	for _, expected := range []string{
		"# TYPE cfui_messages_total counter",
		"# TYPE cfui_sse_connections gauge",
		"# TYPE cfui_http_request_duration_seconds histogram",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %q in /metrics output:\n%s", expected, body)
		}
	}
}

func TestMetricsEndpoint_NoSessionRequired(t *testing.T) {
	srv, _ := newTestServerWithBundle(t)

	// Do NOT set a session cookie — should still get 200.
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics should be public (no auth required), got %d", resp.StatusCode)
	}
}

func TestMetricsEndpoint_CounterIncrementOnPublish(t *testing.T) {
	srv, bundle := newTestServerWithBundle(t)

	// Publish three events — two to campfire-a, one to campfire-b.
	bundle.hub.Publish("campfire-a", SSEEvent{Type: SSEEventMessage, Data: map[string]any{"text": "hello"}})
	bundle.hub.Publish("campfire-a", SSEEvent{Type: SSEEventMessage, Data: map[string]any{"text": "world"}})
	bundle.hub.Publish("campfire-b", SSEEvent{Type: SSEEventMessage, Data: map[string]any{"text": "bye"}})

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	if !strings.Contains(body, `cfui_messages_total{campfire_id="campfire-a"} 2`) {
		t.Errorf("expected counter=2 for campfire-a in:\n%s", body)
	}
	if !strings.Contains(body, `cfui_messages_total{campfire_id="campfire-b"} 1`) {
		t.Errorf("expected counter=1 for campfire-b in:\n%s", body)
	}
}

func TestMetricsEndpoint_SSEGaugeViaRegistry(t *testing.T) {
	_, bundle := newTestServerWithBundle(t)

	// Directly manipulate the SSE gauge via the registry (SSE connections require
	// a live HTTP response writer with Flusher support; testing the gauge's
	// register/unregister path here is the correct scope for a unit-style integration test).
	bundle.metrics.SSE.Inc("operator@example.com")
	bundle.metrics.SSE.Inc("operator@example.com")

	var sb strings.Builder
	bundle.metrics.SSE.writeTo(&sb)
	out := sb.String()

	if !strings.Contains(out, `cfui_sse_connections{operator="operator@example.com"} 2`) {
		t.Errorf("expected gauge=2 for operator in:\n%s", out)
	}

	bundle.metrics.SSE.Dec("operator@example.com")
	sb.Reset()
	bundle.metrics.SSE.writeTo(&sb)
	out = sb.String()

	if !strings.Contains(out, `cfui_sse_connections{operator="operator@example.com"} 1`) {
		t.Errorf("expected gauge=1 after dec in:\n%s", out)
	}
}

// TestHistogram_SingleObservationCumulativeCounts verifies that a single observation
// produces correct cumulative bucket values (regression for double-cumulation bug: q34).
// A 0.03s observation must appear in le=0.05, le=0.1, ..., le=10.0 as 1,
// NOT as progressively larger numbers caused by per-bucket increments + re-summing.
func TestHistogram_SingleObservationCumulativeCounts(t *testing.T) {
	h := newLatencyHistogram()
	// 0.03s falls in the 0.05 bucket (first upper bound >= 0.03).
	h.Observe("GET", "/test", 30*time.Millisecond)

	var sb strings.Builder
	h.writeTo(&sb)
	out := sb.String()

	// Buckets below 0.03s must be 0.
	for _, le := range []string{"0.005", "0.01", "0.025"} {
		want := fmt.Sprintf(`cfui_http_request_duration_seconds_bucket{method="GET",path="/test",le="%s"} 0`, le)
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in histogram output:\n%s", want, out)
		}
	}

	// The 0.05 bucket is the first to include the 0.03s observation; cumulative = 1.
	want005 := `cfui_http_request_duration_seconds_bucket{method="GET",path="/test",le="0.05"} 1`
	if !strings.Contains(out, want005) {
		t.Errorf("expected le=0.05 bucket=1 (single observation), got output:\n%s", out)
	}

	// All higher buckets must also be 1 (cumulative), not 2, 3, etc.
	for _, le := range []string{"0.1", "0.25", "0.5", "1", "2.5", "5", "10"} {
		want := fmt.Sprintf(`cfui_http_request_duration_seconds_bucket{method="GET",path="/test",le="%s"} 1`, le)
		if !strings.Contains(out, want) {
			t.Errorf("expected cumulative bucket=1 for le=%s (no double-cumulation), got output:\n%s", le, out)
		}
	}

	// +Inf bucket and count must both be 1.
	wantInf := `cfui_http_request_duration_seconds_bucket{method="GET",path="/test",le="+Inf"} 1`
	if !strings.Contains(out, wantInf) {
		t.Errorf("expected +Inf bucket=1, got output:\n%s", out)
	}
	wantCount := `cfui_http_request_duration_seconds_count{method="GET",path="/test"} 1`
	if !strings.Contains(out, wantCount) {
		t.Errorf("expected count=1, got output:\n%s", out)
	}
}

// TestHistogram_LabelNotCorrupted verifies that label values are not corrupted across
// bucket lines due to slice aliasing (regression for udj: append(base,...) shared backing array).
// After writeTo, every bucket line must have the correct le= value, not a value overwritten
// by a subsequent append.
func TestHistogram_LabelNotCorrupted(t *testing.T) {
	h := newLatencyHistogram()
	h.Observe("POST", "/send", 5*time.Millisecond)

	var sb strings.Builder
	h.writeTo(&sb)
	out := sb.String()

	// Each bucket line must pair its own le value with the correct count.
	// The aliasing bug would cause earlier le= values to be overwritten by "+Inf".
	cases := []struct {
		le   string
		want int64
	}{
		{"0.005", 1}, // 5ms <= 0.005s
		{"0.01", 1},
		{"0.025", 1},
		{"0.05", 1},
		{"0.1", 1},
		{"0.25", 1},
		{"0.5", 1},
		{"1", 1},
		{"2.5", 1},
		{"5", 1},
		{"10", 1},
		{"+Inf", 1},
	}
	for _, tc := range cases {
		line := fmt.Sprintf(`cfui_http_request_duration_seconds_bucket{method="POST",path="/send",le="%s"} %d`, tc.le, tc.want)
		if !strings.Contains(out, line) {
			t.Errorf("label corruption or wrong count: expected line %q in:\n%s", line, out)
		}
	}

	// Confirm no bucket line contains le="+Inf" where it shouldn't (aliasing symptom).
	// Every non-inf le= line must have its own le value present in the output.
	for _, le := range []string{"0.005", "0.01", "0.025", "0.05", "0.1", "0.25", "0.5", "1", "2.5", "5", "10"} {
		marker := fmt.Sprintf(`le="%s"`, le)
		if !strings.Contains(out, marker) {
			t.Errorf("missing le=%s in output (possible aliasing): %s", le, out)
		}
	}
}

func TestMetricsEndpoint_LatencyRecordedByMiddleware(t *testing.T) {
	srv, bundle := newTestServerWithBundle(t)

	// Make a request — the latency middleware should record it.
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()

	var sb strings.Builder
	bundle.metrics.Latency.writeTo(&sb)
	out := sb.String()

	// At least one observation for GET /healthz should have been recorded.
	if !strings.Contains(out, `cfui_http_request_duration_seconds_count{method="GET",path="/healthz"} 1`) {
		t.Errorf("expected latency recorded for GET /healthz in:\n%s", out)
	}
}
