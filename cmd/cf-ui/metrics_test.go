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
