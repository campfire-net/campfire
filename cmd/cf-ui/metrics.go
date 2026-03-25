// cmd/cf-ui/metrics.go — In-memory Prometheus-compatible metrics.
//
// Implements three metric families for the fast-loop instrumentation (RPT 1.7):
//
//   - cfui_messages_total{campfire_id="..."} — counter, incremented by SSEHub.Publish
//   - cfui_sse_connections{operator="..."} — gauge, tracking active SSE connections
//   - cfui_http_request_duration_seconds{path="...",method="..."} — histogram
//
// The /metrics endpoint returns Prometheus text exposition format (v0.0.4).
// No external metrics library is required.
package main

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// metricLabel is a key=value pair for metric labels.
type metricLabel struct {
	name  string
	value string
}

// labelStr formats a slice of labels as a Prometheus label string, e.g. {k="v",k2="v2"}.
func labelStr(labels []metricLabel) string {
	if len(labels) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('{')
	for i, l := range labels {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `%s="%s"`, l.name, escapeLabelValue(l.value))
	}
	sb.WriteByte('}')
	return sb.String()
}

// escapeLabelValue escapes backslash, double-quote, and newline in label values.
func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// --- Counter ---

// counterKey is the map key for a counter series (label fingerprint).
type counterKey struct {
	campfireID string
}

// messageCounter holds cfui_messages_total counters keyed by campfire ID.
type messageCounter struct {
	mu     sync.Mutex
	values map[counterKey]*atomic.Int64
}

func newMessageCounter() *messageCounter {
	return &messageCounter{values: make(map[counterKey]*atomic.Int64)}
}

// Inc increments the counter for campfireID by 1.
func (c *messageCounter) Inc(campfireID string) {
	key := counterKey{campfireID: campfireID}
	c.mu.Lock()
	v, ok := c.values[key]
	if !ok {
		v = &atomic.Int64{}
		c.values[key] = v
	}
	c.mu.Unlock()
	v.Add(1)
}

// writeTo writes the counter family to w in Prometheus text format.
func (c *messageCounter) writeTo(w io.Writer) {
	fmt.Fprintln(w, "# HELP cfui_messages_total Total messages published per campfire.")
	fmt.Fprintln(w, "# TYPE cfui_messages_total counter")

	c.mu.Lock()
	type row struct {
		campfireID string
		val        int64
	}
	rows := make([]row, 0, len(c.values))
	for k, v := range c.values {
		rows = append(rows, row{campfireID: k.campfireID, val: v.Load()})
	}
	c.mu.Unlock()

	sort.Slice(rows, func(i, j int) bool { return rows[i].campfireID < rows[j].campfireID })
	for _, r := range rows {
		labels := labelStr([]metricLabel{{name: "campfire_id", value: r.campfireID}})
		fmt.Fprintf(w, "cfui_messages_total%s %d\n", labels, r.val)
	}
}

// --- Gauge ---

// sseGauge tracks cfui_sse_connections per operator.
type sseGauge struct {
	mu     sync.Mutex
	values map[string]int64 // operatorEmail → count
}

func newSSEGauge() *sseGauge {
	return &sseGauge{values: make(map[string]int64)}
}

// Inc increments the gauge for operator by 1.
func (g *sseGauge) Inc(operator string) {
	g.mu.Lock()
	g.values[operator]++
	g.mu.Unlock()
}

// Dec decrements the gauge for operator by 1, removing the key when it hits zero.
func (g *sseGauge) Dec(operator string) {
	g.mu.Lock()
	g.values[operator]--
	if g.values[operator] <= 0 {
		delete(g.values, operator)
	}
	g.mu.Unlock()
}

// writeTo writes the gauge family to w in Prometheus text format.
func (g *sseGauge) writeTo(w io.Writer) {
	fmt.Fprintln(w, "# HELP cfui_sse_connections Active SSE connections per operator.")
	fmt.Fprintln(w, "# TYPE cfui_sse_connections gauge")

	g.mu.Lock()
	type row struct {
		operator string
		val      int64
	}
	rows := make([]row, 0, len(g.values))
	for op, v := range g.values {
		rows = append(rows, row{operator: op, val: v})
	}
	g.mu.Unlock()

	sort.Slice(rows, func(i, j int) bool { return rows[i].operator < rows[j].operator })
	for _, r := range rows {
		labels := labelStr([]metricLabel{{name: "operator", value: r.operator}})
		fmt.Fprintf(w, "cfui_sse_connections%s %d\n", labels, r.val)
	}
}

// --- Histogram ---

// histogramBuckets are the upper bounds (in seconds) for the HTTP latency histogram.
// These cover the expected range for a server-rendered web UI.
var histogramBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0}

// histKey is the label fingerprint for a histogram series.
type histKey struct {
	path   string
	method string
}

// histogramBucketCount is the number of histogram buckets (constant for array sizing).
const histogramBucketCount = 11

