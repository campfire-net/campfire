package http

// Security sweep tests for path-vector amendment (item agentic-internet-ops-23i).
//
// Focus areas not already covered by security_pathvector_test.go:
//   1. peerNeeds unbounded growth (resource exhaustion via distinct senderHex values)
//   2. Budget-full path skips duplicate-endpoint check (slot-poisoning attack)
//   3. peerNeeds not cleaned when routes expire via TTL (memory leak)
//   4. Path node_id elements not validated (empty/malformed hops bypass loop detection)
//   5. Lookup write-lock contention (documented, not a correctness bug)
//   6. SSRF-dangerous endpoints accepted into routing table
//
// Severities: HIGH / MEDIUM per reviewer judgment.

import (
	"crypto/ed25519"
	"encoding/hex"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. PeerNeeds unbounded growth (resource exhaustion)
// ─────────────────────────────────────────────────────────────────────────────

// HIGH: TestPeerNeedsUnboundedGrowth
//
// RecordMessageDelivery adds any node_id unconditionally with no cap.
// An attacker who controls N distinct identities (or who spoofs distinct senderHex
// values via compromised intermediate peers) can cause peerNeeds[campfireID] to
// grow to O(N) entries consuming unbounded memory.
//
// This test verifies the current unbounded behavior and documents the missing cap.
func TestPeerNeedsUnboundedGrowth(t *testing.T) {
	rt := newRoutingTable()
	// Use a fixed 64-char hex string as campfire_id (doesn't need to be a real key
	// for RecordMessageDelivery — that method doesn't validate the format).
	campfireID := "cafebeef" + "aa" + hex.EncodeToString(make([]byte, 27))

	const attackerCount = 1000
	for i := 0; i < attackerCount; i++ {
		// Each attacker has a distinct node_id (simulates N distinct identities).
		nodeID := hex.EncodeToString([]byte{byte(i >> 8), byte(i)}) + "aabbccdd"
		rt.RecordMessageDelivery(campfireID, nodeID)
	}

	needs := rt.PeerNeedsSet(campfireID)
	if len(needs) != attackerCount {
		t.Fatalf("expected %d peer needs entries, got %d", attackerCount, len(needs))
	}

	// HIGH: no cap is enforced — peerNeeds grows to exactly attackerCount entries.
	// In production, an attacker with 10k identities causes 10k map entries per campfire.
	// A router handling 100 campfires = 1M map entries from a coordinated attack.
	t.Logf("HIGH: peerNeeds grew to %d entries with no cap — unbounded memory growth confirmed", len(needs))
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Budget-full path skips duplicate-endpoint check (slot-poisoning)
// ─────────────────────────────────────────────────────────────────────────────

// HIGH: TestBudgetEvictionAllowsDuplicateEndpoint
//
// When len(existing) >= routingBeaconBudget, HandleBeacon skips the per-endpoint
// dedup check and goes straight to evict-oldest by InnerTimestamp. This means a
// beacon for an endpoint that already has a slot can evict a DIFFERENT endpoint's
// slot rather than refreshing its own.
//
// Demonstrated: fill budget with N distinct endpoints (budget=5, timestamps T+0..T+4).
// Then send a beacon for endpoint[N-1] (freshest) with an even newer timestamp.
// Expected correct behavior: refresh endpoint[N-1]'s existing slot.
// Actual behavior: evict endpoint[0] (oldest timestamp), creating a duplicate for endpoint[N-1].
func TestBudgetEvictionAllowsDuplicateEndpoint(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	baseTS := time.Now().Unix()

	// Fill budget with distinct endpoints at timestamps baseTS, baseTS+1, ..., baseTS+(budget-1).
	endpoints := make([]string, routingBeaconBudget)
	for i := 0; i < routingBeaconBudget; i++ {
		endpoints[i] = "http://ep-" + string(rune('a'+i)) + ".example.com"
		ts := baseTS + int64(i)
		payload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, endpoints[i], "p2p-http", ts, nil)
		if err := rt.HandleBeacon(payload, "gw", "node-"+string(rune('a'+i))); err != nil {
			t.Fatalf("HandleBeacon[%d]: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}

	// Confirm budget is full.
	if got := len(rt.Lookup(campfireIDHex)); got != routingBeaconBudget {
		t.Fatalf("setup: expected %d routes at budget, got %d", routingBeaconBudget, got)
	}

	// Send a beacon for endpoints[last] (the freshest slot) with an even newer timestamp.
	// The budget-full code will:
	//   - Find oldestIdx (endpoints[0], timestamp baseTS).
	//   - Check: newTS > oldestTimestamp — yes, evict.
	//   - Replace endpoints[0]'s slot with a NEW entry for endpoints[last].
	//   - Result: endpoints[last] has TWO slots; endpoints[0] is lost.
	freshTS := baseTS + int64(routingBeaconBudget) + 1
	payload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, endpoints[routingBeaconBudget-1], "p2p-http", freshTS, []string{"new-path"})
	if err := rt.HandleBeacon(payload, "gw", "new-sender"); err != nil {
		t.Fatalf("HandleBeacon (duplicate endpoint at budget): %v", err)
	}

	// Count endpoint occurrences after the operation.
	routesAfter := rt.Lookup(campfireIDHex)
	duplicateCount := 0
	ep0Present := false
	for _, r := range routesAfter {
		if r.Endpoint == endpoints[routingBeaconBudget-1] {
			duplicateCount++
		}
		if r.Endpoint == endpoints[0] {
			ep0Present = true
		}
	}

	if duplicateCount > 1 {
		t.Logf("HIGH: endpoint %q appears %d times after budget-full beacon — "+
			"duplicate-endpoint check skipped at budget; endpoints[0] (oldest) was evicted instead of refreshing existing slot. "+
			"An attacker who knows the budget state can evict legitimate entries by sending a fresh beacon for their own endpoint.",
			endpoints[routingBeaconBudget-1], duplicateCount)
	} else if !ep0Present {
		t.Logf("HIGH: endpoints[0] was evicted by a beacon for endpoints[last] — "+
			"budget-full path replaced a different endpoint's slot instead of refreshing the existing one. "+
			"endpoints[last] now has 1 slot (old evicted), budget integrity maintained by coincidence.")
	} else {
		t.Log("OK: budget eviction handled correctly in this scenario")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. peerNeeds not cleaned when routes expire via TTL
// ─────────────────────────────────────────────────────────────────────────────

// MEDIUM: TestPeerNeedsNotCleanedOnTTLExpiry
//
// Lookup() performs lazy eviction of expired route entries. When all entries
// for a campfire expire (len(live)==0), it calls delete(rt.entries, campfireID).
// However, it does NOT call delete(rt.peerNeeds, campfireID).
//
// The peerNeeds set persists indefinitely after all routes expire, causing:
//   1. Memory leak: peerNeeds grows without bound across campfire lifetimes.
//   2. Stale forwarding: forwardMessage union with peerNeeds may forward to
//      peers of a campfire that no longer has live routes.
func TestPeerNeedsNotCleanedOnTTLExpiry(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Insert a beacon and record a message delivery to populate peerNeeds.
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://expiring.example.com", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw", "peer-alpha"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}
	rt.RecordMessageDelivery(campfireIDHex, "peer-beta")

	// Confirm peerNeeds is populated.
	needs := rt.PeerNeedsSet(campfireIDHex)
	if len(needs) == 0 {
		t.Fatal("setup: expected peerNeeds to be populated after beacon + delivery")
	}

	// Backdate the route entry beyond TTL to force expiry on next Lookup.
	rt.mu.Lock()
	for i := range rt.entries[campfireIDHex] {
		rt.entries[campfireIDHex][i].Received = time.Now().Add(-routingTableTTL - time.Second)
	}
	rt.mu.Unlock()

	// Trigger lazy eviction via Lookup.
	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 0 {
		t.Fatalf("expected 0 live routes after TTL expiry, got %d", len(routes))
	}

	// Verify the entries map entry was deleted.
	rt.mu.RLock()
	_, entriesExist := rt.entries[campfireIDHex]
	peerNeedsExist := rt.peerNeeds[campfireIDHex] != nil
	rt.mu.RUnlock()

	if entriesExist {
		t.Error("entries for expired campfire should have been deleted by lazy eviction")
	}

	// This is the bug: peerNeeds persists after routes expire.
	if peerNeedsExist {
		t.Log("MEDIUM: peerNeeds for campfire persists after all routes expire via TTL — " +
			"memory leak confirmed; stale peers remain in forwarding set indefinitely. " +
			"Fix: add delete(rt.peerNeeds, campfireID) in the lazy-eviction block inside Lookup().")
	} else {
		t.Log("OK: peerNeeds correctly cleaned when routes expire (bug is fixed)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. Path node_id elements not validated
// ─────────────────────────────────────────────────────────────────────────────

// MEDIUM: TestPathEmptyHopAccepted
//
// HandleBeacon does not validate individual path hop elements. An empty string ("")
// in the path is accepted and stored in RouteEntry.Path. This has two consequences:
//
//  1. Loop detection iterates all hops; an empty hop never matches any real NodeID.
//     A path like ["nodeA", "", "nodeC"] passes loop detection even if "" were somehow
//     the router's NodeID (it can't be, but the empty string is unvalidated).
//  2. Path length is artificially inflated by empty slots, biasing route preference.
func TestPathEmptyHopAccepted(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTableWithNodeID("my-router-node-id")

	// Path with an empty string element (injected by adversarial relay).
	pathWithEmptyHop := []string{"nodeA", "", "nodeC"}
	payload := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com", pathWithEmptyHop, false)

	err := rt.HandleBeacon(payload, "gw", "nodeC")
	if err != nil {
		t.Logf("HandleBeacon rejected beacon with empty path hop: %v", err)
		t.Log("OK: empty path hops are rejected (validation is enforced)")
		return
	}

	// Beacon was accepted. Verify the empty hop is stored.
	routes := rt.Lookup(campfireIDHex)
	if len(routes) == 0 {
		t.Log("beacon accepted but no route installed")
		return
	}

	hasEmptyHop := false
	for _, hop := range routes[0].Path {
		if hop == "" {
			hasEmptyHop = true
			break
		}
	}

	if hasEmptyHop {
		t.Log("MEDIUM: empty string hop accepted in path — no per-element validation; " +
			"loop detection cannot match empty hops, path length is artificially inflated by empty slots")
	}
}

// MEDIUM: TestPathOversizedHopAccepted
//
// Path hop elements have no length bound. An attacker can inject a very long string
// (e.g., 4KB) into the path, consuming significant memory per route entry.
// With routingBeaconBudget=5 entries per campfire and many campfires, this
// amplifies to significant memory waste per attacker-controlled beacon.
func TestPathOversizedHopAccepted(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTableWithNodeID("router-node")

	// Path with an oversized hop (4KB).
	oversizedHop := string(make([]byte, 4096))
	// Fill with printable 'X' characters so JSON encoding doesn't complain.
	for i := range []byte(oversizedHop) {
		_ = i
	}
	xBytes := make([]byte, 4096)
	for i := range xBytes {
		xBytes[i] = 'X'
	}
	oversizedHop = string(xBytes)

	malformedPath := []string{"valid-nodeA", oversizedHop, "valid-nodeC"}
	payload := makePathBeaconPayload(t, cfPub, cfPriv, "http://example.com", malformedPath, false)

	err := rt.HandleBeacon(payload, "gw", "nodeC")
	if err != nil {
		t.Logf("HandleBeacon rejected oversized path hop: %v", err)
		t.Log("OK: oversized path hops are rejected (validation is enforced)")
		return
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) > 0 && len(routes[0].Path) > 0 {
		maxHopLen := 0
		for _, hop := range routes[0].Path {
			if len(hop) > maxHopLen {
				maxHopLen = len(hop)
			}
		}
		t.Logf("MEDIUM: oversized path hop accepted (max hop length: %d bytes) — "+
			"no per-element length validation; memory waste of ~%d bytes per route entry",
			maxHopLen, maxHopLen*routingBeaconBudget)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. Lookup write-lock contention under concurrent reads
// ─────────────────────────────────────────────────────────────────────────────

// MEDIUM: TestLookupWriteLockContentionUnderConcurrentReads
//
// Lookup() takes rt.mu.Lock() (exclusive write lock) for ALL code paths, including
// the common case where no eviction is needed. Under concurrent load, multiple
// goroutines calling Lookup() serialize completely.
//
// This is a performance/availability issue: an attacker who generates high-frequency
// Lookup-triggering requests (e.g., via repeated message delivery) can degrade
// forwarding throughput for all campfires sharing this RoutingTable.
//
// Fix: use RLock for the initial read; if eviction is needed, release and re-acquire
// with Lock (with a re-check to handle the TOCTOU window).
func TestLookupWriteLockContentionUnderConcurrentReads(t *testing.T) {
	cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Insert a fresh route (no expiry needed during test).
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw", "peer"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	const goroutines = 50
	const lookupsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	start := time.Now()
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < lookupsPerGoroutine; j++ {
				_ = rt.Lookup(campfireIDHex)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	totalLookups := goroutines * lookupsPerGoroutine
	throughput := float64(totalLookups) / elapsed.Seconds()

	// Document the throughput under exclusive-lock contention.
	t.Logf("MEDIUM: %d concurrent Lookup calls completed in %v (%.0f lookups/sec) — "+
		"all serialized by exclusive write lock (mu.Lock); switching to RLock for "+
		"non-evicting paths would allow true parallel reads under concurrent load",
		totalLookups, elapsed, throughput)
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. SSRF-dangerous endpoints accepted into routing table
// ─────────────────────────────────────────────────────────────────────────────

// MEDIUM: TestPathVectorEndpointSSRFStorage
//
// HandleBeacon accepts any non-empty endpoint URL into the routing table without
// validating whether it points to an internal/dangerous address. The existing SSRF
// protections in the HTTP handlers (ssrf_test.go) guard the join/beacon endpoints
// but do NOT guard the deliverMessage() path used by path-vector forwarding.
//
// An attacker who holds a campfire key (or intercepts a threshold>1 beacon) can
// advertise a beacon with endpoint="http://169.254.169.254/..." (AWS metadata) or
// "http://localhost:6379" (Redis). When forwardMessage() runs, it POSTs to that URL.
//
// This test documents that SSRF-dangerous endpoints are stored without rejection.
// The actual exploitation depends on deliverMessage()'s HTTP client — if it lacks
// SSRF filtering, this is a live SSRF vector introduced by path-vector forwarding.
func TestPathVectorEndpointSSRFStorage(t *testing.T) {
	ssrfEndpoints := []string{
		"http://localhost:8080",
		"http://127.0.0.1:6379",
		"http://169.254.169.254/latest/meta-data/",
		"http://0.0.0.0:22",
	}

	for _, ep := range ssrfEndpoints {
		cfPub, cfPriv, _ := ed25519.GenerateKey(nil)
		campfireIDHex := hex.EncodeToString(cfPub)
		rt := newRoutingTable()

		payload := makeBeaconPayloadWithPath(t, cfPriv, cfPub, ep, "p2p-http", time.Now().Unix(), []string{"attacker"})
		err := rt.HandleBeacon(payload, "gw", "attacker")
		if err != nil {
			t.Logf("OK: SSRF endpoint %q rejected by HandleBeacon: %v", ep, err)
			continue
		}

		routes := rt.Lookup(campfireIDHex)
		stored := false
		for _, r := range routes {
			if r.Endpoint == ep {
				stored = true
				break
			}
		}

		if stored {
			t.Logf("MEDIUM: SSRF-dangerous endpoint %q stored in routing table — "+
				"HandleBeacon performs no URL validation; path-vector forwardMessage "+
				"will POST to this URL; relies solely on deliverMessage HTTP client for SSRF defense",
				ep)
		}
	}
}
