package http_test

// Tests for campfire-agent-4r3:
// 1. TestJoinResponseIncludesEncryptedFlag — encrypted campfire → JoinResponse.Encrypted=true
//    (and unencrypted → false)
//
// Port block: 620–624 (this file uses 620–624).

import (
	"encoding/json"
	"fmt"
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
	"crypto/ed25519"
)

// setupEncryptedCampfireServer starts a transport whose campfire state has
// Encrypted=encrypted and DeliveryModes=["pull","push"].
// Returns the campfire ID, server endpoint, and the admitting store.
func setupEncryptedCampfireServer(t *testing.T, portOffset int, encrypted bool) (campfireID, ep string, sHost store.Store) {
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

	// Write campfire state with Encrypted flag and push+pull delivery modes.
	stateDir := t.TempDir()
	state := campfire.CampfireState{
		PublicKey:     cfPub,
		PrivateKey:    cfPriv,
		JoinProtocol:  "open",
		Threshold:     1,
		Encrypted:     encrypted,
		DeliveryModes: []string{campfire.DeliveryModePull, campfire.DeliveryModePush},
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
		Encrypted:    encrypted,
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

// TestJoinResponseIncludesEncryptedFlag verifies that:
//   - an encrypted campfire's JoinResponse has Encrypted=true
//   - an unencrypted campfire's JoinResponse has Encrypted=false
//   - Join() propagates the Encrypted flag into JoinResult.Encrypted
func TestJoinResponseIncludesEncryptedFlag(t *testing.T) {
	t.Run("encrypted campfire sets Encrypted=true in JoinResponse", func(t *testing.T) {
		campfireID, ep, _ := setupEncryptedCampfireServer(t, 620, true)

		joiner, err := identity.Generate()
		if err != nil {
			t.Fatalf("generating joiner: %v", err)
		}

		req := buildJoinRequest(t, ep, campfireID, joiner, "")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("join HTTP error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var joinResp cfhttp.JoinResponse
		if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
			t.Fatalf("decoding join response: %v", err)
		}

		if !joinResp.Encrypted {
			t.Errorf("expected JoinResponse.Encrypted=true for encrypted campfire, got false")
		}
	})

	t.Run("unencrypted campfire sets Encrypted=false in JoinResponse", func(t *testing.T) {
		campfireID, ep, _ := setupEncryptedCampfireServer(t, 621, false)

		joiner, err := identity.Generate()
		if err != nil {
			t.Fatalf("generating joiner: %v", err)
		}

		req := buildJoinRequest(t, ep, campfireID, joiner, "")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("join HTTP error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var joinResp cfhttp.JoinResponse
		if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
			t.Fatalf("decoding join response: %v", err)
		}

		if joinResp.Encrypted {
			t.Errorf("expected JoinResponse.Encrypted=false for unencrypted campfire, got true")
		}
	})

	t.Run("Join() client propagates Encrypted flag into JoinResult", func(t *testing.T) {
		campfireID, ep, _ := setupEncryptedCampfireServer(t, 622, true)

		joiner, err := identity.Generate()
		if err != nil {
			t.Fatalf("generating joiner: %v", err)
		}

		result, err := cfhttp.Join(ep, campfireID, joiner, "")
		if err != nil {
			t.Fatalf("Join() failed: %v", err)
		}

		if !result.Encrypted {
			t.Errorf("expected JoinResult.Encrypted=true for encrypted campfire, got false")
		}
	})
}
