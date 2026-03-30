package protocol_test

// Tests for protocol.Client.Create() and Client.Join() — campfire-agent-q7n.
//
// Done conditions:
// 1. CREATE-JOIN ROUND-TRIP (filesystem): A calls Create(), B calls Join(), B sends, A reads.
// 2. CREATE-JOIN ROUND-TRIP (P2P HTTP): same over real in-process HTTP servers.
// 3. BEACON PUBLISHED: after Create(), beacon file exists with correct campfire pubkey.
// 4. SELF-ADMITTED: after Create(), creator's pubkey appears as role=full member.
// 5. DKG COMPLETED (P2P HTTP, threshold=2): after Create(), GetThresholdShare non-nil.
//
// No mocks. Real filesystem dirs, real in-process HTTP servers, real SQLite stores.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/threshold"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// portBaseCreate returns a per-process port offset for create_test.go.
// Range: 22000 + pid%500. Distinct from other protocol test files.
// (portBaseFROST=21000, portBaseP2P uses httptest so no port conflicts).
func portBaseCreate() int {
	return 22000 + (os.Getpid() % 500)
}

// TestCreate runs all Create+Join sub-tests.
func TestCreate(t *testing.T) {
	t.Run("FilesystemRoundTrip", testCreateFilesystemRoundTrip)
	t.Run("P2PHTTPRoundTrip", testCreateP2PHTTPRoundTrip)
	t.Run("BeaconPublished", testCreateBeaconPublished)
	t.Run("SelfAdmitted", testCreateSelfAdmitted)
	t.Run("DKGCompleted", testCreateDKGCompleted)
}

// testCreateFilesystemRoundTrip: Client A calls Create(). Client B calls Join()
// on the same campfire. B calls Send(). A calls Read(). A receives B's message.
func testCreateFilesystemRoundTrip(t *testing.T) {
	t.Helper()

	transportBaseDir := t.TempDir()
	beaconDir := t.TempDir()

	// Client A: creator.
	configDirA := t.TempDir()
	clientA, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	result, err := clientA.Create(protocol.CreateRequest{
		Transport: &protocol.FilesystemTransport{Dir: transportBaseDir},
		BeaconDir:     beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := result.CampfireID

	// The campfire-specific dir is transportBaseDir/campfireID.
	campfireDir := filepath.Join(transportBaseDir, campfireID)

	// Client B: joiner.
	configDirB := t.TempDir()
	clientB, err := protocol.Init(configDirB)
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	// Join using the campfire-specific dir (not the base dir).
	// joinFilesystem uses fs.ForDir(req.TransportDir), which uses rootDir mode:
	// CampfireDir() returns req.TransportDir directly, so campfire.cbor lives at
	// req.TransportDir/campfire.cbor.
	if _, err := clientB.Join(protocol.JoinRequest{
		Transport: &protocol.FilesystemTransport{Dir: campfireDir},
		CampfireID:    campfireID,
	}); err != nil {
		t.Fatalf("Join: %v", err)
	}

	// B sends a message.
	want := "hello from B via filesystem"
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("B.Send: %v", err)
	}

	// A reads — must see B's message (syncs from filesystem transport).
	readResult, err := clientA.Read(protocol.ReadRequest{
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("A.Read: %v", err)
	}

	// Find B's message in the results.
	found := false
	for _, m := range readResult.Messages {
		if string(m.Payload) == want {
			found = true
			break
		}
	}
	if !found {
		payloads := make([]string, len(readResult.Messages))
		for i, m := range readResult.Messages {
			payloads[i] = string(m.Payload)
		}
		t.Errorf("A did not receive B's message %q; got: %v", want, payloads)
	}
}

// testCreateP2PHTTPRoundTrip: Create+Join+Send+Read over real in-process HTTP servers.
func testCreateP2PHTTPRoundTrip(t *testing.T) {
	t.Helper()

	// Override HTTP client to allow loopback connections.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	t.Cleanup(func() {
		cfhttp.OverrideHTTPClientForTest(&http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport,
		})
	})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)
	t.Cleanup(func() {
		cfhttp.OverridePollTransportForTest(http.DefaultTransport)
	})

	base := portBaseCreate()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+0)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+1)
	endpointA := fmt.Sprintf("http://%s", addrA)
	endpointB := fmt.Sprintf("http://%s", addrB)

	transportDirA := t.TempDir()
	transportDirB := t.TempDir()
	beaconDir := t.TempDir()

	// Client A: creator with its own store and transport.
	configDirA := t.TempDir()
	clientA, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	sA := clientA.Store()
	trA := cfhttp.New(addrA, sA)
	if err := trA.Start(); err != nil {
		t.Fatalf("Start transport A: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	createResult, err := clientA.Create(protocol.CreateRequest{
		Transport: &protocol.P2PHTTPTransport{Transport: trA, MyEndpoint: endpointA, Dir: transportDirA},
		BeaconDir:      beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := createResult.CampfireID

	// Client B: joiner with its own store and transport.
	configDirB := t.TempDir()
	clientB, err := protocol.Init(configDirB)
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	sB := clientB.Store()
	trB := cfhttp.New(addrB, sB)
	if err := trB.Start(); err != nil {
		t.Fatalf("Start transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	if _, err := clientB.Join(protocol.JoinRequest{
		Transport: &protocol.P2PHTTPTransport{Transport: trB, MyEndpoint: endpointB, PeerEndpoint: endpointA, Dir: transportDirB},
		CampfireID:     campfireID,
	}); err != nil {
		t.Fatalf("Join: %v", err)
	}

	// B sends a message to A (delivered via HTTP peer delivery).
	want := "hello from B via p2p-http"
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("B.Send: %v", err)
	}

	// A reads from its local store (message was pushed by B during Send).
	readResult, err := clientA.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		SkipSync:   true,
	})
	if err != nil {
		t.Fatalf("A.Read: %v", err)
	}

	found := false
	for _, m := range readResult.Messages {
		if string(m.Payload) == want {
			found = true
			break
		}
	}
	if !found {
		payloads := make([]string, len(readResult.Messages))
		for i, m := range readResult.Messages {
			payloads[i] = string(m.Payload)
		}
		t.Errorf("A did not receive B's message %q via p2p-http; got: %v", want, payloads)
	}
}

