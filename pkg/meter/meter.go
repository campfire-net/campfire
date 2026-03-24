// Package meter implements Azure Marketplace Metering API integration for the
// campfire hosted service. It collects per-campfire message counts hourly and
// emits usage events to the Marketplace Metering API for billing purposes.
//
// Metering is reporting-only. Enforcement of message limits is handled by
// pkg/ratelimit.
package meter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// DefaultMeteringAPIURL is the Azure Marketplace Metering API endpoint.
const DefaultMeteringAPIURL = "https://marketplaceapi.microsoft.com/api/usageEvent?api-version=2018-08-31"

// UsageEvent is the payload sent to the Marketplace Metering API.
// See https://learn.microsoft.com/en-us/azure/marketplace/marketplace-metering-service-apis
type UsageEvent struct {
	// ResourceID is the Azure managed application resource ID or subscription resource ID.
	ResourceID string `json:"resourceId"`
	// Quantity is the number of units consumed in the period.
	Quantity float64 `json:"quantity"`
	// Dimension is the custom meter dimension name (e.g. "messages").
	Dimension string `json:"dimension"`
	// EffectiveStartTime is the ISO 8601 UTC start time of the usage period (top of the hour).
	EffectiveStartTime string `json:"effectiveStartTime"`
	// PlanID is the Marketplace plan identifier.
	PlanID string `json:"planId"`
}

// TokenProvider returns an Azure AD Bearer token for the Metering API.
// In production this is backed by a managed identity credential.
// In tests this can be a stub that returns a static token.
type TokenProvider interface {
	// Token returns a valid Bearer token string.
	Token(ctx context.Context) (string, error)
}

// MarketplaceClient posts usage events to the Azure Marketplace Metering API.
// It is safe for concurrent use.
type MarketplaceClient struct {
	apiURL        string
	tokenProvider TokenProvider
	httpClient    *http.Client
}

// MarketplaceClientConfig holds configuration for MarketplaceClient.
type MarketplaceClientConfig struct {
	// APIURL overrides the Metering API endpoint. Defaults to DefaultMeteringAPIURL.
	// Set to a mock server URL in tests.
	APIURL string

	// TokenProvider supplies Azure AD Bearer tokens.
	TokenProvider TokenProvider

	// HTTPClient overrides the HTTP client. Defaults to http.DefaultClient.
	HTTPClient *http.Client
}

// NewMarketplaceClient creates a MarketplaceClient from the given config.
func NewMarketplaceClient(cfg MarketplaceClientConfig) *MarketplaceClient {
	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = DefaultMeteringAPIURL
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &MarketplaceClient{
		apiURL:        apiURL,
		tokenProvider: cfg.TokenProvider,
		httpClient:    httpClient,
	}
}

