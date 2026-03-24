package meter_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/meter"
)

// --- Helpers ---

// staticTokenProvider returns a fixed token string.
type staticTokenProvider struct{ token string }

func (s *staticTokenProvider) Token(_ context.Context) (string, error) { return s.token, nil }

// captureServer is a mock HTTP server that records received UsageEvent payloads.
type captureServer struct {
	mu     sync.Mutex
	events []meter.UsageEvent
	status int // response status; defaults to 200
}

func newCaptureServer() *captureServer { return &captureServer{status: http.StatusOK} }

func (s *captureServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var ev meter.UsageEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.events = append(s.events, ev)
		s.mu.Unlock()
		w.WriteHeader(s.status)
	}
}

func (s *captureServer) captured() []meter.UsageEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]meter.UsageEvent, len(s.events))
	copy(out, s.events)
	return out
}

// --- UsageCollector tests ---

func TestUsageCollectorRecordAndCount(t *testing.T) {
	c := meter.NewUsageCollector()

	c.RecordMessage("fire1")
	c.RecordMessage("fire1")
	c.RecordMessage("fire2")

	if got := c.Count("fire1"); got != 2 {
		t.Fatalf("expected Count(fire1)=2, got %d", got)
	}
	if got := c.Count("fire2"); got != 1 {
		t.Fatalf("expected Count(fire2)=1, got %d", got)
	}
	if got := c.Count("fire3"); got != 0 {
		t.Fatalf("expected Count(fire3)=0, got %d", got)
	}
}

func TestUsageCollectorSnapshotDrainsCounters(t *testing.T) {
	c := meter.NewUsageCollector()

	c.RecordMessage("fire1")
	c.RecordMessage("fire1")
	c.RecordMessage("fire2")

	snap := c.Snapshot()

	if snap["fire1"] != 2 {
		t.Fatalf("snapshot fire1: want 2, got %d", snap["fire1"])
	}
	if snap["fire2"] != 1 {
		t.Fatalf("snapshot fire2: want 1, got %d", snap["fire2"])
	}

	// After snapshot, counters are reset.
	if got := c.Count("fire1"); got != 0 {
		t.Fatalf("after snapshot, Count(fire1) should be 0, got %d", got)
	}
	if got := c.Count("fire2"); got != 0 {
		t.Fatalf("after snapshot, Count(fire2) should be 0, got %d", got)
	}

	// Snapshot of empty collector returns empty map, not nil.
	snap2 := c.Snapshot()
	if len(snap2) != 0 {
		t.Fatalf("empty snapshot should have 0 entries, got %d", len(snap2))
	}
}

func TestUsageCollectorSnapshotExcludesZeroCounts(t *testing.T) {
	c := meter.NewUsageCollector()
	// A campfire that was recorded and then a snapshot was taken leaves no residue.
	c.RecordMessage("fire1")
	c.Snapshot() // drain

	// Now snapshot again — fire1 should not appear.
	snap := c.Snapshot()
	if _, ok := snap["fire1"]; ok {
		t.Fatal("snapshot should not include campfires with zero count")
	}
}

func TestUsageCollectorConcurrentSafety(t *testing.T) {
	c := meter.NewUsageCollector()

	const goroutines = 20
	const messages = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < messages; j++ {
				c.RecordMessage("fire1")
			}
		}()
	}
	wg.Wait()

	snap := c.Snapshot()
	if snap["fire1"] != goroutines*messages {
		t.Fatalf("expected %d, got %d", goroutines*messages, snap["fire1"])
	}
}

// --- MarketplaceClient tests ---

