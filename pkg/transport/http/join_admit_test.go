package http_test

// Tests for campfire-agent-4r3:
// 1. TestJoinResponseIncludesEncryptedFlag — encrypted campfire → JoinResponse.Encrypted=true
//    (and unencrypted → false)
// 2. TestAdmittingNodeWritesMemberRecord — when admitter is set, after a successful join
//    the admitter's store contains a membership record for the joiner's campfire.
//
// Port block: 620–624 (this file uses 620–624).

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/admission"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
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

// TestAdmittingNodeWritesMemberRecord verifies that when an admitter is configured,
// a successful join causes the admitter's store to contain a membership record for
// the joined campfire. The admitter uses a SEPARATE store from the transport store
// so that AdmitMember's AddMembership call doesn't conflict with the transport's
// own membership record for this campfire.
//
// Production usage: the admitter's Store typically points to a member-tracking
// layer separate from the transport's campfire_memberships table.
func TestAdmittingNodeWritesMemberRecord(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID := fmt.Sprintf("%x", cfPub)

	selfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating host identity: %v", err)
	}

	// Write campfire state file so the handler can read DeliveryModes and Encrypted.
	stateDir := t.TempDir()
	state := campfire.CampfireState{
		PublicKey:     cfPub,
		PrivateKey:    cfPriv,
		JoinProtocol:  "open",
		Threshold:     1,
		DeliveryModes: []string{campfire.DeliveryModePull, campfire.DeliveryModePush},
	}
	stateData, encErr := cfencoding.Marshal(state)
	if encErr != nil {
		t.Fatalf("encoding campfire state: %v", encErr)
	}
	if writeErr := os.WriteFile(filepath.Join(stateDir, "campfire.cbor"), stateData, 0600); writeErr != nil {
		t.Fatalf("writing campfire state: %v", writeErr)
	}

	// Transport store: the host transport's own membership record.
	sHost := tempStore(t)
	if addErr := sHost.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: stateDir,
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); addErr != nil {
		t.Fatalf("adding membership to host store: %v", addErr)
	}

	// Admission store: separate store for AdmitMember to write into.
	// AdmitMember calls deps.Store.AddMembership(campfireID, ...) — using a
	// separate store avoids the PRIMARY KEY conflict with sHost's existing record.
	sAdmission := tempStore(t)

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+623)
	ep := fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, sHost)
	tr.SetSelfInfo(selfID.PublicKeyHex(), ep)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	})

	// Configure the admitter with the separate admission store.
	admitter := &admission.AdmitterDeps{
		Store:         sAdmission,
		HTTPTransport: tr,
	}
	tr.SetAdmitter(admitter)

	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner: %v", err)
	}

	// Join — admitter will call sAdmission.AddMembership for the campfire.
	_, err = cfhttp.Join(ep, campfireID, joiner, "")
	if err != nil {
		t.Fatalf("Join() failed: %v", err)
	}

	// Verify: the admission store must now contain a membership record for this campfire.
	// AdmitMember calls deps.Store.AddMembership with CampfireID=campfireID.
	membership, getMsErr := sAdmission.GetMembership(campfireID)
	if getMsErr != nil {
		t.Fatalf("GetMembership on admission store failed: %v", getMsErr)
	}
	if membership == nil {
		t.Fatal("expected membership record in admission store after join, got nil — admitter not called?")
	}
	if membership.CampfireID != campfireID {
		t.Errorf("membership.CampfireID = %q, want %q", membership.CampfireID, campfireID)
	}
	// Role should be "full" (unencrypted campfire, no explicit role).
	if membership.Role != campfire.RoleFull {
		t.Errorf("membership.Role = %q, want %q", membership.Role, campfire.RoleFull)
	}
	// JoinProtocol should match the campfire's protocol.
	if membership.JoinProtocol != "open" {
		t.Errorf("membership.JoinProtocol = %q, want %q", membership.JoinProtocol, "open")
	}
}
