package cmd

// join_nil_pubkey_test.go — B-L1 regression test (campfireagent-8b4).
//
// Done condition: when a relay returns an empty campfire_pub_key in the join
// response, joinP2PHTTP must NOT call UpsertPeerEndpoint. An unusable peer
// entry with an empty member pubkey would confuse syncFromHTTPPeers.

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// TestJoinP2PHTTP_NilCampfirePubKey_NoPeerEndpointStored verifies that when
// the relay returns a join response with an empty campfire_pub_key, joinP2PHTTP
// does not store a peer endpoint (guarded by the len(result.CampfirePubKey)>0
// check added in campfireagent-8b4).
func TestJoinP2PHTTP_NilCampfirePubKey_NoPeerEndpointStored(t *testing.T) {
	// Generate a real campfire key pair so we can build a valid join response.
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID := fmt.Sprintf("%x", cfPub)

	// Build an httptest server that returns a valid join response but with an
	// empty campfire_pub_key field — simulating a relay that doesn't return the
	// campfire pubkey (e.g. an older server or a nil-pubkey edge case).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only handle /campfire/{id}/join.
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}

		// Return a join response with empty campfire_pub_key — this is the
		// B-L1 scenario. We care only about whether joinP2PHTTP stores a
		// peer endpoint with an empty member pubkey.
		resp := cfhttp.JoinResponse{
			// CampfirePubKey intentionally omitted / empty.
			CampfirePubKey:        "",
			JoinProtocol:          "open",
			ReceptionRequirements: []string{},
			Threshold:             1,
			Peers:                 nil,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer ts.Close()

	// Override the SSRF-safe HTTP client so loopback connections work in tests.
	cfhttp.OverrideHTTPClientForTest(&http.Client{})
	defer cfhttp.OverrideHTTPClientForTest(&http.Client{Transport: http.DefaultTransport})

	// Set up CF_HOME.
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	// Create campfire state directory so joinP2PHTTP can write the .cbor file.
	stateDir := filepath.Join(cfHomeDir, "campfires")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatalf("creating campfire state dir: %v", err)
	}

	// Write a campfire state file so joinP2PHTTP doesn't fail on file write.
	dummyState := campfire.CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            cfPriv,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(dummyState)
	if err != nil {
		t.Fatalf("encoding campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, campfireID+".cbor"), stateData, 0600); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

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

	// Call joinP2PHTTP against our mock server. The mock returns an empty
	// campfire_pub_key — the key assertion is that ListPeerEndpoints contains
	// NO entry with an empty MemberPubkey.
	relayEP := ts.URL
	err = joinP2PHTTP(campfireID, joinerID, joinerStore, relayEP, "", "", "", "")
	// joinP2PHTTP may succeed or fail — we care about the peer-endpoint side effect.
	// If it failed before reaching UpsertPeerEndpoint, that's also acceptable.
	if err != nil {
		// Even on error, confirm no empty-pubkey peer endpoint was stored.
		peers, listErr := joinerStore.ListPeerEndpoints(campfireID)
		if listErr != nil {
			t.Logf("ListPeerEndpoints after joinP2PHTTP error: %v", listErr)
			return
		}
		for _, p := range peers {
			if p.MemberPubkey == "" {
				t.Errorf("UpsertPeerEndpoint called with empty MemberPubkey despite nil CampfirePubKey; peer: %+v", p)
			}
		}
		return
	}

	// joinP2PHTTP succeeded — verify no empty-pubkey peer was stored.
	peers, err := joinerStore.ListPeerEndpoints(campfireID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}
	for _, p := range peers {
		if p.MemberPubkey == "" {
			t.Errorf("UpsertPeerEndpoint called with empty MemberPubkey despite nil CampfirePubKey; peer: %+v", p)
		}
	}
}
