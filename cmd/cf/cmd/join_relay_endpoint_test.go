package cmd

// Test for campfireagent-0cb: CLI join via relay stores relay as peer endpoint.
//
// joinP2PHTTP must upsert the --via endpoint as a peer endpoint using the
// campfire pubkey as the member identifier. Serverless relays don't include
// themselves in the peer list they return, so without this fix the joiner
// has zero sync targets.

import (
	"crypto/ed25519"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/store"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
)

// TestJoinP2PHTTP_RelayStoredAsPeerEndpoint verifies that after joining via a
// relay endpoint, the relay URL appears in ListPeerEndpoints keyed by the
// campfire pubkey hex. This ensures syncFromHTTPPeers has at least one target.
func TestJoinP2PHTTP_RelayStoredAsPeerEndpoint(t *testing.T) {
	// Generate campfire key pair.
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID := fmt.Sprintf("%x", cfPub)

	// Generate host (relay) identity.
	hostID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating host identity: %v", err)
	}

	// Set up host store and membership so the transport can serve join requests.
	hostStore, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatalf("opening host store: %v", err)
	}
	defer hostStore.Close()

	// Write campfire state CBOR to a temp dir so the transport can load it.
	stateDir := t.TempDir()
	state := campfire.CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            cfPriv,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("encoding campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "campfire.cbor"), stateData, 0600); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// Add membership to host store so the transport knows the campfire.
	if err := hostStore.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: stateDir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding host membership: %v", err)
	}

	// Override the SSRF-safe HTTP client so localhost connections are allowed in tests.
	cfhttp.OverrideHTTPClientForTest(&http.Client{})
	defer cfhttp.OverrideHTTPClientForTest(&http.Client{Transport: http.DefaultTransport})

	// Start the relay transport on an OS-assigned port. Bind to "127.0.0.1:0"
	// and read the actual address from tr.Addr() after Start()
	// (campfireagent-c37; replaces PID-modulo block at 22500-22599).
	tr := cfhttp.New("127.0.0.1:0", hostStore)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("key not found: %s", id)
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting relay transport: %v", err)
	}
	defer tr.Stop() //nolint:errcheck
	relayEP := fmt.Sprintf("http://%s", tr.Addr())
	tr.SetSelfInfo(hostID.PublicKeyHex(), relayEP)
	time.Sleep(20 * time.Millisecond)

	// Set up CF_HOME for joinP2PHTTP (needs to write campfire state files).
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	// Generate joiner identity.
	joinerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}

	// Open joiner store.
	joinerStore, err := store.Open(filepath.Join(t.TempDir(), "joiner.db"))
	if err != nil {
		t.Fatalf("opening joiner store: %v", err)
	}
	defer joinerStore.Close()

	// Call joinP2PHTTP — this is the function under test.
	if err := joinP2PHTTP(campfireID, joinerID, joinerStore, relayEP, "", "", "", ""); err != nil {
		t.Fatalf("joinP2PHTTP: %v", err)
	}

	// Verify relay URL is stored as a peer endpoint.
	peers, err := joinerStore.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}

	cfPubKeyHex := fmt.Sprintf("%x", cfPub)
	found := false
	for _, p := range peers {
		if p.Endpoint == relayEP && p.MemberPubkey == cfPubKeyHex {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("relay endpoint %q with campfire pubkey %q not found in ListPeerEndpoints; got %d peers: %v",
			relayEP, cfPubKeyHex, len(peers), peers)
	}
}