// histSeries is one histogram time series (one {path, method} label combination).
type histSeries struct {
	mu      sync.Mutex
	buckets [histogramBucketCount]int64 // cumulative counts per upper bound; index matches histogramBuckets
	count   int64
	sum     float64
}

func (s *histSeries) observe(v float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, upper := range histogramBuckets {
		if v <= upper {
			s.buckets[i]++
		}
	}
	s.count++
	s.sum += v
}

// latencyHistogram tracks cfui_http_request_duration_seconds.
type latencyHistogram struct {
	mu     sync.Mutex
	series map[histKey]*histSeries
}

func newLatencyHistogram() *latencyHistogram {
	return &latencyHistogram{series: make(map[histKey]*histSeries)}
}

// Observe records an observation of duration d for the given path and method.
func (h *latencyHistogram) Observe(method, path string, d time.Duration) {
	key := histKey{path: path, method: method}
	h.mu.Lock()
	s, ok := h.series[key]
	if !ok {
		s = &histSeries{}
		h.series[key] = s
	}
	h.mu.Unlock()
	s.observe(d.Seconds())
}

// histSeriesSnapshot is a lock-free snapshot of a histSeries for rendering.
type histSeriesSnapshot struct {
	key     histKey
	buckets [histogramBucketCount]int64
	count   int64
	sum     float64
}

// writeTo writes the histogram family to w in Prometheus text format.
func (h *latencyHistogram) writeTo(w io.Writer) {
	fmt.Fprintln(w, "# HELP cfui_http_request_duration_seconds HTTP request latency histogram.")
	fmt.Fprintln(w, "# TYPE cfui_http_request_duration_seconds histogram")

	h.mu.Lock()
	snaps := make([]histSeriesSnapshot, 0, len(h.series))
	for k, s := range h.series {
		s.mu.Lock()
		snaps = append(snaps, histSeriesSnapshot{
			key:     k,
			buckets: s.buckets,
			count:   s.count,
			sum:     s.sum,
		})
		s.mu.Unlock()
	}
	h.mu.Unlock()

	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].key.method != snaps[j].key.method {
			return snaps[i].key.method < snaps[j].key.method
		}
		return snaps[i].key.path < snaps[j].key.path
	})

	for i := range snaps {
		sn := &snaps[i]
		base := []metricLabel{
			{name: "method", value: sn.key.method},
			{name: "path", value: sn.key.path},
		}
		// Bucket lines (cumulative).
		var cumulative int64
		for bi, upper := range histogramBuckets {
			cumulative += sn.buckets[bi]
			leLabels := append(base, metricLabel{name: "le", value: formatFloat(upper)})
			fmt.Fprintf(w, "cfui_http_request_duration_seconds_bucket%s %d\n", labelStr(leLabels), cumulative)
		}
		// +Inf bucket.
		infLabels := append(base, metricLabel{name: "le", value: "+Inf"})
		fmt.Fprintf(w, "cfui_http_request_duration_seconds_bucket%s %d\n", labelStr(infLabels), sn.count)
		// Sum and count.
		fmt.Fprintf(w, "cfui_http_request_duration_seconds_sum%s %s\n", labelStr(base), formatFloat(sn.sum))
		fmt.Fprintf(w, "cfui_http_request_duration_seconds_count%s %d\n", labelStr(base), sn.count)
	}
}

// formatFloat formats a float64 without scientific notation for Prometheus exposition.
func formatFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "+Inf"
	}
	if math.IsInf(f, -1) {
		return "-Inf"
	}
	if math.IsNaN(f) {
		return "NaN"
	}
	s := fmt.Sprintf("%g", f)
	return s
}

// --- Registry ---

// MetricsRegistry holds all metric families for the cf-ui server.
// It is safe for concurrent use from multiple goroutines.
type MetricsRegistry struct {
	Messages *messageCounter
	SSE      *sseGauge
	Latency  *latencyHistogram
}

// NewMetricsRegistry creates a MetricsRegistry with all metric families initialised.
func NewMetricsRegistry() *MetricsRegistry {
	return &MetricsRegistry{
		Messages: newMessageCounter(),
		SSE:      newSSEGauge(),
		Latency:  newLatencyHistogram(),
	}
}

// writeMetrics writes all metric families to w in Prometheus text exposition format.
func (r *MetricsRegistry) writeMetrics(w io.Writer) {
	r.Messages.writeTo(w)
	r.SSE.writeTo(w)
	r.Latency.writeTo(w)
}

// handleMetrics returns an HTTP handler for GET /metrics.
// The endpoint is public (no session auth) and is intended for infrastructure monitoring.
func handleMetrics(reg *MetricsRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		reg.writeMetrics(w)
	}
}

// LatencyMiddleware returns an HTTP middleware that records request duration
// for all routes using the provided MetricsRegistry.
// The path label is the raw URL path (not the route pattern, to avoid high cardinality
// in the normal case — callers may normalise it further if needed).
func LatencyMiddleware(reg *MetricsRegistry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			reg.Latency.Observe(r.Method, r.URL.Path, time.Since(start))
		})
	}
}
