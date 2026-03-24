package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// setupBridgeFilterEnv creates a minimal environment for bridge filter tests.
func setupBridgeFilterEnv(t *testing.T, campfireID string) (*fs.Transport, store.Store, *identity.Identity, string) {
	t.Helper()

	// Set up filesystem transport.
	tmpDir := t.TempDir()
	cfDir := filepath.Join(tmpDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	fsTransport := fs.New(tmpDir)
	return fsTransport, s, agentID, tmpDir
}

// TestBridgeTagFilterRelaysMatchingMessages verifies that when --tag is set,
// only messages carrying that tag are delivered to the HTTP peer.
func TestBridgeTagFilterRelaysMatchingMessages(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockHTTPPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	campfireID := "bridge-filter-match-campfire-0000000000000000000000000000000000"
	fsTransport, s, agentID, _ := setupBridgeFilterEnv(t, campfireID)

	// Write an "escalation"-tagged message.
	msgTagged, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("escalation msg"), []string{"escalation"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMessage(campfireID, msgTagged); err != nil {
		t.Fatal(err)
	}

	// Write an untagged message.
	msgUntagged, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("untagged msg"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMessage(campfireID, msgUntagged); err != nil {
		t.Fatal(err)
	}

	forwarded := buildForwardedSet(campfireID, fsTransport, s)

	// Run pump with tag filter "escalation".
	pumpFSToHTTP(campfireID, fsTransport, s, agentID, srv.URL, forwarded, []string{"escalation"})

	// Only the tagged message should be delivered.
	delivered := peer.getDelivered()
	if len(delivered) != 1 {
		t.Fatalf("expected 1 delivered message, got %d", len(delivered))
	}
	if string(delivered[0].Payload) != "escalation msg" {
		t.Errorf("expected 'escalation msg', got %q", string(delivered[0].Payload))
	}
}

// TestBridgeTagFilterStoresUnmatchedLocally verifies that messages not matching
// the tag filter are still stored locally (cursor advances) but not relayed.
func TestBridgeTagFilterStoresUnmatchedLocally(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockHTTPPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	campfireID := "bridge-filter-store-campfire-000000000000000000000000000000000000"
	fsTransport, s, agentID, _ := setupBridgeFilterEnv(t, campfireID)

	// Write a message with a non-matching tag.
	msgOther, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("other msg"), []string{"status"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMessage(campfireID, msgOther); err != nil {
		t.Fatal(err)
	}

	forwarded := buildForwardedSet(campfireID, fsTransport, s)

	// Run pump filtering for "escalation" only.
	pumpFSToHTTP(campfireID, fsTransport, s, agentID, srv.URL, forwarded, []string{"escalation"})

	// Nothing should be delivered to HTTP.
	delivered := peer.getDelivered()
	if len(delivered) != 0 {
		t.Fatalf("expected 0 delivered messages, got %d", len(delivered))
	}

	// But the message IS stored locally.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message in local store, got %d", len(msgs))
	}
	if string(msgs[0].Payload) != "other msg" {
		t.Errorf("expected 'other msg' in store, got %q", string(msgs[0].Payload))
	}

	// And the message ID is in the forwarded set (won't be re-processed).
	if !forwarded[msgOther.ID] {
		t.Error("expected unrelayed message ID in forwarded set")
	}
}

// TestBridgeNoTagFilterRelaysAll verifies that omitting --tag relays all messages
// (backward-compatible default behavior).
func TestBridgeNoTagFilterRelaysAll(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockHTTPPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	campfireID := "bridge-filter-all-campfire-000000000000000000000000000000000000"
	fsTransport, s, agentID, _ := setupBridgeFilterEnv(t, campfireID)

	// Write messages with different tags.
	for _, payload := range []struct {
		body string
		tags []string
	}{
		{"msg a", []string{"escalation"}},
		{"msg b", []string{"status"}},
		{"msg c", nil},
	} {
		msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload.body), payload.tags, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := fsTransport.WriteMessage(campfireID, msg); err != nil {
			t.Fatal(err)
		}
	}

	forwarded := buildForwardedSet(campfireID, fsTransport, s)

	// Run pump with NO tag filter (nil).
	pumpFSToHTTP(campfireID, fsTransport, s, agentID, srv.URL, forwarded, nil)

	// All 3 messages should be delivered.
	delivered := peer.getDelivered()
	if len(delivered) != 3 {
		t.Fatalf("expected 3 delivered messages, got %d", len(delivered))
	}
}

// TestBridgeTagFilterORSemantics verifies that multiple --tag values use OR semantics:
// a message matching any of the tags is relayed.
func TestBridgeTagFilterORSemantics(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 5 * time.Second})
	defer cfhttp.OverrideHTTPClientForTest(http.DefaultClient)

	peer := newMockHTTPPeer()
	srv := httptest.NewServer(peer.handler())
	defer srv.Close()

	campfireID := "bridge-filter-or-campfire-0000000000000000000000000000000000000"
	fsTransport, s, agentID, _ := setupBridgeFilterEnv(t, campfireID)

	// escalation-tagged: should relay (matches first filter)
	msgA, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("escalation"), []string{"escalation"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMessage(campfireID, msgA); err != nil {
		t.Fatal(err)
	}

	// blocker-tagged: should relay (matches second filter)
	msgB, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("blocker"), []string{"blocker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMessage(campfireID, msgB); err != nil {
		t.Fatal(err)
	}

	// status-tagged: should NOT relay (matches neither filter)
	msgC, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("status"), []string{"status"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTransport.WriteMessage(campfireID, msgC); err != nil {
		t.Fatal(err)
	}

	forwarded := buildForwardedSet(campfireID, fsTransport, s)

	// Run pump with two tag filters using OR semantics.
	pumpFSToHTTP(campfireID, fsTransport, s, agentID, srv.URL, forwarded, []string{"escalation", "blocker"})

	delivered := peer.getDelivered()
	if len(delivered) != 2 {
		t.Fatalf("expected 2 delivered messages (escalation + blocker), got %d", len(delivered))
	}

	// Verify the non-matching message is still stored locally.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages in local store, got %d", len(msgs))
	}
}