func TestMarketplaceClientPostUsage(t *testing.T) {
	srv := newCaptureServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{
		APIURL:        ts.URL,
		TokenProvider: &staticTokenProvider{token: "test-token"},
	})

	event := meter.UsageEvent{
		ResourceID:         "sub-123",
		Quantity:           42,
		Dimension:          "messages",
		EffectiveStartTime: "2024-01-01T12:00:00Z",
		PlanID:             "free",
	}

	if err := client.PostUsage(context.Background(), event); err != nil {
		t.Fatalf("PostUsage failed: %v", err)
	}

	events := srv.captured()
	if len(events) != 1 {
		t.Fatalf("expected 1 captured event, got %d", len(events))
	}

	got := events[0]
	if got.ResourceID != event.ResourceID {
		t.Errorf("ResourceID: want %q, got %q", event.ResourceID, got.ResourceID)
	}
	if got.Quantity != event.Quantity {
		t.Errorf("Quantity: want %v, got %v", event.Quantity, got.Quantity)
	}
	if got.Dimension != event.Dimension {
		t.Errorf("Dimension: want %q, got %q", event.Dimension, got.Dimension)
	}
	if got.EffectiveStartTime != event.EffectiveStartTime {
		t.Errorf("EffectiveStartTime: want %q, got %q", event.EffectiveStartTime, got.EffectiveStartTime)
	}
	if got.PlanID != event.PlanID {
		t.Errorf("PlanID: want %q, got %q", event.PlanID, got.PlanID)
	}
}

func TestMarketplaceClientAuthHeader(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{
		APIURL:        ts.URL,
		TokenProvider: &staticTokenProvider{token: "my-secret-token"},
	})

	if err := client.PostUsage(context.Background(), meter.UsageEvent{}); err != nil {
		t.Fatalf("PostUsage failed: %v", err)
	}
	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("want Authorization %q, got %q", "Bearer my-secret-token", gotAuth)
	}
}

func TestMarketplaceClientConflictIsSuccess(t *testing.T) {
	// 409 Conflict = duplicate event, treated as success (idempotent).
	srv := newCaptureServer()
	srv.status = http.StatusConflict
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{APIURL: ts.URL})
	if err := client.PostUsage(context.Background(), meter.UsageEvent{}); err != nil {
		t.Fatalf("409 should be treated as success, got: %v", err)
	}
}

func TestMarketplaceClientErrorOnBadStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{APIURL: ts.URL})
	if err := client.PostUsage(context.Background(), meter.UsageEvent{}); err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

func TestMarketplaceClientNoTokenProvider(t *testing.T) {
	// No token provider — Authorization header should not be set.
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{APIURL: ts.URL})
	if err := client.PostUsage(context.Background(), meter.UsageEvent{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header, got %q", gotAuth)
	}
}

// --- Emitter integration tests ---

// TestEmitterEmitsOnInterval verifies that the emitter fires on the configured
// interval and posts usage events for each campfire with messages.
func TestEmitterEmitsOnInterval(t *testing.T) {
	srv := newCaptureServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	collector := meter.NewUsageCollector()
	collector.RecordMessage("fire1")
	collector.RecordMessage("fire1")
	collector.RecordMessage("fire2")

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{APIURL: ts.URL})

	ctx, cancel := context.WithCancel(context.Background())

	fired := make(chan struct{}, 1)
	emitter := meter.NewEmitter(meter.EmitterConfig{
		Collector:  collector,
		Client:     client,
		ResourceID: "sub-abc",
		PlanID:     "free",
		Dimension:  "messages",
		Interval:   20 * time.Millisecond, // fast interval for test
		Now: func() time.Time {
			return time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
		},
		OnError: func(campfireID string, err error) {
			// Ignore errors from in-flight requests after context cancellation.
			if ctx.Err() != nil {
				return
			}
			t.Errorf("unexpected error for %s: %v", campfireID, err)
		},
	})
	go func() {
		emitter.Run(ctx)
		close(fired)
	}()

	// Wait for at least one emission.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(srv.captured()) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-fired

	events := srv.captured()
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (one per campfire), got %d", len(events))
	}

	// Verify event fields.
	byID := make(map[string]meter.UsageEvent)
	for _, ev := range events {
		byID[ev.ResourceID] = ev
	}

	ev := events[0]
	if ev.Dimension != "messages" {
		t.Errorf("Dimension: want messages, got %q", ev.Dimension)
	}
	if ev.PlanID != "free" {
		t.Errorf("PlanID: want free, got %q", ev.PlanID)
	}
	if ev.EffectiveStartTime == "" {
		t.Error("EffectiveStartTime should not be empty")
	}
}

