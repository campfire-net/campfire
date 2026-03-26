package http

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/campfire-net/campfire/pkg/beacon"
)

// TestBeaconSignDeclarationRoundtrip is a cross-package integration test that
// signs a beacon using beacon.SignDeclaration and verifies it via
// RoutingTable.HandleBeacon. It catches inner_signature field ordering
// mismatches between the signing and verification paths.
func TestBeaconSignDeclarationRoundtrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	// Sign using the canonical beacon package function.
	decl, err := beacon.SignDeclaration(
		pub, priv,
		"https://relay.example.com",
		"p2p-http",
		"integration test campfire",
		"open",
	)
	if err != nil {
		t.Fatalf("SignDeclaration: %v", err)
	}

	// Marshal the declaration to JSON (as it would arrive in a routing:beacon message).
	rawPayload, err := json.Marshal(decl)
	if err != nil {
		t.Fatalf("marshaling declaration: %v", err)
	}

	// Verify via HandleBeacon — this is the path used by the router.
	rt := newRoutingTable()
	if err := rt.HandleBeacon(rawPayload, "gateway-integration-test"); err != nil {
		t.Fatalf("HandleBeacon rejected a beacon signed by SignDeclaration: %v", err)
	}

	// Confirm the entry was actually inserted with verified=true.
	routes := rt.Lookup(decl.CampfireID)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if !routes[0].Verified {
		t.Error("route Verified should be true")
	}
	if routes[0].Endpoint != "https://relay.example.com" {
		t.Errorf("endpoint = %q, want %q", routes[0].Endpoint, "https://relay.example.com")
	}
}
