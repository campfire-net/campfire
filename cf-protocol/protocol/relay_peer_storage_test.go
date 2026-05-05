package protocol_test

// Tests for the v0.17.2 fix: protocol.Client.Join stores the relay URL
// (PeerEndpoint) as a peer endpoint so syncFromHTTPPeers can pull from it.
// Without this fix, endpointless joiners have zero sync targets and
// cf read returns empty.

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
)

// TestJoinP2PHTTP_StoresRelayAsPeer verifies that after joinP2PHTTP, the relay
// URL (PeerEndpoint) is stored in peer_endpoints so syncFromHTTPPeers can pull
// from it. This is the critical v0.17.2 fix: without it, endpointless joiners
// have zero sync targets and cf read returns empty.
//
// The test uses a real in-process HTTP transport (no mocks), a real SQLite store,
// and a real P2P join. It then queries the joiner's store and asserts the relay
// URL is present in ListPeerEndpoints.
func TestJoinP2PHTTP_StoresRelayAsPeer(t *testing.T) {
	transportDirA := t.TempDir()
	transportDirB := t.TempDir()
	beaconDir := t.TempDir()

	// Client A: creator with real in-process HTTP transport (OS-assigned port).
	clientA := newJoinClient(t)
	sA := clientA.ClientStore()
	trA := cfhttp.New("127.0.0.1:0", sA)
	if err := trA.Start(); err != nil {
		t.Fatalf("start transport A: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	endpointA := fmt.Sprintf("http://%s", trA.Addr())

	createResult, err := clientA.Create(protocol.CreateRequest{
		Transport: &protocol.P2PHTTPTransport{Transport: trA, MyEndpoint: endpointA, Dir: transportDirA},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := createResult.CampfireID

	// Client B: joiner — no running HTTP transport (simulates an endpointless joiner
	// that relies on the relay for sync). PeerEndpoint = endpointA (the relay).
	clientB := newJoinClient(t)
	sB := clientB.ClientStore()
	_, err = clientB.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport: &protocol.P2PHTTPTransport{
			PeerEndpoint: endpointA,
			Dir:          transportDirB,
		},
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// The critical assertion: endpointA (the relay) must appear in B's peer_endpoints
	// so that syncFromHTTPPeers can pull messages from it.
	peers, err := sB.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("B.Store.ListPeerEndpoints: %v", err)
	}

	found := false
	for _, p := range peers {
		if p.Endpoint == endpointA {
			found = true
			break
		}
	}
	if !found {
		endpoints := make([]string, len(peers))
		for i, p := range peers {
			endpoints[i] = p.Endpoint
		}
		t.Errorf("relay URL %q not found in B's peer_endpoints after joinP2PHTTP — v0.17.2 regression; got: %v", endpointA, endpoints)
	}
}

func TestJoinP2PHTTP_JoinedAtIsNanoseconds(t *testing.T) {
	// Verify that after any join, JoinedAt is in nanoseconds (> 1e18).
	// This is a regression guard for the v0.17.1 fix.

	dir, err := os.MkdirTemp("", "cf-test-")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	trDir, _ := os.MkdirTemp("", "cf-tr-")
	defer os.RemoveAll(trDir)

	client := protocol.New(s, agentID)
	result, err := client.Create(protocol.CreateRequest{
		Transport: &protocol.FilesystemTransport{Dir: trDir},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	m, err := s.GetMembership(result.CampfireID)
	if err != nil || m == nil {
		t.Fatalf("getting membership: %v", err)
	}

	if m.JoinedAt < 1e18 {
		t.Errorf("JoinedAt=%d is seconds (< 1e18), not nanoseconds — v0.17.1 regression", m.JoinedAt)
	}

	joinTime := time.Unix(0, m.JoinedAt)
	if time.Since(joinTime) > time.Minute {
		t.Errorf("JoinedAt %v is more than 1 minute old", joinTime)
	}
	t.Logf("JoinedAt: %v (correct nanoseconds)", joinTime.Format(time.RFC3339Nano))
}