// PostUsage sends a single usage event to the Marketplace Metering API.
// The Marketplace API is idempotent: duplicate events for the same resourceId,
// dimension, and effectiveStartTime are accepted (deduplicated server-side).
func (c *MarketplaceClient) PostUsage(ctx context.Context, event UsageEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("meter: marshal usage event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("meter: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if c.tokenProvider != nil {
		token, err := c.tokenProvider.Token(ctx)
		if err != nil {
			return fmt.Errorf("meter: get token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("meter: post usage event: %w", err)
	}
	defer resp.Body.Close()
	// Drain body to allow connection reuse.
	_, _ = io.Copy(io.Discard, resp.Body)

	// 200 OK or 409 Conflict (duplicate) are both success cases.
	// 409 means the Marketplace already has this event — idempotent.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("meter: unexpected status %d from metering API", resp.StatusCode)
}

// UsageCollector tracks per-campfire message counts for the current hour.
// Call RecordMessage each time a message is accepted. The Emitter drains and
// resets the counters at the top of each hour.
//
// UsageCollector is safe for concurrent use.
type UsageCollector struct {
	mu     sync.Mutex
	counts map[string]int64 // campfireID → message count this hour
}

// NewUsageCollector creates an empty UsageCollector.
func NewUsageCollector() *UsageCollector {
	return &UsageCollector{
		counts: make(map[string]int64),
	}
}

// RecordMessage increments the hourly message count for the given campfire.
// Call this whenever a message is successfully accepted (after rate limit checks pass).
func (c *UsageCollector) RecordMessage(campfireID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[campfireID]++
}

// Snapshot returns a copy of the current per-campfire counts and resets the
// internal counters to zero. The returned map is safe to use after the call.
func (c *UsageCollector) Snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := make(map[string]int64, len(c.counts))
	for id, n := range c.counts {
		if n > 0 {
			snap[id] = n
		}
	}
	// Reset.
	c.counts = make(map[string]int64)
	return snap
}

// Count returns the current (non-destructive) hourly count for a campfire.
// Useful for monitoring; use Snapshot for emission.
func (c *UsageCollector) Count(campfireID string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[campfireID]
}

// EmitterConfig holds configuration for the hourly emission loop.
type EmitterConfig struct {
	// Collector is the UsageCollector to read from. Required.
	Collector *UsageCollector

	// Client is the MarketplaceClient to post to. Required.
	Client *MarketplaceClient

	// ResourceID is the Azure resource ID for all emitted events.
	ResourceID string

	// PlanID is the Marketplace plan ID for all emitted events.
	PlanID string

	// Dimension is the metering dimension name. Defaults to "messages".
	Dimension string

	// Interval overrides the emission interval. Defaults to 1 hour.
	// Set to a shorter value in tests.
	Interval time.Duration

	// Now is the clock function. Defaults to time.Now.
	Now func() time.Time

	// OnError is called when a usage event fails to post. If nil, errors are
	// silently discarded (the next hourly emission will retry with fresh counts).
	OnError func(campfireID string, err error)
}

func (cfg *EmitterConfig) dimension() string {
	if cfg.Dimension != "" {
		return cfg.Dimension
	}
	return "messages"
}

func (cfg *EmitterConfig) interval() time.Duration {
	if cfg.Interval > 0 {
		return cfg.Interval
	}
	return time.Hour
}

func (cfg *EmitterConfig) now() time.Time {
	if cfg.Now != nil {
		return cfg.Now()
	}
	return time.Now().UTC()
}

// Emitter runs the hourly usage emission loop. Start it with Run.
type Emitter struct {
	cfg EmitterConfig
}

// NewEmitter creates an Emitter. cfg.Collector and cfg.Client are required.
func NewEmitter(cfg EmitterConfig) *Emitter {
	return &Emitter{cfg: cfg}
}

// Run starts the hourly emission loop. It blocks until ctx is cancelled.
// Run fires at the top of each hour (aligned to wall clock), or at cfg.Interval
// for test overrides.
//
// On each tick: collect per-campfire counts, post one UsageEvent per campfire,
// discard campfires with zero messages. Failed posts trigger cfg.OnError (if set).
func (e *Emitter) Run(ctx context.Context) {
	for {
		// Wait until the next tick.
		waitDur := e.nextTick()
		select {
		case <-ctx.Done():
			return
		case <-time.After(waitDur):
		}
		e.emit(ctx)
	}
}

// nextTick returns the duration until the next emission. If Interval is set
// (non-hour override), it returns that interval. Otherwise it aligns to the
// top of the next hour.
func (e *Emitter) nextTick() time.Duration {
	if e.cfg.Interval > 0 {
		return e.cfg.Interval
	}
	// Align to top of next hour.
	now := e.cfg.now()
	next := now.Truncate(time.Hour).Add(time.Hour)
	return next.Sub(now)
}

// emit collects current counts and posts one usage event per campfire.
func (e *Emitter) emit(ctx context.Context) {
	snapshot := e.cfg.Collector.Snapshot()
	if len(snapshot) == 0 {
		return
	}
	// effectiveStartTime is the top of the most recently completed hour.
	startTime := e.cfg.now().Truncate(time.Hour).Format(time.RFC3339)

	for campfireID, count := range snapshot {
		event := UsageEvent{
			ResourceID:         e.cfg.ResourceID,
			Quantity:           float64(count),
			Dimension:          e.cfg.dimension(),
			EffectiveStartTime: startTime,
			PlanID:             e.cfg.PlanID,
		}
		// Use campfireID as resource sub-identifier if no global ResourceID.
		if event.ResourceID == "" {
			event.ResourceID = campfireID
		}
		if err := e.cfg.Client.PostUsage(ctx, event); err != nil {
			if e.cfg.OnError != nil {
				e.cfg.OnError(campfireID, err)
			}
		}
	}
}

// EmitNow triggers an immediate emission outside the normal schedule.
// Useful for graceful shutdown — emit whatever is in the collector before exit.
func (e *Emitter) EmitNow(ctx context.Context) {
	e.emit(ctx)
}
