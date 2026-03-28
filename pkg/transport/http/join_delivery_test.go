package http_test

// Tests for delivery preference validation on join (campfire-agent-9er):
//
// handleJoin must enforce delivery mode compatibility when a joiner provides
// an endpoint (JoinerEndpoint non-empty). The campfire's on-disk CampfireState
// is consulted for DeliveryModes; nil/absent defaults to ["pull"] via
// campfire.EffectiveDeliveryModes.
//
// Port block: 540-559 (this file uses 540-543).

import (
	"crypto/ed25519"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// setupDeliveryModeServer starts a transport with a campfire whose DeliveryModes
// are written to disk. stateDir is the transport dir; campfireID is the key hex.
// modes is the slice stored in CampfireState.DeliveryModes (nil = omit field).
func setupDeliveryModeServer(t *testing.T, portOffset int, modes []string) (campfireID, ep string, sHost store.Store) {
	t.Helper()

	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID = fmt.Sprintf("%x", cfPub)

	selfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating host identity: %v", err)
	}

	// Write CampfireState CBOR to the transport dir.
	stateDir := t.TempDir()
	state := campfire.CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            cfPriv,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
		DeliveryModes:         modes, // nil omits the field (backward compat)
	}
	stateData, encErr := cfencoding.Marshal(state)
	if encErr != nil {
		t.Fatalf("encoding campfire state: %v", encErr)
	}
	if writeErr := os.WriteFile(filepath.Join(stateDir, "campfire.cbor"), stateData, 0600); writeErr != nil {
		t.Fatalf("writing campfire state: %v", writeErr)
	}

	sHost = tempStore(t)
	if addErr := sHost.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: stateDir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); addErr != nil {
		t.Fatalf("adding membership: %v", addErr)
	}

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+portOffset)
	ep = fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, sHost)
	tr.SetSelfInfo(selfID.PublicKeyHex(), ep)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	return campfireID, ep, sHost
}

// TestJoinEndpointRejectedWhenPullOnly verifies that joining with an endpoint
// is rejected (400) when the campfire's DeliveryModes is ["pull"].
func TestJoinEndpointRejectedWhenPullOnly(t *testing.T) {
	campfireID, ep, _ := setupDeliveryModeServer(t, 540, []string{campfire.DeliveryModePull})

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner: %v", err)
	}

	// Join with a non-empty endpoint — should be rejected.
	req := buildJoinRequest(t, ep, campfireID, joiner, "http://203.0.113.50:9050")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 400 for pull-only campfire with endpoint, got %d: %s", resp.StatusCode, body)
	}

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusBadRequest {
		_ = body // drain
	}
}

// TestJoinEndpointAcceptedWhenPushSupported verifies that joining with an endpoint
// succeeds (200) when the campfire's DeliveryModes includes "push".
func TestJoinEndpointAcceptedWhenPushSupported(t *testing.T) {
	campfireID, ep, sHost := setupDeliveryModeServer(t, 541,
		[]string{campfire.DeliveryModePull, campfire.DeliveryModePush})

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner: %v", err)
	}

	// Join with a non-empty endpoint — should succeed.
	req := buildJoinRequest(t, ep, campfireID, joiner, "http://203.0.113.51:9051")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for push-supported campfire with endpoint, got %d: %s", resp.StatusCode, body)
	}

	// Endpoint must be stored in peer_endpoints.
	peers, listErr := sHost.ListPeerEndpoints(campfireID)
	if listErr != nil {
		t.Fatalf("listing peers: %v", listErr)
	}
	found := false
	for _, p := range peers {
		if p.MemberPubkey == joiner.PublicKeyHex() {
			found = true
			if p.Endpoint != "http://203.0.113.51:9051" {
				t.Errorf("stored endpoint = %q, want %q", p.Endpoint, "http://203.0.113.51:9051")
			}
			break
		}
	}
	if !found {
		t.Errorf("joiner endpoint not stored in peer_endpoints after successful join")
	}
}

// TestJoinNoEndpointSucceedsOnPullOnly verifies that joining without an endpoint
// succeeds (200) regardless of DeliveryModes.
func TestJoinNoEndpointSucceedsOnPullOnly(t *testing.T) {
	campfireID, ep, _ := setupDeliveryModeServer(t, 542, []string{campfire.DeliveryModePull})

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner: %v", err)
	}

	// Join with empty endpoint — should succeed (pull-only join).
	req := buildJoinRequest(t, ep, campfireID, joiner, "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for pull-only campfire without endpoint, got %d: %s", resp.StatusCode, body)
	}
}

// TestJoinEndpointRejectedWhenNilDeliveryModes verifies backward compatibility:
// a campfire with nil DeliveryModes (legacy, pre-field-9) defaults to pull-only,
// so joining with an endpoint is rejected (400).
func TestJoinEndpointRejectedWhenNilDeliveryModes(t *testing.T) {
	// Pass nil modes — the CBOR field will be omitted (backward compat).
	campfireID, ep, _ := setupDeliveryModeServer(t, 543, nil)

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner: %v", err)
	}

	// Join with a non-empty endpoint — nil modes → pull-only → should be rejected.
	req := buildJoinRequest(t, ep, campfireID, joiner, "http://203.0.113.53:9053")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 400 for nil DeliveryModes campfire with endpoint, got %d: %s", resp.StatusCode, body)
	}
}