// testCreateBeaconPublished: after Create(), beacon file exists at expected path.
// Parse and verify campfire public key + transport metadata.
func testCreateBeaconPublished(t *testing.T) {
	t.Helper()

	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	configDir := t.TempDir()
	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	result, err := client.Create(protocol.CreateRequest{
		Transport: &protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:     beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Beacon file must exist at beaconDir/{campfireID}.beacon.
	entries, err := os.ReadDir(beaconDir)
	if err != nil {
		t.Fatalf("reading beacon dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no beacon files found after Create")
	}

	// Load and verify the beacon.
	beaconPath := filepath.Join(beaconDir, entries[0].Name())
	data, err := os.ReadFile(beaconPath)
	if err != nil {
		t.Fatalf("reading beacon file: %v", err)
	}

	var b beacon.Beacon
	if err := cfencoding.Unmarshal(data, &b); err != nil {
		t.Fatalf("unmarshalling beacon: %v", err)
	}

	// Campfire public key must match CreateResult.CampfireID.
	gotID := fmt.Sprintf("%x", b.CampfireID)
	if gotID != result.CampfireID {
		t.Errorf("beacon campfire ID mismatch: got %s, want %s", gotID, result.CampfireID)
	}

	// Beacon signature must be valid (signed by campfire private key).
	if !b.Verify() {
		t.Error("beacon signature is invalid")
	}

	// Transport metadata must be present.
	if b.Transport.Protocol == "" {
		t.Error("beacon transport protocol is empty")
	}
}

// testCreateSelfAdmitted: after Create(), creator's pubkey appears as role=full member.
func testCreateSelfAdmitted(t *testing.T) {
	t.Helper()

	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	configDir := t.TempDir()
	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	result, err := client.Create(protocol.CreateRequest{
		Transport: &protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:     beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify membership exists in the store.
	m, err := client.Store().GetMembership(result.CampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("creator has no membership record after Create")
	}
	if campfire.EffectiveRole(m.Role) != campfire.RoleFull {
		t.Errorf("creator role is %q, want %q", m.Role, campfire.RoleFull)
	}

	// Verify creator's pubkey appears in the transport member files.
	creatorPubHex := client.Identity().PublicKeyHex()
	memberFiles, err := os.ReadDir(filepath.Join(m.TransportDir, "members"))
	if err != nil {
		t.Fatalf("reading members dir: %v", err)
	}

	found := false
	for _, e := range memberFiles {
		if e.Name() == creatorPubHex+".cbor" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("creator pubkey %s not found in members directory; files: %v", creatorPubHex, memberFiles)
	}
}

// testCreateDKGCompleted: after Create() with threshold=2 on P2P HTTP transport,
// store.GetThresholdShare() returns non-nil share.
func testCreateDKGCompleted(t *testing.T) {
	t.Helper()

	// Override HTTP client to allow loopback connections.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	t.Cleanup(func() {
		cfhttp.OverrideHTTPClientForTest(&http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport,
		})
	})

	base := portBaseCreate()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+2)
	endpointA := fmt.Sprintf("http://%s", addrA)

	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	configDir := t.TempDir()
	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	tr := cfhttp.New(addrA, client.Store())
	if err := tr.Start(); err != nil {
		t.Fatalf("Start transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	result, err := client.Create(protocol.CreateRequest{
		Transport: &protocol.P2PHTTPTransport{Transport: tr, MyEndpoint: endpointA, Dir: transportDir},
		BeaconDir:      beaconDir,
		Threshold:      2,
	})
	if err != nil {
		t.Fatalf("Create with threshold=2: %v", err)
	}

	// GetThresholdShare must return a non-nil share for the creator.
	share, err := client.Store().GetThresholdShare(result.CampfireID)
	if err != nil {
		t.Fatalf("GetThresholdShare: %v", err)
	}
	if share == nil {
		t.Fatal("expected non-nil threshold share after Create with threshold=2, got nil")
	}

	// Verify the share deserializes correctly and creator gets participant ID 1.
	pid, dkgResult, err := threshold.UnmarshalResult(share.SecretShare)
	if err != nil {
		t.Fatalf("UnmarshalResult: %v", err)
	}
	if pid != 1 {
		t.Errorf("expected participant ID 1 for creator, got %d", pid)
	}
	if dkgResult == nil {
		t.Fatal("UnmarshalResult returned nil DKGResult")
	}
}