// TestEmitterEmitNow verifies that EmitNow posts current counts immediately.
func TestEmitterEmitNow(t *testing.T) {
	srv := newCaptureServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	collector := meter.NewUsageCollector()
	collector.RecordMessage("fire1")
	collector.RecordMessage("fire1")
	collector.RecordMessage("fire1")

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{APIURL: ts.URL})
	emitter := meter.NewEmitter(meter.EmitterConfig{
		Collector: collector,
		Client:    client,
		PlanID:    "paid",
		Now: func() time.Time {
			return time.Date(2024, 6, 15, 9, 30, 0, 0, time.UTC)
		},
	})

	emitter.EmitNow(context.Background())

	events := srv.captured()
	if len(events) != 1 {
		t.Fatalf("expected 1 event from EmitNow, got %d", len(events))
	}

	ev := events[0]
	if ev.Quantity != 3 {
		t.Errorf("Quantity: want 3, got %v", ev.Quantity)
	}
	// effectiveStartTime should be truncated to hour: 09:00
	if ev.EffectiveStartTime != "2024-06-15T09:00:00Z" {
		t.Errorf("EffectiveStartTime: want 2024-06-15T09:00:00Z, got %q", ev.EffectiveStartTime)
	}
	// Default dimension.
	if ev.Dimension != "messages" {
		t.Errorf("Dimension: want messages, got %q", ev.Dimension)
	}
}

// TestEmitterEmitNowSkipsZeroCounts verifies that campfires with no messages
// are not included in emissions.
func TestEmitterEmitNowSkipsZeroCounts(t *testing.T) {
	srv := newCaptureServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	collector := meter.NewUsageCollector()
	// No messages recorded.

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{APIURL: ts.URL})
	emitter := meter.NewEmitter(meter.EmitterConfig{
		Collector: collector,
		Client:    client,
	})

	emitter.EmitNow(context.Background())

	if len(srv.captured()) != 0 {
		t.Fatalf("expected 0 events for empty collector, got %d", len(srv.captured()))
	}
}

// TestEmitterOnErrorCallback verifies that the OnError callback is invoked when
// the Marketplace API returns an error.
func TestEmitterOnErrorCallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	collector := meter.NewUsageCollector()
	collector.RecordMessage("fire1")

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{APIURL: ts.URL})

	var mu sync.Mutex
	var errored []string
	emitter := meter.NewEmitter(meter.EmitterConfig{
		Collector: collector,
		Client:    client,
		OnError: func(campfireID string, err error) {
			mu.Lock()
			errored = append(errored, campfireID)
			mu.Unlock()
		},
	})

	emitter.EmitNow(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(errored) != 1 || errored[0] != "fire1" {
		t.Fatalf("expected OnError called with fire1, got %v", errored)
	}
}

// TestEmitterUsageEventPayloadFormat verifies the exact JSON payload sent to the API.
func TestEmitterUsageEventPayloadFormat(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received = b
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	collector := meter.NewUsageCollector()
	collector.RecordMessage("fire1")

	client := meter.NewMarketplaceClient(meter.MarketplaceClientConfig{APIURL: ts.URL})
	emitter := meter.NewEmitter(meter.EmitterConfig{
		Collector:  collector,
		Client:     client,
		ResourceID: "my-resource",
		PlanID:     "enterprise",
		Dimension:  "messages",
		Now: func() time.Time {
			return time.Date(2024, 3, 10, 14, 45, 0, 0, time.UTC)
		},
	})

	emitter.EmitNow(context.Background())

	var ev meter.UsageEvent
	if err := json.Unmarshal(received, &ev); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if ev.ResourceID != "my-resource" {
		t.Errorf("ResourceID: want my-resource, got %q", ev.ResourceID)
	}
	if ev.Quantity != 1 {
		t.Errorf("Quantity: want 1, got %v", ev.Quantity)
	}
	if ev.Dimension != "messages" {
		t.Errorf("Dimension: want messages, got %q", ev.Dimension)
	}
	if ev.EffectiveStartTime != "2024-03-10T14:00:00Z" {
		t.Errorf("EffectiveStartTime: want 2024-03-10T14:00:00Z, got %q", ev.EffectiveStartTime)
	}
	if ev.PlanID != "enterprise" {
		t.Errorf("PlanID: want enterprise, got %q", ev.PlanID)
	}
}
