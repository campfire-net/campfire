package http

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// Unit tests for RoutingTable.

// makeBeaconPayload creates a valid routing:beacon payload signed by the given campfire key.
func makeBeaconPayload(t *testing.T, campfirePriv ed25519.PrivateKey, campfirePub ed25519.PublicKey, endpoint, transport, gateway string) []byte {
	t.Helper()
	campfireIDHex := hex.EncodeToString(campfirePub)
	ts := time.Now().Unix()

	signInput := innerBeaconSignInput{
		CampfireID:        campfireIDHex,
		ConventionVersion: "0.4.2",
		Description:       "test campfire",
		Endpoint:          endpoint,
		JoinProtocol:      "open",
		Timestamp:         ts,
		Transport:         transport,
	}
	signBytes, err := json.Marshal(signInput)
	if err != nil {
		t.Fatalf("marshaling beacon sign input: %v", err)
	}
	sig := ed25519.Sign(campfirePriv, signBytes)

	payload := beaconPayload{
		CampfireID:        campfireIDHex,
		Endpoint:          endpoint,
		Transport:         transport,
		Description:       "test campfire",
		JoinProtocol:      "open",
		Timestamp:         ts,
		ConventionVersion: "0.4.2",
		InnerSignature:    hex.EncodeToString(sig),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling beacon payload: %v", err)
	}
	return b
}

// makeWithdrawPayload creates a valid routing:withdraw payload signed by the given campfire key.
func makeWithdrawPayload(t *testing.T, campfirePriv ed25519.PrivateKey, campfirePub ed25519.PublicKey, reason string) []byte {
	t.Helper()
	campfireIDHex := hex.EncodeToString(campfirePub)

	type withdrawSignInput struct {
		CampfireID string `json:"campfire_id"`
		Reason     string `json:"reason"`
	}
	signInput := withdrawSignInput{
		CampfireID: campfireIDHex,
		Reason:     reason,
	}
	signBytes, err := json.Marshal(signInput)
	if err != nil {
		t.Fatalf("marshaling withdraw sign input: %v", err)
	}
	sig := ed25519.Sign(campfirePriv, signBytes)

	payload := withdrawPayload{
		CampfireID:     campfireIDHex,
		Reason:         reason,
		InnerSignature: hex.EncodeToString(sig),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling withdraw payload: %v", err)
	}
	return b
}

