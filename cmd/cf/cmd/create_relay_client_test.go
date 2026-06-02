package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
	"github.com/campfire-net/campfire/pkg/identity"
)

// TestClientCreate_Relay exercises protocol.Client.Create's relay path
// (P2PHTTPTransport.RelayEndpoint, campfireagent-bec) end-to-end against the
// in-process relay harness: it must register on the relay, record a p2p-http
// membership + the relay as a peer endpoint, publish a local beacon, and thread
// the relay endpoint back on CreateResult — all WITHOUT a running cfhttp.Transport
// (the creator is a relay client, not a host).
func TestClientCreate_Relay(t *testing.T) {
	relay := newTestRelay(t)

	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(&http.Client{Transport: http.DefaultTransport})

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}
	agentStore, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("opening agent store: %v", err)
	}
	defer agentStore.Close()

	beaconDir := t.TempDir()
	client := protocol.New(agentStore, agentID)
	res, err := client.Create(protocol.CreateRequest{
		Description: "relay via Client.Create",
		Transport: &protocol.P2PHTTPTransport{
			RelayEndpoint: relay.URL,
			Dir:           t.TempDir(),
			// Transport intentionally nil: the relay path must not require a host.
		},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Client.Create relay: %v", err)
	}

	if res.RelayEndpoint != relay.URL {
		t.Errorf("RelayEndpoint = %q, want %q", res.RelayEndpoint, relay.URL)
	}
	if res.CampfireID == "" {
		t.Error("CampfireID empty")
	}

	// Membership recorded as p2p-http.
	m, err := agentStore.GetMembership(res.CampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership not recorded by Client.Create relay path")
	}
	if m.TransportType != "p2p-http" {
		t.Errorf("transport_type = %q, want p2p-http", m.TransportType)
	}

	// Relay recorded as a peer endpoint.
	peers, err := agentStore.ListPeerEndpoints(res.CampfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}
	foundRelay := false
	for _, p := range peers {
		if p.Endpoint == relay.URL {
			foundRelay = true
		}
	}
	if !foundRelay {
		t.Errorf("relay endpoint %q not recorded as peer; got %v", relay.URL, peers)
	}

	// Local beacon published (a local cache pointing at the relay).
	if res.BeaconPath == "" {
		t.Error("BeaconPath empty — local beacon not published")
	} else if _, statErr := os.Stat(res.BeaconPath); statErr != nil {
		t.Errorf("beacon file not written at %s: %v", res.BeaconPath, statErr)
	}
	if res.Beacon == nil {
		t.Error("Beacon object nil")
	}
}