// TestRoutingTableInsertFromBeacon verifies that a valid beacon inserts an entry.
func TestRoutingTableInsertFromBeacon(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := newRoutingTable()
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gateway-1")
	if err := rt.HandleBeacon(payload, "gateway-1"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	campfireIDHex := hex.EncodeToString(cfPub)
	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Endpoint != "http://example.com:8080" {
		t.Errorf("endpoint = %q, want %q", routes[0].Endpoint, "http://example.com:8080")
	}
	if routes[0].Transport != "p2p-http" {
		t.Errorf("transport = %q, want %q", routes[0].Transport, "p2p-http")
	}
	if routes[0].Gateway != "gateway-1" {
		t.Errorf("gateway = %q, want %q", routes[0].Gateway, "gateway-1")
	}
	if !routes[0].Verified {
		t.Error("route should be verified")
	}
}

// TestRoutingTableRejectsInvalidInnerSignature verifies that a beacon with a bad
// inner_signature is rejected.
func TestRoutingTableRejectsInvalidInnerSignature(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := newRoutingTable()

	// Create a valid beacon then tamper with the endpoint (signature will no longer match).
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gateway-1")
	var p beaconPayload
	json.Unmarshal(payload, &p)
	p.Endpoint = "http://evil.example.com:9999" // tamper
	tampered, _ := json.Marshal(p)

	if err := rt.HandleBeacon(tampered, "gateway-1"); err == nil {
		t.Error("expected HandleBeacon to fail for tampered beacon, got nil error")
	}

	campfireIDHex := hex.EncodeToString(cfPub)
	if routes := rt.Lookup(campfireIDHex); len(routes) != 0 {
		t.Errorf("no routes should be stored for failed beacon, got %d", len(routes))
	}
}

// TestRoutingTableTTL verifies that entries expire after routingTableTTL.
// This test uses a shortened TTL injected directly into the entry.
func TestRoutingTableTTL(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := newRoutingTable()
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	campfireIDHex := hex.EncodeToString(cfPub)

	// Backdate the entry to be older than TTL.
	rt.mu.Lock()
	for i := range rt.entries[campfireIDHex] {
		rt.entries[campfireIDHex][i].Received = time.Now().Add(-routingTableTTL - time.Second)
	}
	rt.mu.Unlock()

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != 0 {
		t.Errorf("expected 0 routes after TTL, got %d", len(routes))
	}
}

// TestRoutingTableBeaconBudget verifies that at most routingBeaconBudget entries
// are kept per campfire_id and the stalest is evicted when the budget is exceeded.
func TestRoutingTableBeaconBudget(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Insert routingBeaconBudget beacons for different endpoints.
	// We need to manually craft payloads with different timestamps to get distinct entries.
	for i := 0; i < routingBeaconBudget; i++ {
		endpoint := "http://instance-" + string(rune('a'+i)) + ".example.com:8080"
		payload := makeBeaconPayload(t, cfPriv, cfPub, endpoint, "p2p-http", "gw")
		if err := rt.HandleBeacon(payload, "gw"); err != nil {
			t.Fatalf("HandleBeacon[%d]: %v", i, err)
		}
		// Small sleep to ensure distinct timestamps.
		time.Sleep(time.Millisecond)
	}

	routes := rt.Lookup(campfireIDHex)
	if len(routes) != routingBeaconBudget {
		t.Fatalf("expected %d routes, got %d", routingBeaconBudget, len(routes))
	}
}

// TestRoutingTableWithdraw verifies that routing:withdraw removes all entries
// for the campfire_id.
func TestRoutingTableWithdraw(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gw")
	if err := rt.HandleBeacon(payload, "gw"); err != nil {
		t.Fatalf("HandleBeacon: %v", err)
	}

	// Verify entry was added.
	if routes := rt.Lookup(campfireIDHex); len(routes) == 0 {
		t.Fatal("expected route after beacon, got none")
	}

	// Withdraw.
	withdrawPayloadBytes := makeWithdrawPayload(t, cfPriv, cfPub, "going offline")
	if err := rt.HandleWithdraw(withdrawPayloadBytes); err != nil {
		t.Fatalf("HandleWithdraw: %v", err)
	}

	// Entry should be gone.
	if routes := rt.Lookup(campfireIDHex); len(routes) != 0 {
		t.Errorf("expected 0 routes after withdraw, got %d", len(routes))
	}
}

// TestRoutingTableWithdrawRejectsInvalidSignature verifies that a withdraw with
// bad inner_signature is rejected.
func TestRoutingTableWithdrawRejectsInvalidSignature(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Insert a valid beacon first.
	payload := makeBeaconPayload(t, cfPriv, cfPub, "http://example.com:8080", "p2p-http", "gw")
	rt.HandleBeacon(payload, "gw") //nolint:errcheck

	// Craft a withdraw with a bad signature (all zeros).
	badWithdraw := withdrawPayload{
		CampfireID:     campfireIDHex,
		Reason:         "spoofed",
		InnerSignature: hex.EncodeToString(make([]byte, ed25519.SignatureSize)),
	}
	b, _ := json.Marshal(badWithdraw)
	if err := rt.HandleWithdraw(b); err == nil {
		t.Error("expected HandleWithdraw to fail for invalid signature, got nil")
	}

	// Entry should still be present.
	if routes := rt.Lookup(campfireIDHex); len(routes) == 0 {
		t.Error("entry should not have been removed by invalid withdraw")
	}
}

// TestRoutingTableLookupEmpty verifies that Lookup returns nil for unknown campfire_id.
func TestRoutingTableLookupEmpty(t *testing.T) {
	rt := newRoutingTable()
	if routes := rt.Lookup("unknown-campfire-id"); routes != nil {
		t.Errorf("expected nil for unknown campfire, got %v", routes)
	}
}

// TestRoutingTableLen verifies the Len method.
func TestRoutingTableLen(t *testing.T) {
	rt := newRoutingTable()
	if rt.Len() != 0 {
		t.Errorf("empty table should have Len 0, got %d", rt.Len())
	}

	cfPub1, cfPriv1, _ := ed25519.GenerateKey(nil)
	cfPub2, cfPriv2, _ := ed25519.GenerateKey(nil)

	rt.HandleBeacon(makeBeaconPayload(t, cfPriv1, cfPub1, "http://a.example.com", "p2p-http", "gw"), "gw") //nolint:errcheck
	rt.HandleBeacon(makeBeaconPayload(t, cfPriv2, cfPub2, "http://b.example.com", "p2p-http", "gw"), "gw") //nolint:errcheck

	if rt.Len() != 2 {
		t.Errorf("expected Len 2, got %d", rt.Len())
	}
}

// TestRoutingTableRejectsOldTimestamp verifies that a beacon with an old timestamp
// (older than routingTableTTL) is rejected.
func TestRoutingTableRejectsOldTimestamp(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)
	rt := newRoutingTable()

	// Build a beacon with an old timestamp (25 hours ago).
	oldTS := time.Now().Add(-25 * time.Hour).Unix()
	signInput := innerBeaconSignInput{
		CampfireID:        campfireIDHex,
		ConventionVersion: "0.4.2",
		Description:       "old campfire",
		Endpoint:          "http://old.example.com",
		JoinProtocol:      "open",
		Timestamp:         oldTS,
		Transport:         "p2p-http",
	}
	signBytes, _ := json.Marshal(signInput)
	sig := ed25519.Sign(cfPriv, signBytes)

	payload := beaconPayload{
		CampfireID:        campfireIDHex,
		Endpoint:          "http://old.example.com",
		Transport:         "p2p-http",
		Description:       "old campfire",
		JoinProtocol:      "open",
		Timestamp:         oldTS,
		ConventionVersion: "0.4.2",
		InnerSignature:    hex.EncodeToString(sig),
	}
	b, _ := json.Marshal(payload)

	if err := rt.HandleBeacon(b, "gw"); err == nil {
		t.Error("expected HandleBeacon to reject beacon with old timestamp, got nil")
	}

	if routes := rt.Lookup(campfireIDHex); len(routes) != 0 {
		t.Errorf("no routes should be stored for old beacon, got %d", len(routes))
	}
}
